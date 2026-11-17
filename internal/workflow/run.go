// Package workflow runs one session end to end: start a sandbox, clone the
// repos onto a session branch, run the agent, commit and push, and (when
// memory is on) have the agent write down what it did.
package workflow

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/memory"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/session"
	"github.com/mistergregg/unire/internal/tools"
)

// Options are settings that are not part of the session itself.
type Options struct {
	Out, Err    io.Writer
	In          io.Reader
	Interactive bool // attach a terminal to the agent
	// GitTokens maps a repo's directory name to the token for its git server.
	// Tokens are used only for clone and push, only toward that repo's own
	// host, and are never given to the agent.
	GitTokens map[string]string
	Memory    string
	CPUs      string
	// Workspace names the volume holding this conversation's repos (see runner.StartSpec).
	Workspace string
	// Mem turns project memory on: the agent starts with Context and, when it
	// finishes, is asked to write a summary and updated briefs. nil means off.
	Mem *MemoryPlan
	// MemoryTimeout limits the memory step; zero means five minutes.
	MemoryTimeout time.Duration
	// Tools are what the agent may ask Unire to do (see package tools). Empty means none.
	Tools []tools.Tool
	// Ask shows the agent's questions to the owner (the ask tool). AskPoll is how
	// often the sandbox is checked for new ones; zero means two seconds.
	Ask     AskFunc
	AskPoll time.Duration
	// Call answers the agent's live tool requests (pr-status and the like): it
	// gets the tool's name and the agent's text and returns what the agent reads.
	Call func(ctx context.Context, tool, args string) string
	// Control carries what the owner says to the agent while it works. nil means
	// the agent is run once and cannot be steered. OnMessage reports what became of
	// each message (delivered, failed).
	Control   <-chan agent.Steer
	OnMessage func(id, state string)
	// OnWorkDone is called once the agent has finished and its work is pushed,
	// before the memory step. It is not called when the session fails earlier.
	OnWorkDone func(res Result, err error)
}

// MemoryPlan is the project memory for this session.
type MemoryPlan struct {
	Context string             // markdown the agent starts with; empty if there is none yet
	Repos   []memory.RepoBrief // previous briefs, Dir matching the repo directory names
}

// Result is what a session produced besides its git branches.
type Result struct {
	Summary       string            // cleaned, scrubbed session summary; empty when memory is off
	SummarySource string            // "agent" or "fallback"
	Briefs        map[string]string // repo directory -> updated brief written by the agent
	Name          string            // a title the agent asked for with the name tool; empty if none
	PRTitle       string            // a pull request title the agent suggested with the pr tool; empty if none
	PRBody        string            // a pull request description the agent wrote with the pr-body tool; empty if none
}

const workspace = "/workspace"

// credentialHelper returns git config that feeds the token from the
// environment to git, for one host only. The token is never written into the
// repo config or the clone URL, and git will not offer it to any other host
// (for example one named by a submodule).
func credentialHelper(scope string) string {
	return "credential." + scope + `.helper=!f() { echo username=unire; echo "password=$UNIRE_GIT_TOKEN"; }; f`
}

// hostScope returns "https://host" for an http(s) clone URL, or "" otherwise.
func hostScope(cloneURL string) string {
	u, err := url.Parse(cloneURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func newRef() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// tail keeps the last few kilobytes written to it.
type tail struct{ buf []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 6000 {
		t.buf = t.buf[len(t.buf)-4000:]
	}
	return len(p), nil
}

// Run executes the session. Work the agent produced is pushed even when the
// agent exits with an error, and the error is returned afterwards.
func Run(ctx context.Context, r runner.Runner, spec session.Spec, opt Options) (Result, error) {
	var res Result
	adapter, err := agent.Get(spec.Agent)
	if err != nil {
		return res, err
	}
	ref := newRef()
	if _, err := adapter.Command(spec.Prompt, spec.Mode, ref); err != nil {
		return res, err // fail before starting anything if the mode is unsupported
	}

	var mounts []runner.Mount
	for _, repo := range spec.Repos {
		if repo.LocalPath != "" {
			mounts = append(mounts, runner.Mount{HostPath: repo.LocalPath, ContainerPath: repo.URL})
		}
	}

	fmt.Fprintf(opt.Err, "starting session %s (agent %s, branch %s)\n", spec.ID, spec.Agent, spec.Branch())
	began := time.Now()
	sb, err := r.Start(ctx, runner.StartSpec{SessionID: spec.ID, Mounts: mounts, Memory: opt.Memory, CPUs: opt.CPUs, Workspace: opt.Workspace, Log: opt.Err})
	if err != nil {
		return res, err
	}
	fmt.Fprintf(opt.Err, "sandbox ready (%.1fs)\n", time.Since(began).Seconds())
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = sb.Stop(stopCtx)
	}()

	w := &worker{sb: sb, opt: opt}

	// Mounted local repos are owned by a different user than the sandbox
	// user. A global setting is needed because git ignores per-command
	// settings for the process that reads the source repo.
	if _, err := w.git(ctx, "", nil, "config", "--global", "--add", "safe.directory", "*"); err != nil {
		return res, fmt.Errorf("git setup: %w", err)
	}

	// A conversation keeps its workspace between turns, so a reply finds the
	// repos where the last turn left them. Requests the agent left in files
	// last time must not be read again.
	_ = w.sb.Exec(ctx, runner.ExecOpts{Argv: []string{"rm", "-rf", tools.Dir, memory.OutDir}})

	startHeads := map[string]string{}
	for _, repo := range spec.Repos {
		repo := repo
		dir := workspace + "/" + repo.Name
		if w.cloned(ctx, dir) {
			// Already here: carry on from what is there. -B creates the branch, or
			// moves to it when it exists, and keeps the work in the tree (a different
			// agent gets its own branch name that starts from this work).
			fmt.Fprintf(opt.Err, "using the existing copy of %s\n", repo.Name)
			if _, err := w.git(ctx, dir, &repo, "checkout", "-B", spec.Branch()); err != nil {
				return res, fmt.Errorf("branch %s: %w", repo.Name, err)
			}
		} else {
			cloneArgs := []string{"clone"}
			if repo.Base != "" {
				cloneArgs = append(cloneArgs, "--branch", repo.Base)
			}
			cloneArgs = append(cloneArgs, repo.URL, dir)
			fmt.Fprintf(opt.Err, "cloning %s\n", repo.Name)
			if _, err := w.git(ctx, "", &repo, cloneArgs...); err != nil {
				return res, fmt.Errorf("clone %s: %w", repo.Name, err)
			}
			// Cloning an earlier turn's branch already puts us on it.
			if repo.Base != spec.Branch() {
				if _, err := w.git(ctx, dir, &repo, "checkout", "-b", spec.Branch()); err != nil {
					return res, fmt.Errorf("branch %s: %w", repo.Name, err)
				}
			}
		}
		head, err := w.git(ctx, dir, &repo, "rev-parse", "HEAD")
		if err != nil {
			return res, err
		}
		startHeads[repo.Name] = strings.TrimSpace(head)
	}

	// Give the agent its memory. Either as a file in /workspace, above every
	// repo, so it is never committed and the agent finds it because it starts
	// there; or, for an agent whose file loading is unverified, at the start of
	// the prompt, which works for any agent.
	prompt := spec.Prompt
	if opt.Mem != nil && opt.Mem.Context != "" {
		if adapter.MemoryMode() == agent.MemoryPrompt && prompt != "" {
			prompt = opt.Mem.Context + "\n\n---\n\n# Your task\n\n" + spec.Prompt
			fmt.Fprintf(opt.Err, "memory: gave the agent project memory (%d characters, at the start of its prompt)\n", len(opt.Mem.Context))
		} else if err := w.writeFile(ctx, workspace+"/"+adapter.MemoryFile(), opt.Mem.Context); err != nil {
			fmt.Fprintf(opt.Err, "memory: could not give the agent its memory: %v\n", err)
		} else {
			fmt.Fprintf(opt.Err, "memory: gave the agent project memory (%d characters in %s)\n", len(opt.Mem.Context), adapter.MemoryFile())
		}
	}
	if tp := tools.Prompt(opt.Tools); tp != "" && prompt != "" {
		prompt += "\n\n---\n\n" + tp
	}
	argv, err := adapter.Command(prompt, spec.Mode, ref)
	if err != nil {
		return res, err
	}

	fmt.Fprintf(opt.Err, "running %s\n", adapter.Name())
	out := &tail{}
	agentOpts := runner.ExecOpts{
		Argv: argv, Dir: workspace, TTY: opt.Interactive,
		Stdout: io.MultiWriter(opt.Out, out), Stderr: opt.Err,
	}
	// An agent that can be steered runs as a process that stays open, so the owner
	// can talk to it while it works (see runLive). The others run once.
	live, canLive := adapter.(agent.Live)
	useLive := canLive && opt.Control != nil && !opt.Interactive && spec.Mode == agent.HandsOff && prompt != ""
	// An agent that can stream its events lets the owner see what it is doing
	// while it works, instead of nothing until the answer arrives.
	var stream *streamWriter
	if s, ok := adapter.(agent.Streamer); ok && !useLive && !opt.Interactive && spec.Mode == agent.HandsOff && prompt != "" {
		if sargv, err := s.StreamCommand(prompt, spec.Mode, ref); err == nil {
			agentOpts.Argv = sargv
			stream = &streamWriter{s: s, out: io.MultiWriter(opt.Out, out), log: opt.Err}
			agentOpts.Stdout = stream
		}
	}
	if opt.Interactive {
		// Unattended runs get no stdin, or the agent waits on it.
		agentOpts.Stdin = opt.In
	}
	askCtx, stopAsks := context.WithCancel(ctx)
	asksDone := make(chan struct{})
	go func() {
		defer close(asksDone)
		if w.asking() {
			w.serveQuestions(askCtx)
		}
	}()
	var agentErr error
	if useLive {
		agentErr = w.runLive(ctx, live, spec, prompt, ref, out)
	} else {
		agentErr = sb.Exec(ctx, agentOpts)
		if stream != nil {
			stream.Close()
		}
	}
	stopAsks()
	<-asksDone
	requestedName := tools.CleanName(w.requestedTool(ctx, tools.Name.Name))
	prTitle := tools.CleanName(w.requestedTool(ctx, tools.PR.Name))
	prBody := w.requestedBody(ctx)

	outcomes, err := w.finish(ctx, spec, startHeads)
	if err != nil {
		return res, err
	}
	changed := false
	for _, o := range outcomes {
		changed = changed || o.changed
	}
	if !changed {
		fmt.Fprintln(opt.Err, "no changes to push")
	}

	if agentErr != nil && ctx.Err() == nil {
		fmt.Fprintf(opt.Err, "hint: if this looks like a sign-in problem, open the Agents page and sign %s in again\n", adapter.Info().Label)
	}
	var workErr error
	if agentErr != nil {
		workErr = fmt.Errorf("agent exited with an error: %w", agentErr)
	}
	// The work is done and pushed. Tell the caller now, so the owner can carry on
	// while the memory step below (which can take a while) finishes in the background.
	if opt.OnWorkDone != nil {
		opt.OnWorkDone(Result{Name: requestedName, PRTitle: prTitle, PRBody: prBody}, workErr)
	}
	if opt.Mem != nil && ctx.Err() == nil {
		res = w.rememberSession(ctx, adapter, spec, ref, startHeads, outcomes, agentErr, out.buf)
	}
	res.Name = requestedName
	res.PRTitle = prTitle
	res.PRBody = prBody
	return res, workErr
}

type worker struct {
	sb  runner.Sandbox
	opt Options
}

// git runs git in the sandbox. For a repo with a token, the token is passed
// for that repo's host only.
func (w *worker) git(ctx context.Context, dir string, repo *session.Repo, args ...string) (string, error) {
	argv := []string{"git"}
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	if repo != nil {
		if tok := w.opt.GitTokens[repo.Name]; tok != "" {
			if scope := hostScope(repo.URL); scope != "" {
				argv = append(argv, "-c", credentialHelper(scope))
				env["UNIRE_GIT_TOKEN"] = tok
			}
		}
	}
	argv = append(argv, args...)
	var out, errb bytes.Buffer
	err := w.sb.Exec(ctx, runner.ExecOpts{Argv: argv, Env: env, Dir: dir, Stdout: &out, Stderr: &errb})
	if err != nil {
		return out.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// writeFile writes content to a path in the sandbox. The path is passed as an
// argument and the content on stdin, so neither is ever parsed by a shell.
func (w *worker) writeFile(ctx context.Context, path, content string) error {
	var errb bytes.Buffer
	err := w.sb.Exec(ctx, runner.ExecOpts{
		Argv:  []string{"sh", "-c", `cat > "$1"`, "sh", path},
		Stdin: strings.NewReader(content), Stderr: &errb,
	})
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// readFile reads at most max bytes of a file in the sandbox.
func (w *worker) readFile(ctx context.Context, path string, max int) (string, error) {
	var out, errb bytes.Buffer
	err := w.sb.Exec(ctx, runner.ExecOpts{Argv: []string{"head", "-c", strconv.Itoa(max), path}, Stdout: &out, Stderr: &errb})
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

type outcome struct {
	changed bool
	pushed  bool
}

// finish commits leftover changes in each repo and pushes every repo whose
// HEAD moved. It reports what happened per repo directory.
func (w *worker) finish(ctx context.Context, spec session.Spec, startHeads map[string]string) (map[string]outcome, error) {
	outcomes := map[string]outcome{}
	for _, repo := range spec.Repos {
		repo := repo
		dir := workspace + "/" + repo.Name
		status, err := w.git(ctx, dir, &repo, "status", "--porcelain")
		if err != nil {
			return outcomes, err
		}
		if strings.TrimSpace(status) != "" {
			if _, err := w.git(ctx, dir, &repo, "add", "-A"); err != nil {
				return outcomes, err
			}
			msg := "unire: " + spec.Task
			if _, err := w.git(ctx, dir, &repo, "-c", "user.name=unire-agent", "-c", "user.email=agent@unire.local", "commit", "-m", msg); err != nil {
				return outcomes, err
			}
		}
		head, err := w.git(ctx, dir, &repo, "rev-parse", "HEAD")
		if err != nil {
			return outcomes, err
		}
		if strings.TrimSpace(head) == startHeads[repo.Name] {
			continue
		}
		o := outcome{changed: true}
		if spec.NoPush {
			fmt.Fprintf(w.opt.Err, "%s: changes committed, not pushing (--no-push)\n", repo.Name)
			outcomes[repo.Name] = o
			continue
		}
		if _, err := w.git(ctx, dir, &repo, "push", "-u", "origin", spec.Branch()); err != nil {
			return outcomes, fmt.Errorf("push %s: %w", repo.Name, err)
		}
		o.pushed = true
		outcomes[repo.Name] = o
		fmt.Fprintf(w.opt.Err, "%s: pushed %s\n", repo.Name, spec.Branch())
	}
	return outcomes, nil
}

// rememberSession asks the agent to write down what it did. When it cannot
// (the agent failed, timed out, or wrote nothing usable), Unire writes a plain
// summary from what it knows. It never returns an error: memory must not make
// a finished session fail.
func (w *worker) rememberSession(ctx context.Context, ad agent.Adapter, spec session.Spec, ref string,
	startHeads map[string]string, outcomes map[string]outcome, agentErr error, agentTail []byte) Result {

	var secrets []string
	for _, t := range w.opt.GitTokens {
		secrets = append(secrets, t)
	}
	res := Result{Briefs: map[string]string{}}

	// A session that changed nothing has little to record, and asking the agent costs
	// a request (and tokens) every time. Keep a plain summary and leave the briefs alone.
	anyChanged := false
	for _, o := range outcomes {
		anyChanged = anyChanged || o.changed
	}
	if agentErr == nil && !anyChanged {
		fmt.Fprintln(w.opt.Err, "memory: nothing changed, so the agent was not asked to update the briefs (a plain summary is kept)")
	}
	if agentErr == nil && anyChanged {
		timeout := w.opt.MemoryTimeout
		if timeout == 0 {
			timeout = 5 * time.Minute
		}
		mctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		summary, briefs, err := w.askAgentToRemember(mctx, ad, spec, ref)
		if err != nil {
			fmt.Fprintf(w.opt.Err, "memory: the agent could not update memory (%v); using a plain summary\n", err)
		}
		if s := memory.Clean(summary); s != "" {
			s, hid := memory.Redact(s, secrets)
			res.Summary, res.SummarySource = memory.Cap(s, memory.SummaryCap), "agent"
			if hid {
				fmt.Fprintln(w.opt.Err, "memory: removed something that looked like a secret from the summary")
			}
		}
		for dir, b := range briefs {
			b = memory.Clean(b)
			if b == "" {
				continue
			}
			b, hid := memory.Redact(b, secrets)
			if hid {
				fmt.Fprintf(w.opt.Err, "memory: removed something that looked like a secret from the %s brief\n", dir)
			}
			res.Briefs[dir] = memory.Cap(b, memory.BriefCap)
		}
	}

	if res.Summary == "" {
		fb := memory.Fallback{Task: spec.Task, Prompt: spec.Prompt, Agent: spec.Agent, Status: "done", OutputTail: string(agentTail)}
		if agentErr != nil {
			fb.Status, fb.Error = "failed", agentErr.Error()
		}
		for _, repo := range spec.Repos {
			o := outcomes[repo.Name]
			fr := memory.FallbackRepo{Dir: repo.Name, Branch: spec.Branch(), Changed: o.changed, Pushed: o.pushed}
			if o.changed {
				repo := repo
				dir := workspace + "/" + repo.Name
				fr.DiffStat, _ = w.git(ctx, dir, &repo, "diff", "--stat", startHeads[repo.Name]+"..HEAD")
			}
			fb.Repos = append(fb.Repos, fr)
		}
		s, _ := memory.Redact(memory.FallbackSummary(fb), secrets)
		res.Summary, res.SummarySource = memory.Cap(s, memory.SummaryCap), "fallback"
	}
	fmt.Fprintf(w.opt.Err, "memory: session summary saved (%s)\n", res.SummarySource)
	return res
}

func (w *worker) askAgentToRemember(ctx context.Context, ad agent.Adapter, spec session.Spec, ref string) (string, map[string]string, error) {
	if err := w.sb.Exec(ctx, runner.ExecOpts{Argv: []string{"mkdir", "-p", memory.OutDir}}); err != nil {
		return "", nil, fmt.Errorf("cannot create %s: %v", memory.OutDir, err)
	}
	prompt := memory.UpdatePrompt(w.withDocs(ctx, w.opt.Mem.Repos), spec.Branch())
	argv, resumed := ad.FollowUp(prompt, spec.Mode, ref)
	if !resumed {
		// This agent cannot resume its conversation, so a fresh one is told what happened.
		fresh := prompt + "\n\nThe task you were given was:\n<task>\n" + spec.Prompt + "\n</task>\n" +
			"You are in a new conversation: use git log and git diff in each repo (branch " + spec.Branch() + ") to see what was done."
		var err error
		if argv, err = ad.Command(fresh, spec.Mode, ""); err != nil {
			return "", nil, err
		}
	}
	fmt.Fprintln(w.opt.Err, "memory: asking the agent to update project memory")
	var errb bytes.Buffer
	if err := w.sb.Exec(ctx, runner.ExecOpts{Argv: argv, Dir: workspace, Stdout: io.Discard, Stderr: &errb}); err != nil {
		return "", nil, fmt.Errorf("%v", err)
	}
	summary, err := w.readFile(ctx, memory.OutDir+"/"+memory.SummaryFile, 4*memory.SummaryCap)
	if err != nil {
		summary = ""
	}
	briefs := map[string]string{}
	for _, repo := range spec.Repos {
		if b, err := w.readFile(ctx, memory.OutDir+"/"+memory.BriefFile(repo.Name), 4*memory.BriefCap); err == nil {
			briefs[repo.Name] = b
		}
	}
	if summary == "" {
		return "", briefs, fmt.Errorf("it wrote no %s", memory.SummaryFile)
	}
	return summary, briefs, nil
}

// requestedTool is the text of the agent's last request for a tool that only
// needs reading when the run ends (the last request wins), or "" when the session
// was not given the tool or it was not used.
func (w *worker) requestedTool(ctx context.Context, name string) string {
	given := false
	for _, t := range w.opt.Tools {
		given = given || t.Name == name
	}
	if !given || ctx.Err() != nil {
		return ""
	}
	content, err := w.readFile(ctx, tools.Dir+"/"+name, 4096)
	if err != nil {
		return "" // the agent did not use the tool
	}
	reqs := tools.Requests(content)
	if len(reqs) == 0 {
		return ""
	}
	return reqs[len(reqs)-1]
}

// cloned reports whether a repo was already cloned into dir by an earlier turn.
func (w *worker) cloned(ctx context.Context, dir string) bool {
	var out bytes.Buffer
	err := w.sb.Exec(ctx, runner.ExecOpts{Argv: []string{"ls", "-d", dir + "/.git"}, Stdout: &out})
	return err == nil && strings.TrimSpace(out.String()) != ""
}

// requestedBody is the pull request description the agent wrote with the pr-body
// tool, or "" when the session was not given the tool or it was not used.
func (w *worker) requestedBody(ctx context.Context) string {
	if !w.hasTool(tools.PRBody.Name) || ctx.Err() != nil {
		return ""
	}
	content, err := w.readFile(ctx, tools.Dir+"/"+tools.PRBody.Name, 8000)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(content)
}

// withDocs says, for each repo, which of its own documentation files exist, so the
// agent leaves out of memory what they already say.
func (w *worker) withDocs(ctx context.Context, repos []memory.RepoBrief) []memory.RepoBrief {
	out := make([]memory.RepoBrief, len(repos))
	for i, r := range repos {
		out[i] = r
		var names bytes.Buffer
		script := `cd "$1" 2>/dev/null && ls -1d README* readme* CLAUDE.md AGENTS.md GEMINI.md CONTRIBUTING* ARCHITECTURE* docs 2>/dev/null`
		if err := w.sb.Exec(ctx, runner.ExecOpts{Argv: []string{"sh", "-c", script, "sh", workspace + "/" + r.Dir}, Stdout: &names}); err == nil || names.Len() > 0 {
			out[i].Docs = strings.Fields(names.String())
		}
	}
	return out
}

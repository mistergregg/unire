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
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/memory"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/safe"
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
	// AddRepo resolves the agent's request for another repo (the repo tool) to one the
	// session may use, asking the owner when that is needed. It returns the repo, or nil
	// and what to tell the agent.
	AddRepo func(ctx context.Context, query string) (*AddedRepo, string)
	// Resume is the agent's own conversation to carry on (a reply by the same agent), so it
	// remembers everything said and done before; Fallback is the prompt to use instead if
	// that conversation is gone (it tells a fresh agent what happened in the chat).
	Resume   string
	Fallback string
	// Control carries what the owner says to the agent while it works. nil means
	// the agent is run once and cannot be steered. OnMessage reports what became of
	// each message (delivered, failed).
	Control   <-chan agent.Steer
	OnMessage func(id, state string)
	OnUsage   func(agent.AgentUsage)
	// Files are copied into the sandbox before the agent starts: images the owner
	// attached to the prompt, which names where they are.
	Files []agent.File
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
	Shared        []Shared          // files the agent put in the chat with the share tool
	Conversation  string            // the agent's own conversation id, so the next reply can carry it on
}

const workspace = "/workspace"

// The identity commits in the sandbox are made with, when the owner set none.
const (
	commitName  = "unire-agent"
	commitEmail = "agent@unire.local"
)

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

// textCapture collects up to max bytes written to it.
type textCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (c *textCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.max <= 0 || c.buf.Len() < c.max {
		toWrite := p
		if c.max > 0 && c.buf.Len()+len(p) > c.max {
			toWrite = p[:c.max-c.buf.Len()]
		}
		c.buf.Write(toWrite)
	}
	return len(p), nil
}

func (c *textCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// Run executes the session. Work the agent produced is pushed even when the
// agent exits with an error, and the error is returned afterwards.
func Run(ctx context.Context, r runner.Runner, spec session.Spec, opt Options) (Result, error) {
	var res Result
	adapter, err := agent.Get(spec.Agent)
	if err != nil {
		return res, err
	}
	if spec.Model != "" {
		adapter = adapter.WithModel(spec.Model) // used by the one-shot commands and the live start
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

	w := &worker{sb: sb, opt: opt, spec: spec}

	// Mounted local repos are owned by a different user than the sandbox
	// user. A global setting is needed because git ignores per-command
	// settings for the process that reads the source repo. The home folder is
	// kept between sessions, so the setting replaces itself rather than being
	// added again each time.
	if _, err := w.git(ctx, "", nil, "config", "--global", "--replace-all", "safe.directory", "*"); err != nil {
		return res, fmt.Errorf("git setup: %w", err)
	}
	// Agents commit their own work, and git refuses to commit without a name and
	// an address (an agent was seen failing its first commit, then inventing one).
	// They get the identity Unire commits leftovers with, unless the owner set one.
	for _, kv := range [][2]string{{"user.name", commitName}, {"user.email", commitEmail}} {
		if _, err := w.git(ctx, "", nil, "config", "--global", kv[0]); err != nil {
			if _, err := w.git(ctx, "", nil, "config", "--global", kv[0], kv[1]); err != nil {
				return res, fmt.Errorf("git setup: %w", err)
			}
		}
	}

	// A conversation keeps its workspace between turns, so a reply finds the
	// repos where the last turn left them. Requests the agent left in files
	// last time must not be read again.
	_ = w.sb.Exec(ctx, runner.ExecOpts{Argv: []string{"rm", "-rf", tools.Dir, memory.OutDir}})
	if err := w.copyFiles(ctx, opt.Files); err != nil {
		return res, err
	}

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
	// What Unire adds for a session: its tools, and where the code is.
	if tp := tools.Prompt(opt.Tools); tp != "" && prompt != "" {
		prompt += "\n\n---\n\n" + tp + "\n" + workspaceNote(spec)
	}
	// The prompt for a fresh conversation, if the saved one cannot be carried on, gets the
	// same memory, tools and note on where the code is.
	if fb := opt.Fallback; fb != "" {
		if opt.Mem != nil && opt.Mem.Context != "" && adapter.MemoryMode() == agent.MemoryPrompt {
			fb = opt.Mem.Context + "\n\n---\n\n# Your task\n\n" + fb
		}
		if tp := tools.Prompt(opt.Tools); tp != "" {
			fb += "\n\n---\n\n" + tp + "\n" + workspaceNote(spec)
		}
		w.opt.Fallback = fb
	}
	argv, err := adapter.Command(prompt, spec.Mode, ref)
	if err != nil {
		return res, err
	}

	fmt.Fprintf(opt.Err, "running %s\n", adapter.Name())
	out := &tail{}
	capture := &textCapture{max: 1024 * 1024}
	agentOpts := runner.ExecOpts{
		Argv: argv, Dir: workspace, TTY: opt.Interactive,
		Stdout: io.MultiWriter(opt.Out, out, capture), Stderr: opt.Err,
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
			stream = &streamWriter{
				s: s, out: io.MultiWriter(opt.Out, out, capture), log: opt.Err,
				onUsage: func(u *agent.AgentUsage) {
					if opt.OnUsage != nil && u != nil {
						opt.OnUsage(*u)
					}
				},
			}
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
		defer safe.Recover("serve questions", nil)
		if w.asking() {
			w.serveQuestions(askCtx)
		}
	}()
	var agentErr error
	if useLive {
		agentErr = w.runLive(ctx, live, spec, prompt, ref, io.MultiWriter(out, capture))
		res.Conversation = w.conv
		if w.conv != "" {
			ref = w.conv // the memory step carries on the conversation the agent actually worked in
		}
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
	shared := w.requestedShares(ctx, capture.String())

	w.mu.Lock()
	spec.Repos = append(spec.Repos, w.added...)
	for dir, head := range w.addedHeads {
		startHeads[dir] = head
	}
	w.mu.Unlock()
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
		opt.OnWorkDone(Result{Name: requestedName, PRTitle: prTitle, PRBody: prBody, Shared: shared, Conversation: w.conv}, workErr)
	}
	if opt.Mem != nil && ctx.Err() == nil {
		res = w.rememberSession(ctx, adapter, spec, ref, startHeads, outcomes, agentErr, out.buf)
	}
	res.Name = requestedName
	res.PRTitle = prTitle
	res.PRBody = prBody
	res.Shared = shared
	res.Conversation = w.conv
	return res, workErr
}

type worker struct {
	spec        session.Spec
	conv        string            // the agent's conversation this run worked in (to carry it on next time)
	mu          sync.Mutex        // guards what the repo tool adds while the agent runs
	added       []session.Repo    // repos brought in while the agent ran
	addedTokens map[string]string // their tokens, by directory
	addedHeads  map[string]string // where each started
	sb          runner.Sandbox
	opt         Options
}

// git runs git in the sandbox. For a repo with a token, the token is passed
// for that repo's host only.
func (w *worker) git(ctx context.Context, dir string, repo *session.Repo, args ...string) (string, error) {
	argv := []string{"git"}
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	if repo != nil {
		if tok := w.tokenFor(repo.Name); tok != "" {
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

// workspaceNote tells the agent where its repos are. It starts in /workspace, which
// is not a repo itself; without this, agents were seen running git there, or
// reading files at the wrong path, before they found the code.
func workspaceNote(spec session.Spec) string {
	if len(spec.Repos) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Where the code is\n\n")
	b.WriteString("You start in " + workspace + ", which is not a git repo. The repos for this chat are cloned inside it, each on the branch " + spec.Branch() + ":\n\n")
	for _, r := range spec.Repos {
		b.WriteString("- " + workspace + "/" + r.Name)
		if r.Base != "" && r.Base != spec.Branch() {
			b.WriteString(" (started from " + r.Base + ")")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nRun git, build and test commands inside a repo's folder, and commit your work there: Unire pushes it when you finish.\n")
	return b.String()
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

// copyFiles copies files from the host into the sandbox (attachments: see
// sessions/attach.go). Each is fed on stdin and its path passed as an argument, so
// nothing is parsed by a shell.
func (w *worker) copyFiles(ctx context.Context, files []agent.File) error {
	for _, f := range files {
		if err := w.copyFile(ctx, f); err != nil {
			return fmt.Errorf("could not give the agent %s: %w", path.Base(f.To), err)
		}
	}
	return nil
}

func (w *worker) copyFile(ctx context.Context, f agent.File) error {
	in, err := os.Open(f.From)
	if err != nil {
		return err
	}
	defer in.Close()
	var errb bytes.Buffer
	err = w.sb.Exec(ctx, runner.ExecOpts{
		Argv:  []string{"sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", f.To},
		Stdin: in, Stderr: &errb,
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
			if _, err := w.git(ctx, dir, &repo, "-c", "user.name="+commitName, "-c", "user.email="+commitEmail, "commit", "-m", msg); err != nil {
				return outcomes, err
			}
		}
		// The agent may have put the same change on other branches (for example one
		// for each release branch). They are named after the session branch with a
		// suffix, and each started from the branch it is meant for.
		extras := w.pushExtraBranches(ctx, dir, &repo, spec)
		head, err := w.git(ctx, dir, &repo, "rev-parse", "HEAD")
		if err != nil {
			return outcomes, err
		}
		// Nothing new this turn. But an earlier push may have failed (a token that cannot
		// write, a network error) and left commits that were never pushed: push those.
		if strings.TrimSpace(head) == startHeads[repo.Name] && !w.hasUnpushedWork(ctx, dir, &repo, spec) {
			if extras > 0 {
				outcomes[repo.Name] = outcome{changed: true, pushed: true}
			}
			continue
		}
		o := outcome{changed: true, pushed: extras > 0}
		if spec.NoPush {
			fmt.Fprintf(w.opt.Err, "%s: changes committed, not pushing (--no-push)\n", repo.Name)
			outcomes[repo.Name] = o
			continue
		}
		if err := w.pushBranch(ctx, dir, &repo, spec.Branch()); err != nil {
			return outcomes, fmt.Errorf("push %s: %w%s", repo.Name, err, pushHint(err))
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

// pushExtraBranches pushes the branches the agent made for other targets: local
// branches named <session branch>-<suffix>. The branch each started from (the one
// a pull request should go into) is remembered in the repo's config, because
// pushing replaces it. It returns how many were pushed.
func (w *worker) pushExtraBranches(ctx context.Context, dir string, repo *session.Repo, spec session.Spec) int {
	if spec.NoPush {
		return 0
	}
	out, err := w.git(ctx, dir, repo, "for-each-ref", "--format=%(refname:short)|%(upstream:short)", "refs/heads/")
	if err != nil {
		return 0
	}
	prefix := spec.Branch() + "-"
	pushed := 0
	for _, line := range strings.Split(out, "\n") {
		name, up, _ := strings.Cut(strings.TrimSpace(line), "|")
		if !strings.HasPrefix(name, prefix) || name == prefix {
			continue
		}
		base := ""
		if strings.HasPrefix(up, "origin/") && up != "origin/"+name {
			base = strings.TrimPrefix(up, "origin/")
		}
		if base != "" {
			_, _ = w.git(ctx, dir, repo, "config", "branch."+name+".unire-base", base)
		} else if b, err := w.git(ctx, dir, repo, "config", "--get", "branch."+name+".unire-base"); err == nil {
			base = strings.TrimSpace(b)
		}
		if err := w.pushBranch(ctx, dir, repo, name); err != nil {
			fmt.Fprintf(w.opt.Err, "%s: could not push %s: %v\n", repo.Name, name, err)
			continue
		}
		pushed++
		if base != "" {
			fmt.Fprintf(w.opt.Err, "%s: also pushed %s (into %s)\n", repo.Name, name, base)
		} else {
			fmt.Fprintf(w.opt.Err, "%s: also pushed %s\n", repo.Name, name)
		}
	}
	return pushed
}

// AddedRepo is a repo brought into a running session: the directory it is cloned
// into, where from, the token for its server, and the branch to start from (empty:
// the repo's default).
type AddedRepo struct {
	Dir, URL, Token, Base string
}

// tokenFor is the git token for a repo directory, including repos added while the
// agent ran.
func (w *worker) tokenFor(dir string) string {
	if tok := w.opt.GitTokens[dir]; tok != "" {
		return tok
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.addedTokens[dir]
}

// addRepo clones another repo into the workspace for the agent, on the session's
// branch, and arranges for it to be committed and pushed with the others.
func (w *worker) addRepo(ctx context.Context, query string) string {
	spec := w.spec
	r, msg := w.opt.AddRepo(ctx, query)
	if r == nil {
		return msg
	}
	repo := session.Repo{Name: r.Dir, URL: r.URL, Base: r.Base}
	w.mu.Lock()
	if w.addedTokens == nil {
		w.addedTokens = map[string]string{}
		w.addedHeads = map[string]string{}
	}
	w.addedTokens[r.Dir] = r.Token
	w.mu.Unlock()

	dir := workspace + "/" + r.Dir
	if w.cloned(ctx, dir) { // an earlier turn of the conversation already has it here
		if _, err := w.git(ctx, dir, &repo, "checkout", "-B", spec.Branch()); err != nil {
			return "Could not use " + r.Dir + ": " + err.Error()
		}
	} else {
		args := []string{"clone"}
		if r.Base != "" {
			args = append(args, "--branch", r.Base)
		}
		args = append(args, r.URL, dir)
		fmt.Fprintf(w.opt.Err, "cloning %s (the agent asked for it)\n", r.Dir)
		if _, err := w.git(ctx, "", &repo, args...); err != nil {
			return "Could not clone " + r.Dir + ": " + err.Error()
		}
		if r.Base != spec.Branch() {
			if _, err := w.git(ctx, dir, &repo, "checkout", "-b", spec.Branch()); err != nil {
				return "Could not branch " + r.Dir + ": " + err.Error()
			}
		}
	}
	head, err := w.git(ctx, dir, &repo, "rev-parse", "HEAD")
	if err != nil {
		return "Could not read " + r.Dir + ": " + err.Error()
	}
	w.mu.Lock()
	w.added = append(w.added, repo)
	w.addedHeads[r.Dir] = strings.TrimSpace(head)
	w.mu.Unlock()
	return fmt.Sprintf("Cloned into %s on branch %s. It is pushed with your other changes when you finish.", dir, spec.Branch())
}

// hasUnpushedWork reports whether the session branch holds commits that are not on the
// git server: it is checked out, it is ahead of the repo's base, and the server does not
// have it (or has an older copy). It never says yes for an untouched repo, so a chat that
// changed nothing does not create a branch.
func (w *worker) hasUnpushedWork(ctx context.Context, dir string, repo *session.Repo, spec session.Spec) bool {
	if spec.NoPush {
		return false
	}
	branch := spec.Branch()
	if cur, err := w.git(ctx, dir, repo, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || strings.TrimSpace(cur) != branch {
		return false
	}
	head, err := w.git(ctx, dir, repo, "rev-parse", "HEAD")
	if err != nil {
		return false
	}
	if remote, err := w.git(ctx, dir, repo, "rev-parse", "-q", "--verify", "refs/remotes/origin/"+branch); err == nil && strings.TrimSpace(remote) == strings.TrimSpace(head) {
		return false // the server has exactly this
	}
	base := "origin/HEAD"
	if repo.Base != "" {
		base = "origin/" + repo.Base
	}
	n, err := w.git(ctx, dir, repo, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return false
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(n))
	return err == nil && ahead > 0
}

// pushBranch pushes a branch, keeping it up to date first so the push is not
// refused and the pull request is not left behind:
//  1. Commits that appeared on the server's copy of the branch (the owner pressed
//     "Update branch", or something pushed from elsewhere) are brought in.
//  2. When the branch has fallen behind the branch it is meant for (usually main),
//     that is merged in, as "Update branch" would, if it merges cleanly.
//
// A push that is refused anyway (something moved in between) brings the newer
// commits in again and retries. Only a conflict that nothing can resolve
// automatically is reported, with the branch left clean.
func (w *worker) pushBranch(ctx context.Context, dir string, repo *session.Repo, name string) error {
	_, _ = w.git(ctx, dir, repo, "fetch", "origin") // best effort: the branch may not exist on the server yet
	if err := w.integrate(ctx, dir, repo, name, "origin/"+name, false); err != nil {
		return err
	}
	w.updateFromBase(ctx, dir, repo, name)
	_, err := w.git(ctx, dir, repo, "push", "-u", "origin", name)
	for try := 0; err != nil && rejectedPush(err) && try < 3; try++ {
		fmt.Fprintf(w.opt.Err, "%s: the server has newer commits on %s; bringing them in and pushing again\n", repo.Name, name)
		if _, ferr := w.git(ctx, dir, repo, "fetch", "origin", name); ferr != nil {
			return fmt.Errorf("%w (and fetching the newer commits failed: %v)", err, ferr)
		}
		if ierr := w.integrate(ctx, dir, repo, name, "origin/"+name, true); ierr != nil {
			return fmt.Errorf("%w (%v)", err, ierr)
		}
		_, err = w.git(ctx, dir, repo, "push", "-u", "origin", name)
	}
	return err
}

// integrate brings the commits of ref that the current branch lacks into it: a
// rebase (keeps the history straight), or a merge when the rebase conflicts. With
// force false it does nothing when there is nothing to bring in. A conflict that
// neither can resolve is aborted, leaving the branch as it was, and returned.
func (w *worker) integrate(ctx context.Context, dir string, repo *session.Repo, name, ref string, force bool) error {
	if !force && w.behindBy(ctx, dir, repo, ref) == 0 {
		return nil
	}
	id := []string{"-c", "user.name=" + commitName, "-c", "user.email=" + commitEmail}
	if _, err := w.git(ctx, dir, repo, append(id, "rebase", ref)...); err == nil {
		return nil
	}
	_, _ = w.git(ctx, dir, repo, "rebase", "--abort")
	if _, err := w.git(ctx, dir, repo, append(id, "merge", "--no-edit", ref)...); err != nil {
		_, _ = w.git(ctx, dir, repo, "merge", "--abort")
		return fmt.Errorf("the newer commits on %s conflict with this branch, so they could not be brought in automatically: %v", ref, err)
	}
	fmt.Fprintf(w.opt.Err, "%s: the rebase conflicted, so %s was merged in instead\n", repo.Name, ref)
	return nil
}

// behindBy is how many commits ref has that the current branch lacks (0 when ref
// does not exist or cannot be compared).
func (w *worker) behindBy(ctx context.Context, dir string, repo *session.Repo, ref string) int {
	out, err := w.git(ctx, dir, repo, "rev-list", "--count", "HEAD.."+ref)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// updateFromBase merges the branch this one is meant for (the repo's base, else
// its default branch) into it when it has moved on, so the pull request is not
// out of date. It never fails the push: a conflict is left for the owner and the
// agent, and the branch is left as it was.
func (w *worker) updateFromBase(ctx context.Context, dir string, repo *session.Repo, name string) {
	base := "origin/HEAD"
	if repo.Base != "" {
		base = "origin/" + repo.Base
	}
	if base == "origin/"+name || w.behindBy(ctx, dir, repo, base) == 0 {
		return
	}
	id := []string{"-c", "user.name=" + commitName, "-c", "user.email=" + commitEmail}
	if _, err := w.git(ctx, dir, repo, append(id, "merge", "--no-edit", base)...); err != nil {
		_, _ = w.git(ctx, dir, repo, "merge", "--abort")
		fmt.Fprintf(w.opt.Err, "%s: %s has moved on but does not merge cleanly into %s; left as it is\n", repo.Name, base, name)
		return
	}
	fmt.Fprintf(w.opt.Err, "%s: brought %s into %s so the pull request is not out of date\n", repo.Name, base, name)
}

// rejectedPush reports whether git refused a push because the remote branch has
// commits this clone lacks (as opposed to a permission or network problem).
func rejectedPush(err error) bool {
	m := err.Error()
	return strings.Contains(m, "fetch first") || strings.Contains(m, "non-fast-forward") ||
		(strings.Contains(m, "[rejected]") && strings.Contains(m, "remote contains work"))
}

// pushHint says what is probably wrong when a push is refused for lack of permission.
func pushHint(err error) string {
	m := err.Error()
	if strings.Contains(m, "403") || strings.Contains(m, "Permission to") || strings.Contains(m, "denied") || strings.Contains(m, "401") {
		return ". The token saved for this git server probably cannot write to this repo: on the Git servers page, replace it with one that can write (for GitHub: Contents set to read and write for this repo), and use Check pull requests to see what it may do"
	}
	return ""
}

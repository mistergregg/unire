package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/agent/agenttest"
	"github.com/mistergregg/unire/internal/memory"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/session"
	"github.com/mistergregg/unire/internal/tools"
)

// fakeSandbox plays a sandbox: git answers, files can be written and read,
// and "claude" behaves as the test configures.
type fakeSandbox struct {
	mu         sync.Mutex // the workflow now talks to the sandbox from more than one goroutine
	calls      []runner.ExecOpts
	heads      int
	noChange   bool   // the agent changes nothing: HEAD stays where it was
	refs       string // what `git for-each-ref` prints: local branches and their upstreams
	branchName string // what `git rev-parse --abbrev-ref HEAD` prints
	noIdentity bool   // git has no user.name or user.email configured
	unpushed   bool   // the branch holds commits the server does not have
	pushErr    string // if set, a push fails with this message

	files     map[string]string // files the workflow wrote
	outFiles  map[string]string // files the follow-up agent "writes"
	agentOut  string
	mainErr   error // returned by the main agent run
	followErr error // returned by the follow-up agent run
	block     bool  // the follow-up waits for its context

	agentHook func(ctx context.Context) // runs during the main agent run (it may take a while)
	existing  map[string]bool           // paths `ls -d` finds: repos an earlier turn already cloned

	// A steerable agent runs behind `sh -c`; live plays it (nil: a Claude that
	// answers once). onSignal hears the signals sent to it.
	live     func(ctx context.Context, inner []string, o runner.ExecOpts) error
	onSignal func(sig string)
}

func (f *fakeSandbox) Exec(ctx context.Context, o runner.ExecOpts) error {
	f.mu.Lock()
	f.calls = append(f.calls, o)
	if f.files == nil {
		f.files = map[string]string{}
	}
	if inner, isAgent, isSignal := agenttest.Unwrap(o.Argv); isSignal {
		cb := f.onSignal
		f.mu.Unlock()
		if cb != nil {
			sig := "TERM"
			switch {
			case strings.Contains(o.Argv[2], "kill -INT"):
				sig = "INT"
			case strings.Contains(o.Argv[2], "kill -KILL"):
				sig = "KILL"
			}
			cb(sig)
		}
		return nil
	} else if isAgent {
		live, out := f.live, f.agentOut
		f.mu.Unlock()
		var err error
		if live != nil {
			err = live(ctx, inner, o)
		} else {
			err = agenttest.Claude(ctx, o.Stdin, o.Stdout, func(agenttest.User) string { return out }, nil, nil)
		}
		fmt.Fprintf(o.Stdout, "\n%s0\n", agent.ExitMarker) // what the real wrapper says when the agent ends
		// Like docker exec, finish only once the copy of the process's input has
		// ended, which is when whoever holds the other end closes it. The real thing
		// froze here when the input was left open after the agent was killed.
		if o.Stdin != nil {
			io.Copy(io.Discard, o.Stdin)
		}
		return err
	}
	if len(o.Argv) > 0 && o.Argv[0] == "claude" && !contains(o.Argv, "--resume") {
		hook, out, err := f.agentHook, f.agentOut, f.mainErr
		f.mu.Unlock()
		if hook != nil {
			hook(ctx)
		}
		io.WriteString(o.Stdout, out)
		return err
	}
	defer f.mu.Unlock()
	if len(o.Argv) == 0 {
		return nil
	}
	switch o.Argv[0] {
	case "ls":
		if contains(o.Argv, "-d") {
			p := o.Argv[len(o.Argv)-1]
			if f.existing[p] {
				io.WriteString(o.Stdout, p+"\n")
				return nil
			}
			return errors.New("exit status 2")
		}
		found := false
		for p := range f.outFiles {
			if dir, name := path.Split(p); strings.TrimSuffix(dir, "/") == o.Argv[len(o.Argv)-1] {
				io.WriteString(o.Stdout, name+"\n")
				found = true
			}
		}
		if !found {
			return errors.New("exit status 2")
		}
	case "git":
		switch {
		case f.noIdentity && len(o.Argv) == 4 && o.Argv[1] == "config" && strings.HasPrefix(o.Argv[3], "user."):
			return errors.New("exit status 1") // asked for a value that is not set
		case contains(o.Argv, "--abbrev-ref"):
			io.WriteString(o.Stdout, f.branchName+"\n")
			return nil
		case contains(o.Argv, "--verify"):
			return nil // the server has no copy of the branch
		case contains(o.Argv, "rev-list"):
			if f.unpushed {
				io.WriteString(o.Stdout, "3\n")
			} else {
				io.WriteString(o.Stdout, "0\n")
			}
			return nil
		case contains(o.Argv, "push") && f.pushErr != "":
			return errors.New(f.pushErr)
		}
		if contains(o.Argv, "rev-parse") {
			f.heads++
			// First call per repo is the start HEAD; later calls differ, so the
			// workflow sees the agent's commit.
			if f.heads%2 == 1 || f.noChange {
				io.WriteString(o.Stdout, "aaa\n")
			} else {
				io.WriteString(o.Stdout, "bbb\n")
			}
		}
		if contains(o.Argv, "for-each-ref") {
			io.WriteString(o.Stdout, f.refs)
		}
		if contains(o.Argv, "--stat") {
			io.WriteString(o.Stdout, " file.go | 3 +++\n")
		}
	case "sh":
		if o.Stdin != nil {
			b, _ := io.ReadAll(o.Stdin)
			f.files[o.Argv[len(o.Argv)-1]] = string(b)
		}
	case "head":
		path := o.Argv[len(o.Argv)-1]
		if c, ok := f.outFiles[path]; ok {
			io.WriteString(o.Stdout, c)
			return nil
		}
		return errors.New("exit status 1")
	case "claude":
		if contains(o.Argv, "--resume") {
			if f.block {
				<-ctx.Done()
				return ctx.Err()
			}
			return f.followErr
		}
		io.WriteString(o.Stdout, f.agentOut)
		return f.mainErr
	}
	return nil
}
func (f *fakeSandbox) Stop(context.Context) error { return nil }

type fakeRunner struct{ sb *fakeSandbox }

func (r fakeRunner) Start(context.Context, runner.StartSpec) (runner.Sandbox, error) {
	return r.sb, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func opts(tokens map[string]string) Options {
	return Options{Out: io.Discard, Err: io.Discard, GitTokens: tokens}
}

func baseSpec() session.Spec {
	return session.Spec{
		ID: "t1", Task: "demo", Agent: "claude", Prompt: "go", Mode: "hands-off",
		Repos: []session.Repo{{Name: "api", URL: "https://git.example.com/me/api.git"}},
	}
}

func run(t *testing.T, sb *fakeSandbox, spec session.Spec, o Options) (Result, error) {
	t.Helper()
	return Run(context.Background(), fakeRunner{sb}, spec, o)
}

func TestTokenNeverReachesAgentAndPushHappens(t *testing.T) {
	sb := &fakeSandbox{}
	if _, err := run(t, sb, baseSpec(), opts(map[string]string{"api": "tok123"})); err != nil {
		t.Fatal(err)
	}
	pushed := false
	for _, c := range sb.calls {
		isAgent := len(c.Argv) > 0 && c.Argv[0] == "claude"
		if isAgent && (len(c.Env) != 0) {
			t.Errorf("agent exec got env %v; the token must not reach the agent", c.Env)
		}
		if contains(c.Argv, "push") {
			pushed = true
			if c.Env["UNIRE_GIT_TOKEN"] != "tok123" {
				t.Error("push should receive the token")
			}
		}
		for _, a := range c.Argv {
			if strings.Contains(a, "tok123") {
				t.Errorf("token appears in argv: %v", c.Argv)
			}
		}
	}
	if !pushed {
		t.Error("expected a push because HEAD moved")
	}
}

func TestTokenIsScopedToItsOwnHost(t *testing.T) {
	sb := &fakeSandbox{}
	spec := baseSpec()
	spec.Repos = []session.Repo{
		{Name: "api", URL: "https://git.example.com/me/api.git"},
		{Name: "lib", URL: "https://github.com/me/lib.git"},
	}
	if _, err := run(t, sb, spec, opts(map[string]string{"api": "tok-a", "lib": "tok-b"})); err != nil {
		t.Fatal(err)
	}
	for _, c := range sb.calls {
		if !contains(c.Argv, "clone") {
			continue
		}
		joined := strings.Join(c.Argv, " ")
		switch {
		case strings.Contains(joined, "api.git"):
			if !strings.Contains(joined, "credential.https://git.example.com.helper") || c.Env["UNIRE_GIT_TOKEN"] != "tok-a" {
				t.Errorf("api clone should use its own host's token only: %v env=%v", c.Argv, c.Env)
			}
		case strings.Contains(joined, "lib.git"):
			if !strings.Contains(joined, "credential.https://github.com.helper") || c.Env["UNIRE_GIT_TOKEN"] != "tok-b" {
				t.Errorf("lib clone should use its own host's token only: %v env=%v", c.Argv, c.Env)
			}
		}
	}
}

func TestNoTokenForLocalOrSSHUrls(t *testing.T) {
	sb := &fakeSandbox{}
	spec := baseSpec()
	spec.Repos = []session.Repo{{Name: "api", URL: "/remotes/api"}}
	_, _ = run(t, sb, spec, opts(map[string]string{"api": "tok"}))
	for _, c := range sb.calls {
		if _, ok := c.Env["UNIRE_GIT_TOKEN"]; ok {
			t.Errorf("a token was sent for a non-http URL: %v", c.Argv)
		}
	}
}

func TestNoPushSkipsPush(t *testing.T) {
	sb := &fakeSandbox{}
	spec := baseSpec()
	spec.NoPush = true
	if _, err := run(t, sb, spec, opts(nil)); err != nil {
		t.Fatal(err)
	}
	for _, c := range sb.calls {
		if contains(c.Argv, "push") {
			t.Error("--no-push must not push")
		}
	}
}

// ---- memory ----

func memOpts(tokens map[string]string) Options {
	o := opts(tokens)
	o.Mem = &MemoryPlan{
		Context: "# Unire project memory\n\n## Repo api (me/api)\n\nOld brief.\n",
		Repos:   []memory.RepoBrief{{Dir: "api", FullName: "me/api", Brief: "Old brief."}},
	}
	return o
}

func agentWrites(summary, brief string) map[string]string {
	m := map[string]string{}
	if summary != "" {
		m[memory.OutDir+"/"+memory.SummaryFile] = summary
	}
	if brief != "" {
		m[memory.OutDir+"/api.brief.md"] = brief
	}
	return m
}

func indexOf(calls []runner.ExecOpts, pred func(runner.ExecOpts) bool) int {
	for i, c := range calls {
		if pred(c) {
			return i
		}
	}
	return -1
}

func TestMemoryIsGivenBeforeTheAgentRunsAndNeverCommitted(t *testing.T) {
	sb := &fakeSandbox{outFiles: agentWrites("## Asked\nx\n## Done\ny", "## What it is\nAPI v2")}
	res, err := run(t, sb, baseSpec(), memOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	// The context file is in /workspace (above the repos), under the agent's native name.
	if got := sb.files["/workspace/CLAUDE.md"]; !strings.Contains(got, "Old brief.") {
		t.Errorf("context file = %q", got)
	}
	write := indexOf(sb.calls, func(c runner.ExecOpts) bool { return len(c.Argv) > 0 && c.Argv[0] == "sh" })
	main := indexOf(sb.calls, func(c runner.ExecOpts) bool {
		return len(c.Argv) > 0 && c.Argv[0] == "claude" && contains(c.Argv, "--session-id")
	})
	if write < 0 || main < 0 || write > main {
		t.Errorf("memory must be written before the agent starts (write=%d main=%d)", write, main)
	}
	// The path and content never go through a shell string.
	for _, c := range sb.calls {
		if len(c.Argv) > 0 && c.Argv[0] == "sh" && strings.Contains(strings.Join(c.Argv, " "), "Old brief") {
			t.Errorf("content must be passed on stdin, not in argv: %v", c.Argv)
		}
	}
	if res.SummarySource != "agent" || !strings.Contains(res.Summary, "## Done") || res.Briefs["api"] != "## What it is\nAPI v2" {
		t.Errorf("result = %+v", res)
	}
}

func TestFollowUpResumesTheSameConversation(t *testing.T) {
	sb := &fakeSandbox{outFiles: agentWrites("## Asked\nx", "")}
	if _, err := run(t, sb, baseSpec(), memOpts(nil)); err != nil {
		t.Fatal(err)
	}
	var mainRef, resumeRef string
	for _, c := range sb.calls {
		for i, a := range c.Argv {
			if a == "--session-id" {
				mainRef = c.Argv[i+1]
			}
			if a == "--resume" {
				resumeRef = c.Argv[i+1]
			}
		}
	}
	if mainRef == "" || mainRef != resumeRef {
		t.Errorf("follow-up must resume the main run: session-id=%q resume=%q", mainRef, resumeRef)
	}
	// The prompt carries the previous brief as data.
	var prompt string
	for _, c := range sb.calls {
		if contains(c.Argv, "--resume") {
			prompt = c.Argv[len(c.Argv)-1]
		}
	}
	if !strings.Contains(prompt, "<previous-brief") || !strings.Contains(prompt, "Old brief.") {
		t.Errorf("update prompt = %q", prompt)
	}
}

func TestMemoryOffMeansNoMemoryWork(t *testing.T) {
	sb := &fakeSandbox{outFiles: agentWrites("## Asked\nx", "b")}
	res, err := run(t, sb, baseSpec(), opts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "" || len(res.Briefs) != 0 {
		t.Errorf("memory is off but got %+v", res)
	}
	if len(sb.files) != 0 {
		t.Errorf("nothing should be written when memory is off: %v", sb.files)
	}
	for _, c := range sb.calls {
		if contains(c.Argv, "--resume") {
			t.Error("no follow-up when memory is off")
		}
	}
}

func TestSecretsAreRemovedFromSavedMemory(t *testing.T) {
	sb := &fakeSandbox{outFiles: agentWrites(
		"## Done\nused token tok-secret-123 and ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"## Commands\npassword: hunter2hunter2")}
	res, err := run(t, sb, baseSpec(), memOpts(map[string]string{"api": "tok-secret-123"}))
	if err != nil {
		t.Fatal(err)
	}
	all := res.Summary + res.Briefs["api"]
	for _, leaked := range []string{"tok-secret-123", "ghp_abc", "hunter2"} {
		if strings.Contains(all, leaked) {
			t.Errorf("%q leaked into memory: %s", leaked, all)
		}
	}
}

func TestOversizeMemoryIsCapped(t *testing.T) {
	sb := &fakeSandbox{outFiles: agentWrites(strings.Repeat("summary line\n", 2000), strings.Repeat("brief line\n", 2000))}
	res, _ := run(t, sb, baseSpec(), memOpts(nil))
	if len(res.Summary) > memory.SummaryCap || len(res.Briefs["api"]) > memory.BriefCap {
		t.Errorf("summary %d (cap %d), brief %d (cap %d)", len(res.Summary), memory.SummaryCap, len(res.Briefs["api"]), memory.BriefCap)
	}
}

func TestFallbackWhenTheAgentWritesNothing(t *testing.T) {
	sb := &fakeSandbox{agentOut: "all done here"}
	res, err := run(t, sb, baseSpec(), memOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.SummarySource != "fallback" || !strings.Contains(res.Summary, "Written by Unire") || !strings.Contains(res.Summary, "api: pushed") ||
		!strings.Contains(res.Summary, "file.go") || !strings.Contains(res.Summary, "all done here") {
		t.Errorf("fallback = %+v", res)
	}
	if len(res.Briefs) != 0 {
		t.Error("a fallback must not replace any brief")
	}
}

func TestFallbackWhenTheMainAgentFails(t *testing.T) {
	sb := &fakeSandbox{mainErr: errors.New("exit status 1"), outFiles: agentWrites("## Asked\nshould not be used", "x")}
	res, err := run(t, sb, baseSpec(), memOpts(nil))
	if err == nil {
		t.Fatal("the agent's failure should still be returned")
	}
	if res.SummarySource != "fallback" || !strings.Contains(res.Summary, "Session status: failed") {
		t.Errorf("result = %+v", res)
	}
	for _, c := range sb.calls {
		if contains(c.Argv, "--resume") {
			t.Error("do not ask a failed agent to write memory")
		}
	}
}

func TestFailedAgentRunPointsAtTheAgentsPage(t *testing.T) {
	var log strings.Builder
	o := opts(nil)
	o.Err = &log
	sb := &fakeSandbox{mainErr: errors.New("exit status 41")}
	if _, err := run(t, sb, baseSpec(), o); err == nil {
		t.Fatal("the failure should be returned")
	}
	if !strings.Contains(log.String(), "hint: if this looks like a sign-in problem, open the Agents page and sign Claude Code in again") {
		t.Errorf("a failed run should say where to sign in:\n%s", log.String())
	}
	log.Reset()
	if _, err := run(t, &fakeSandbox{}, baseSpec(), o); err != nil || strings.Contains(log.String(), "hint:") {
		t.Errorf("a successful run must not show the hint: %v\n%s", err, log.String())
	}
}

func TestFallbackWhenTheFollowUpFailsOrTimesOut(t *testing.T) {
	sb := &fakeSandbox{followErr: errors.New("exit status 1"), outFiles: agentWrites("## Asked\nleftover", "")}
	if res, _ := run(t, sb, baseSpec(), memOpts(nil)); res.SummarySource != "fallback" {
		t.Errorf("failed follow-up: %+v", res)
	}

	sb = &fakeSandbox{block: true}
	o := memOpts(nil)
	o.MemoryTimeout = 50 * time.Millisecond
	start := time.Now()
	res, err := run(t, sb, baseSpec(), o)
	if err != nil || res.SummarySource != "fallback" || time.Since(start) > 3*time.Second {
		t.Errorf("timed-out follow-up: %+v err=%v after %v", res, err, time.Since(start))
	}
}

func TestAgentWithoutResumeGetsAFreshConversationWithTheTask(t *testing.T) {
	// An agent that cannot resume is simulated by checking the fallback path of
	// askAgentToRemember directly through a spec whose agent lacks FollowUp.
	ad := noResume{}
	w := &worker{sb: &fakeSandbox{outFiles: agentWrites("## Asked\nx", "")}, opt: memOpts(nil)}
	spec := baseSpec()
	spec.Prompt = "Build the thing"
	if _, _, err := w.askAgentToRemember(context.Background(), ad, spec, "ref"); err != nil {
		t.Fatal(err)
	}
	var prompt string
	for _, c := range w.sb.(*fakeSandbox).calls {
		if len(c.Argv) > 0 && c.Argv[0] == "fresh-agent" {
			prompt = c.Argv[len(c.Argv)-1]
		}
	}
	if !strings.Contains(prompt, "Build the thing") || !strings.Contains(prompt, "new conversation") {
		t.Errorf("a fresh conversation must be told the task; prompt = %q", prompt)
	}
}

type noResume struct{}

func (noResume) Name() string                 { return "fresh" }
func (noResume) Info() agent.Info             { return agent.Info{ID: "fresh", Label: "Fresh"} }
func (noResume) MemoryFile() string           { return "FRESH.md" }
func (noResume) MemoryMode() agent.MemoryMode { return agent.MemoryFile }
func (noResume) Hosts() []string              { return []string{"x"} }
func (noResume) Command(prompt, mode, ref string) ([]string, error) {
	return []string{"fresh-agent", prompt}, nil
}
func (noResume) FollowUp(prompt, mode, ref string) ([]string, bool) { return nil, false }
func (n noResume) WithModel(string) agent.Adapter                   { return n }

func TestAReplyContinuesTheBranchWithoutCreatingIt(t *testing.T) {
	branches := func(base string) int {
		sb := &fakeSandbox{}
		spec := baseSpec()
		spec.Repos[0].Base = base
		if _, err := run(t, sb, spec, opts(nil)); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, c := range sb.calls {
			if contains(c.Argv, "checkout") {
				n++
			}
		}
		return n
	}
	if got := branches(""); got != 1 {
		t.Errorf("a first turn creates its branch: %d checkouts", got)
	}
	if got := branches("agent/claude/demo"); got != 0 {
		t.Errorf("a reply is already on its branch after cloning it: %d checkouts", got)
	}
}

func TestAgentCanNameTheConversationOnlyWhenGivenTheTool(t *testing.T) {
	nameFile := tools.Dir + "/name"
	withTool := func(given []tools.Tool, content string) (string, string) {
		sb := &fakeSandbox{outFiles: map[string]string{nameFile: content}}
		o := opts(nil)
		o.Tools = given
		res, err := run(t, sb, baseSpec(), o)
		if err != nil {
			t.Fatal(err)
		}
		prompt := ""
		for _, c := range sb.calls {
			if len(c.Argv) > 0 && c.Argv[0] == "claude" {
				prompt = c.Argv[len(c.Argv)-1]
			}
		}
		return res.Name, prompt
	}
	name, prompt := withTool(tools.ForNewConversation(), "first idea\n \"Fix the login redirect\" \n")
	if name != "Fix the login redirect" {
		t.Errorf("name = %q, want the last request, cleaned", name)
	}
	if !strings.Contains(prompt, `unire name "<title>"`) {
		t.Errorf("the agent was not told about the tool: %q", prompt)
	}
	if name, prompt := withTool(nil, "sneaky"); name != "" || strings.Contains(prompt, "unire name") {
		t.Errorf("a session without the tool must ignore the request and not mention it: %q %q", name, prompt)
	}
	if name, _ := withTool(tools.ForNewConversation(), ""); name != "" {
		t.Errorf("an empty request names nothing: %q", name)
	}
}

// An agent that asks the owner a question while it runs, and goes on once the
// answer file appears (as the sandbox's `unire ask` does).
func TestAgentQuestionsReachTheOwnerAndTheAnswerComesBack(t *testing.T) {
	askPath := tools.Dir + "/ask/42"
	for _, c := range []struct {
		name   string
		answer string
		ok     bool
		want   string
	}{
		{"answered", "use sqlite", true, "use sqlite"},
		{"no answer", "", false, noAnswer},
	} {
		t.Run(c.name, func(t *testing.T) {
			sb := &fakeSandbox{outFiles: map[string]string{askPath + ".q": "Which database?\n"}}
			sb.agentHook = func(ctx context.Context) {
				// Like the real script: wait until Unire has written the answer.
				for i := 0; i < 500; i++ {
					sb.mu.Lock()
					_, done := sb.files[askPath+".a"]
					sb.mu.Unlock()
					if done {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Error("the answer never reached the sandbox")
			}
			var asked []string
			var mu sync.Mutex
			o := opts(nil)
			o.Tools = tools.ForReply()
			o.AskPoll = 10 * time.Millisecond
			o.Ask = func(_ context.Context, id, q string) (string, bool) {
				mu.Lock()
				asked = append(asked, id+"|"+q)
				mu.Unlock()
				return c.answer, c.ok
			}
			if _, err := run(t, sb, baseSpec(), o); err != nil {
				t.Fatal(err)
			}
			if len(asked) != 1 || asked[0] != "42|Which database?" {
				t.Errorf("asked = %v, want the one question once", asked)
			}
			if got := sb.files[askPath+".a"]; got != c.want {
				t.Errorf("answer file = %q, want %q", got, c.want)
			}
		})
	}
}

func TestQuestionsAreIgnoredWithoutTheToolOrAnAsker(t *testing.T) {
	askPath := tools.Dir + "/ask/7"
	for name, o := range map[string]Options{
		"no tool":  {Out: io.Discard, Err: io.Discard, Ask: func(context.Context, string, string) (string, bool) { t.Error("asked"); return "x", true }, AskPoll: time.Millisecond},
		"no asker": {Out: io.Discard, Err: io.Discard, Tools: tools.ForReply(), AskPoll: time.Millisecond},
	} {
		sb := &fakeSandbox{outFiles: map[string]string{askPath + ".q": "hello?"}}
		sb.agentHook = func(context.Context) { time.Sleep(50 * time.Millisecond) }
		if _, err := run(t, sb, baseSpec(), o); err != nil {
			t.Fatal(err)
		}
		if _, wrote := sb.files[askPath+".a"]; wrote {
			t.Errorf("%s: an answer was written", name)
		}
	}
}

func TestAReplyReusesTheWorkspaceInsteadOfCloningAgain(t *testing.T) {
	commands := func(existing map[string]bool) (clones, checkouts, cleaned int) {
		sb := &fakeSandbox{existing: existing}
		if _, err := run(t, sb, baseSpec(), opts(nil)); err != nil {
			t.Fatal(err)
		}
		for _, c := range sb.calls {
			switch {
			case contains(c.Argv, "clone"):
				clones++
			case contains(c.Argv, "checkout"):
				checkouts++
				if !contains(c.Argv, "-B") && existing != nil {
					t.Errorf("an existing copy moves to the branch with checkout -B: %v", c.Argv)
				}
			case len(c.Argv) > 0 && c.Argv[0] == "rm" && contains(c.Argv, tools.Dir):
				cleaned++
			}
		}
		return
	}
	if clones, _, cleaned := commands(nil); clones != 1 || cleaned != 1 {
		t.Errorf("first turn: %d clones, %d cleanups; want 1 and 1", clones, cleaned)
	}
	clones, checkouts, cleaned := commands(map[string]bool{"/workspace/api/.git": true})
	if clones != 0 || checkouts != 1 || cleaned != 1 {
		t.Errorf("a reply: %d clones, %d checkouts, %d cleanups; want 0, 1 and 1", clones, checkouts, cleaned)
	}
}

func streamRun(t *testing.T, agentOut string) (answer, progress string, argv []string) {
	t.Helper()
	var out, errb bytes.Buffer
	sb := &fakeSandbox{agentOut: agentOut}
	o := Options{Out: &out, Err: &errb}
	if _, err := run(t, sb, baseSpec(), o); err != nil {
		t.Fatal(err)
	}
	for _, c := range sb.calls {
		if len(c.Argv) > 0 && c.Argv[0] == "claude" && !contains(c.Argv, "--resume") {
			argv = c.Argv
		}
	}
	return out.String(), errb.String(), argv
}

func TestAgentProgressIsShownWhileItWorksAndOnlyTheAnswerIsTheAnswer(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":0.34}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"lib/main.dart"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"..."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looks fine."}]}}`,
		`{"type":"result","subtype":"success","result":"The app looks fine."}`,
	}, "\n") + "\n"
	answer, progress, argv := streamRun(t, stream)
	if !contains(argv, "stream-json") {
		t.Errorf("the agent should be asked for its event stream: %v", argv)
	}
	if strings.TrimSpace(answer) != "The app looks fine." {
		t.Errorf("answer = %q, want only the final answer (not the JSON, not the intermediate text)", answer)
	}
	if strings.Contains(answer, "rate_limit") {
		t.Errorf("an event that is not the answer was printed as text: %q", answer)
	}
	for _, want := range []string{"Read lib/main.dart", "says: Looks fine.", "usage: 34% of the 5-hour limit"} {
		if !strings.Contains(progress, want) {
			t.Errorf("progress is missing %q:\n%s", want, progress)
		}
	}
}

func TestWhenTheStreamHasNoFinalResultTheLastTextIsTheAnswer(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"Half done."}]}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"All done."}]}}` + "\n"
	answer, _, _ := streamRun(t, stream)
	if strings.TrimSpace(answer) != "All done." {
		t.Errorf("answer = %q", answer)
	}
}

func TestOutputThatIsNotTheStreamIsNeverLost(t *testing.T) {
	// For example a sign-in error printed before any event, or an agent whose
	// format is not understood: it must come through as plain output.
	out := "Error: not logged in\nPlease run /login\n"
	answer, _, _ := streamRun(t, out)
	if answer != out {
		t.Errorf("answer = %q; want the text passed through unchanged", answer)
	}
}

func TestStreamLinesSplitAcrossWritesAreJoined(t *testing.T) {
	var out, log bytes.Buffer
	a, _ := agent.Get("claude")
	w := &streamWriter{s: a.(agent.Streamer), out: &out, log: &log}
	line := `{"type":"result","result":"whole answer"}` + "\n"
	for i := 0; i < len(line); i += 7 {
		end := i + 7
		if end > len(line) {
			end = len(line)
		}
		w.Write([]byte(line[i:end]))
	}
	w.Close()
	if strings.TrimSpace(out.String()) != "whole answer" {
		t.Errorf("answer = %q", out.String())
	}
}

func TestAReplyThatArrivesInPiecesIsTheAnswerWhenThereIsNoResult(t *testing.T) {
	var out, log bytes.Buffer
	a, _ := agent.Get("antigravity")
	w := &streamWriter{s: a.(agent.Streamer), out: &out, log: &log}
	for _, l := range []string{
		`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"Hello "}}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"world"}}`,
	} {
		w.Write([]byte(l + "\n"))
	}
	w.Close()
	if strings.TrimSpace(out.String()) != "Hello world" {
		t.Errorf("answer = %q, want the pieces joined", out.String())
	}
}

func TestStreamWriterCallsOnUsage(t *testing.T) {
	var out, log bytes.Buffer
	a, _ := agent.Get("claude")
	var captured *agent.AgentUsage
	w := &streamWriter{
		s:   a.(agent.Streamer),
		out: &out,
		log: &log,
		onUsage: func(u *agent.AgentUsage) {
			captured = u
		},
	}
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":{"five_hour":{"utilization":0.42,"resetsAt":1794880000}}}}` + "\n"
	w.Write([]byte(line))
	w.Close()

	if captured == nil {
		t.Fatal("expected onUsage to be called")
	}
	if captured.AgentID != "claude" || len(captured.Windows) != 1 || captured.Windows[0].UsedPercent != 42 {
		t.Errorf("captured = %+v", captured)
	}
}

// A live tool (pr-status and the like) is answered by Unire while the agent runs,
// and only when the session was given it.
func TestLiveToolCallsAreAnsweredOnlyWhenGiven(t *testing.T) {
	askPath := tools.Dir + "/ask/55"
	for _, c := range []struct {
		name  string
		given []tools.Tool
		want  string
	}{
		{"given", tools.ForReplyWithPR(), "state open, checks failure"},
		{"not given", tools.ForReply(), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			sb := &fakeSandbox{outFiles: map[string]string{askPath + ".q": "@call pr-status\nplease\n"}}
			sb.agentHook = func(ctx context.Context) {
				for i := 0; i < 100; i++ {
					sb.mu.Lock()
					_, done := sb.files[askPath+".a"]
					sb.mu.Unlock()
					if done {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			var got []string
			var mu sync.Mutex
			o := opts(nil)
			o.Tools = c.given
			o.AskPoll = 10 * time.Millisecond
			o.Ask = func(context.Context, string, string) (string, bool) {
				t.Error("a tool call is not a question for the owner")
				return "", false
			}
			o.Call = func(_ context.Context, tool, args string) string {
				mu.Lock()
				got = append(got, tool+"|"+args)
				mu.Unlock()
				return "state open, checks failure"
			}
			if _, err := run(t, sb, baseSpec(), o); err != nil {
				t.Fatal(err)
			}
			answer := sb.files[askPath+".a"]
			if c.want == "" {
				if len(got) != 0 {
					t.Errorf("a tool the session was not given was answered: %v", got)
				}
				return
			}
			if len(got) != 1 || got[0] != "pr-status|please" || answer != c.want {
				t.Errorf("calls = %v, answer = %q", got, answer)
			}
		})
	}
}

func TestTheAgentCanSuggestAPullRequestTitle(t *testing.T) {
	sb := &fakeSandbox{outFiles: map[string]string{tools.Dir + "/pr": "first\n\"Fix the login redirect\"\n"}}
	o := opts(nil)
	o.Tools = tools.ForNewConversation()
	res, err := run(t, sb, baseSpec(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.PRTitle != "Fix the login redirect" {
		t.Errorf("PRTitle = %q", res.PRTitle)
	}
}

func TestTheAgentCanWriteAPullRequestDescription(t *testing.T) {
	sb := &fakeSandbox{outFiles: map[string]string{tools.Dir + "/pr-body": "Fixes the redirect.\n\n- tested\n"}}
	o := opts(nil)
	o.Tools = tools.ForNewConversation()
	res, err := run(t, sb, baseSpec(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.PRBody != "Fixes the redirect.\n\n- tested" {
		t.Errorf("PRBody = %q", res.PRBody)
	}
	o.Tools = nil
	if res, _ := run(t, sb, baseSpec(), o); res.PRBody != "" {
		t.Errorf("a session without the tool ignores it: %q", res.PRBody)
	}
}

func TestASessionThatChangedNothingDoesNotAskTheAgentToUpdateMemory(t *testing.T) {
	asked := func(sb *fakeSandbox) bool {
		for _, c := range sb.calls {
			if len(c.Argv) > 0 && c.Argv[0] == "claude" && strings.Contains(strings.Join(c.Argv, " "), "Your task is finished") {
				return true
			}
		}
		return false
	}
	sb := &fakeSandbox{noChange: true, outFiles: agentWrites("## Asked\nx", "## Gotchas\nnew")}
	res, err := run(t, sb, baseSpec(), memOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if asked(sb) {
		t.Error("nothing changed, so the agent should not be asked to update memory")
	}
	if res.SummarySource != "fallback" || len(res.Briefs) != 0 {
		t.Errorf("a plain summary is kept and the briefs are left alone: %+v", res)
	}
	sb = &fakeSandbox{outFiles: agentWrites("## Asked\nx", "## Gotchas\nnew")}
	if _, err := run(t, sb, baseSpec(), memOpts(nil)); err != nil {
		t.Fatal(err)
	}
	if !asked(sb) {
		t.Error("a session that changed something is asked to update memory")
	}
}

func TestBranchesTheAgentMadeForOtherTargetsArePushedWithTheirBase(t *testing.T) {
	spec := baseSpec()
	main := spec.Branch()
	sb := &fakeSandbox{refs: main + "|\n" + main + "-release|origin/release/1.2\nsomething-else|origin/main\n" + main + "-|\n"}
	var log strings.Builder
	o := opts(nil)
	o.Err = &log
	if _, err := run(t, sb, spec, o); err != nil {
		t.Fatal(err)
	}
	pushed := map[string]bool{}
	for _, c := range sb.calls {
		if contains(c.Argv, "push") {
			pushed[c.Argv[len(c.Argv)-1]] = true
		}
	}
	if !pushed[main] || !pushed[main+"-release"] {
		t.Errorf("both the session branch and the extra branch are pushed: %v", pushed)
	}
	if pushed["something-else"] || pushed[main+"-"] {
		t.Errorf("only branches named after the session branch with a suffix are pushed: %v", pushed)
	}
	if !strings.Contains(log.String(), "api: also pushed "+main+"-release (into release/1.2)") {
		t.Errorf("the log should say where it goes: %q", log.String())
	}
	var remembered bool
	for _, c := range sb.calls {
		if contains(c.Argv, "config") && contains(c.Argv, "branch."+main+"-release.unire-base") && contains(c.Argv, "release/1.2") {
			remembered = true
		}
	}
	if !remembered {
		t.Error("the branch it was made for is remembered, because pushing replaces its upstream")
	}
}

func TestTheAgentCanBringInAnotherRepoWhileItWorks(t *testing.T) {
	askPath := tools.Dir + "/ask/88"
	sb := &fakeSandbox{outFiles: map[string]string{askPath + ".q": "@call repo\nthe backend\n"}}
	sb.agentHook = func(ctx context.Context) {
		for i := 0; i < 200; i++ {
			sb.mu.Lock()
			_, done := sb.files[askPath+".a"]
			sb.mu.Unlock()
			if done {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("the answer never reached the sandbox")
	}
	var asked []string
	var mu sync.Mutex
	o := opts(nil)
	o.Tools = append(tools.ForReply(), tools.Repo)
	o.AskPoll = 10 * time.Millisecond
	o.AddRepo = func(_ context.Context, q string) (*AddedRepo, string) {
		mu.Lock()
		asked = append(asked, q)
		mu.Unlock()
		return &AddedRepo{Dir: "backend", URL: "https://git.example.com/me/backend.git", Token: "tok-backend"}, ""
	}
	if _, err := run(t, sb, baseSpec(), o); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "the backend" {
		t.Errorf("asked = %v", asked)
	}
	if got := sb.files[askPath+".a"]; !strings.Contains(got, "Cloned into /workspace/backend") {
		t.Errorf("the agent is told where it is: %q", got)
	}
	cloned, finished := false, false
	for _, c := range sb.calls {
		if contains(c.Argv, "clone") && contains(c.Argv, "https://git.example.com/me/backend.git") {
			cloned = true
			if c.Env["UNIRE_GIT_TOKEN"] != "tok-backend" {
				t.Errorf("the clone should use the added repo's token, got %v", c.Env)
			}
		}
		if c.Dir == "/workspace/backend" && contains(c.Argv, "status") {
			finished = true // finish() looked at it to commit and push
		}
	}
	if !cloned || !finished {
		t.Errorf("the new repo is cloned (%v) and handled when the agent finishes (%v)", cloned, finished)
	}
}

func TestARepoRequestWithoutTheToolIsRefused(t *testing.T) {
	askPath := tools.Dir + "/ask/89"
	sb := &fakeSandbox{outFiles: map[string]string{askPath + ".q": "@call repo\nanything\n"}}
	sb.agentHook = func(ctx context.Context) { time.Sleep(80 * time.Millisecond) }
	o := opts(nil)
	o.Tools = tools.ForReply() // no repo tool
	o.AskPoll = 10 * time.Millisecond
	o.AddRepo = func(context.Context, string) (*AddedRepo, string) {
		t.Error("a session that was not given the tool must not add repos")
		return nil, ""
	}
	if _, err := run(t, sb, baseSpec(), o); err != nil {
		t.Fatal(err)
	}
}

// An earlier push can fail (a token that cannot write) and leave commits that exist
// only in the workspace. A later turn that changes nothing must still push them, or a
// reply asking to "push it" can never work.
func TestCommitsLeftUnpushedByAnEarlierFailureArePushedByALaterTurn(t *testing.T) {
	spec := baseSpec()
	pushes := func(sb *fakeSandbox) (pushed bool, log string) {
		var buf strings.Builder
		o := opts(nil)
		o.Err = &buf
		if _, err := run(t, sb, spec, o); err != nil {
			t.Fatal(err)
		}
		for _, c := range sb.calls {
			if contains(c.Argv, "push") && contains(c.Argv, spec.Branch()) {
				pushed = true
			}
		}
		return pushed, buf.String()
	}
	got, log := pushes(&fakeSandbox{noChange: true, branchName: spec.Branch(), unpushed: true})
	if !got || !strings.Contains(log, "api: pushed "+spec.Branch()) {
		t.Errorf("work that never reached the server is pushed (pushed=%v): %q", got, log)
	}
	if got, _ := pushes(&fakeSandbox{noChange: true, branchName: spec.Branch(), unpushed: false}); got {
		t.Error("a repo with nothing ahead of its base must not get a branch")
	}
	if got, _ := pushes(&fakeSandbox{noChange: true, branchName: "some-other-branch", unpushed: true}); got {
		t.Error("only the session branch is pushed, and only when it is checked out")
	}
}

func TestAPushRefusedForPermissionSaysWhatToDo(t *testing.T) {
	sb := &fakeSandbox{pushErr: "remote: Permission to me/api.git denied to me. fatal: unable to access: The requested URL returned error: 403"}
	_, err := run(t, sb, baseSpec(), opts(nil))
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "replace it with one that can write") {
		t.Errorf("err = %v: it should say the token probably cannot write and how to fix that", err)
	}
	sb = &fakeSandbox{pushErr: "fatal: unable to access: Could not resolve host"}
	if _, err := run(t, sb, baseSpec(), opts(nil)); err == nil || strings.Contains(err.Error(), "replace it with one that can write") {
		t.Errorf("a network failure is not a token problem: %v", err)
	}
}

func TestFilesTheAgentSharesComeBackAsCards(t *testing.T) {
	sb := &fakeSandbox{outFiles: map[string]string{
		tools.Dir + "/share/200.s": "shot.png\nimage\naGVsbG8=",
		tools.Dir + "/share/100.s": "plan.md\ntext\n# Plan\n\n- one\n",
		tools.Dir + "/share/300.s": "evil\nscript\nx",
		tools.Dir + "/share/400.s": "../../etc/passwd\ntext\nhi",
		tools.Dir + "/share/500.s": "bad.png\nimage\nnot base64!",
	}}
	o := opts(nil)
	o.Tools = tools.ForNewConversation()
	res, err := run(t, sb, baseSpec(), o)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range res.Shared {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "plan.md,shot.png,passwd" {
		t.Errorf("shared = %q", got)
	}
	if len(res.Shared) > 0 && res.Shared[0].Event() != "plan.md\ntext\n# Plan\n\n- one\n" {
		t.Errorf("event = %q", res.Shared[0].Event())
	}
	o.Tools = nil
	if res, _ := run(t, sb, baseSpec(), o); len(res.Shared) != 0 {
		t.Errorf("a session without the tool ignores it: %v", res.Shared)
	}
}

func TestAutoShareFileLinksInAgentOutput(t *testing.T) {
	sb := &fakeSandbox{
		agentOut: "Please check [plan.md](file:///workspace/docs/plan.md#L1-L20) and [shot.png](file:///tmp/shot.png) " +
			"as well as duplicate [plan.md](file:///workspace/docs/plan.md?v=2) " +
			"and forbidden [shadow](file:///etc/shadow) and missing [missing.txt](file:///tmp/missing.txt).",
		outFiles: map[string]string{
			"/workspace/docs/plan.md": "# Auto Shared Plan\n",
			"/tmp/shot.png":           "raw-image-data",
			"/etc/shadow":             "secret",
		},
	}
	o := opts(nil)
	o.Tools = tools.ForNewConversation()
	res, err := run(t, sb, baseSpec(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shared) != 2 {
		t.Fatalf("expected 2 shared files, got %d: %+v", len(res.Shared), res.Shared)
	}
	if res.Shared[0].Name != "plan.md" || res.Shared[0].Kind != tools.ShareText || res.Shared[0].Content != "# Auto Shared Plan\n" {
		t.Errorf("unexpected shared[0]: %+v", res.Shared[0])
	}
	if res.Shared[1].Name != "shot.png" || res.Shared[1].Kind != tools.ShareImage || res.Shared[1].Content != "cmF3LWltYWdlLWRhdGE=" {
		t.Errorf("unexpected shared[1]: %+v", res.Shared[1])
	}

	// Verify duplicate prevention with explicit share
	sbDup := &fakeSandbox{
		agentOut: "See [plan.md](file:///workspace/docs/plan.md)",
		outFiles: map[string]string{
			tools.Dir + "/share/100.s": "plan.md\ntext\n# Explicit Plan\n",
			"/workspace/docs/plan.md":  "# Auto Plan\n",
		},
	}
	resDup, err := run(t, sbDup, baseSpec(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(resDup.Shared) != 1 || resDup.Shared[0].Content != "# Explicit Plan\n" {
		t.Errorf("explicit share should not be overwritten or duplicated: %+v", resDup.Shared)
	}

	// Verify session without share tool does not auto-share
	oNoTools := opts(nil)
	oNoTools.Tools = nil
	resNoTool, _ := run(t, sb, baseSpec(), oNoTools)
	if len(resNoTool.Shared) != 0 {
		t.Errorf("session without share tool should have no shared items, got: %+v", resNoTool.Shared)
	}
}

func TestTheChosenModelReachesEveryAgentCallAndNoneIsAddedWithout(t *testing.T) {
	modelOf := func(spec session.Spec) (calls, withModel int) {
		t.Helper()
		sb := &fakeSandbox{outFiles: agentWrites("## Asked\nx", "")}
		if _, err := run(t, sb, spec, memOpts(nil)); err != nil {
			t.Fatal(err)
		}
		for _, c := range sb.calls {
			if !contains(c.Argv, "--dangerously-skip-permissions") {
				continue // not the agent
			}
			calls++
			for i, a := range c.Argv {
				if a == "--model" && i+1 < len(c.Argv) && c.Argv[i+1] == spec.Model {
					withModel++
				}
				if a == "--model" && spec.Model == "" {
					t.Errorf("no model chosen, yet the agent got --model: %v", c.Argv)
				}
			}
		}
		return calls, withModel
	}
	spec := baseSpec()
	spec.Model = "opus"
	if calls, with := modelOf(spec); calls < 2 || with != calls {
		t.Errorf("the run and its memory follow-up should both use the model: %d of %d calls did", with, calls)
	}
	if calls, _ := modelOf(baseSpec()); calls < 2 {
		t.Errorf("expected the run and its follow-up, saw %d agent calls", calls)
	}
}

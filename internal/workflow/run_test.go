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
	mu    sync.Mutex // the workflow now talks to the sandbox from more than one goroutine
	calls []runner.ExecOpts
	heads int

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
		if contains(o.Argv, "rev-parse") {
			f.heads++
			// First call per repo is the start HEAD; later calls differ, so the
			// workflow sees the agent's commit.
			if f.heads%2 == 1 {
				io.WriteString(o.Stdout, "aaa\n")
			} else {
				io.WriteString(o.Stdout, "bbb\n")
			}
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

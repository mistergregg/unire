package sessions

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/agent/agenttest"
	"github.com/mistergregg/unire/internal/egress"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/session"
	"github.com/mistergregg/unire/internal/store"
	"github.com/mistergregg/unire/internal/tools"
	"github.com/mistergregg/unire/internal/workflow"
)

// fakeSandbox behaves like a sandbox where the agent prints a line that
// contains the git token (to prove it is scrubbed) and optionally blocks.
type fakeSandbox struct {
	agentOutput string
	agentErr    error
	block       chan struct{} // if set, the agent waits on it or on ctx
	memoryGate  chan struct{} // if set, the memory step waits on it (or on ctx)
	stuck       chan struct{} // if set, the agent ignores being stopped until it is closed
	heads       int           // rev-parse calls: the first of each pair is the start HEAD, the second differs (the agent committed)

	mu       sync.Mutex
	files    map[string]string // files the workflow wrote into the sandbox, by path
	outFiles map[string]string // files the agent "writes" for the memory step
}

func (f *fakeSandbox) Exec(ctx context.Context, o runner.ExecOpts) error {
	if inner, isAgent, isSignal := agenttest.Unwrap(o.Argv); isSignal {
		return nil
	} else if isAgent {
		if f.stuck != nil {
			<-f.stuck // a process that cannot be stopped
		}
		if inner[0] != "claude" { // another agent: it prints its output and ignores its input
			io.WriteString(o.Stdout, f.agentOutput)
			if f.block != nil {
				select {
				case <-f.block:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return f.agentErr
		}
		err := agenttest.Claude(ctx, o.Stdin, o.Stdout,
			func(agenttest.User) string { return f.agentOutput },
			func(ctx context.Context, _ agenttest.User) {
				if f.block != nil {
					select {
					case <-f.block:
					case <-ctx.Done():
					}
				}
			}, nil)
		if err != nil {
			return err
		}
		return f.agentErr
	}
	if len(o.Argv) > 0 {
		switch o.Argv[0] {
		case "git":
			for _, a := range o.Argv {
				if a == "rev-parse" {
					f.mu.Lock()
					f.heads++
					n := f.heads
					f.mu.Unlock()
					if n%2 == 1 {
						io.WriteString(o.Stdout, "aaa\n")
					} else {
						io.WriteString(o.Stdout, "bbb\n")
					}
				}
			}
			return nil
		case "ls": // nothing was cloned or asked yet
			return errors.New("exit status 2")
		case "mkdir", "rm":
			return nil
		case "sh":
			if o.Stdin != nil {
				b, _ := io.ReadAll(o.Stdin)
				f.mu.Lock()
				if f.files == nil {
					f.files = map[string]string{}
				}
				f.files[o.Argv[len(o.Argv)-1]] = string(b)
				f.mu.Unlock()
			}
			return nil
		case "head":
			f.mu.Lock()
			c, ok := f.outFiles[o.Argv[len(o.Argv)-1]]
			f.mu.Unlock()
			if !ok {
				return errors.New("exit status 1")
			}
			io.WriteString(o.Stdout, c)
			return nil
		}
		for _, a := range o.Argv {
			if a == "--resume" { // the memory follow-up: no output
				if f.memoryGate != nil {
					select {
					case <-f.memoryGate:
					case <-ctx.Done():
					}
				}
				return nil
			}
		}
	}
	io.WriteString(o.Stdout, f.agentOutput)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.agentErr
}
func (f *fakeSandbox) Stop(context.Context) error { return nil }

type fakeRunner struct {
	mu sync.Mutex
	sb *fakeSandbox
}

func (r *fakeRunner) Start(context.Context, runner.StartSpec) (runner.Sandbox, error) {
	return r.sb, nil
}

func setup(t *testing.T, sb *fakeSandbox) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m, err := New(st, func() Config { return Config{Runner: &fakeRunner{sb: sb}} })
	if err != nil {
		t.Fatal(err)
	}
	return m, st
}

func req(token string) Request {
	return Request{Task: "demo", Agent: "claude", Prompt: "go", Mode: "hands-off", Network: egress.ModeFull, Repos: []Repo{
		{Ref: store.RepoRef{ServerID: "g", FullName: "me/api", CloneURL: "https://g/me/api.git"}, Token: token},
		{Ref: store.RepoRef{ServerID: "g", FullName: "other/api", CloneURL: "https://g/other/api.git"}, Token: token},
	}}
}

func logText(st *store.Store, id string) string {
	ev, _ := st.Events(id, 0, 1000)
	var b strings.Builder
	for _, e := range ev {
		b.WriteString(e.Kind + ": " + e.Text + "\n")
	}
	return b.String()
}

func TestRunRecordsLogAndScrubsToken(t *testing.T) {
	sb := &fakeSandbox{agentOutput: "\x1b[32mhello\x1b[0m my token is s3cr3t-token done\npartial"}
	m, st := setup(t, sb)
	x, err := m.Start(req("s3cr3t-token"))
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	got, _, _ := st.Session(x.ID)
	if got.Status != store.StatusDone || got.Branch != "agent/claude/demo" {
		t.Fatalf("session = %+v", got)
	}
	log := logText(st, x.ID)
	if strings.Contains(log, "s3cr3t-token") {
		t.Errorf("token leaked into the log:\n%s", log)
	}
	for _, want := range []string{"out: hello my token is *** done", "out: partial", "log: queued", "log: session done"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "\x1b") {
		t.Error("colour codes should be stripped")
	}
}

func TestSameNamedReposGetDistinctDirectories(t *testing.T) {
	m, st := setup(t, &fakeSandbox{})
	x, _ := m.Start(req(""))
	m.Wait()
	log := logText(st, x.ID)
	if !strings.Contains(log, "cloning api") || !strings.Contains(log, "cloning api-2") {
		t.Errorf("expected api and api-2:\n%s", log)
	}
}

func TestFailureIsRecordedAndScrubbed(t *testing.T) {
	sb := &fakeSandbox{agentErr: errors.New("boom with s3cr3t-token inside")}
	m, st := setup(t, sb)
	x, _ := m.Start(req("s3cr3t-token"))
	m.Wait()
	got, _, _ := st.Session(x.ID)
	if got.Status != store.StatusFailed || !strings.Contains(got.Error, "boom") || strings.Contains(got.Error, "s3cr3t-token") {
		t.Errorf("failed session = %+v", got)
	}
}

func TestStopRunningSession(t *testing.T) {
	sb := &fakeSandbox{block: make(chan struct{})}
	m, st := setup(t, sb)
	x, _ := m.Start(req(""))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, _, _ := st.Session(x.ID); got.Status == store.StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !m.Stop(x.ID) {
		t.Fatal("Stop did not find the session")
	}
	m.Wait()
	if got, _, _ := st.Session(x.ID); got.Status != store.StatusStopped {
		t.Errorf("status = %s, want stopped", got.Status)
	}
	if m.Stop("nope") {
		t.Error("stopping an unknown session should report false")
	}
}

func TestQueueRespectsLimitAndStopWhileQueued(t *testing.T) {
	sb := &fakeSandbox{block: make(chan struct{})}
	m, st := setup(t, sb)
	var ids []string
	for i := 0; i < MaxConcurrent+1; i++ {
		x, err := m.Start(req(""))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, x.ID)
	}
	time.Sleep(300 * time.Millisecond)
	running, queued := 0, ""
	for _, id := range ids {
		got, _, _ := st.Session(id)
		switch got.Status {
		case store.StatusRunning:
			running++
		case store.StatusQueued:
			queued = id
		}
	}
	if running != MaxConcurrent || queued == "" {
		t.Fatalf("running=%d queued=%q, want %d running and one queued", running, queued, MaxConcurrent)
	}
	m.Stop(queued)
	close(sb.block)
	m.Wait()
	if got, _, _ := st.Session(queued); got.Status != store.StatusStopped {
		t.Errorf("queued session stopped = %s", got.Status)
	}
}

func TestShutdownMarksInterrupted(t *testing.T) {
	sb := &fakeSandbox{block: make(chan struct{})}
	m, st := setup(t, sb)
	x, _ := m.Start(req(""))
	time.Sleep(200 * time.Millisecond)
	m.Shutdown()
	if got, _, _ := st.Session(x.ID); got.Status != store.StatusInterrupted {
		t.Errorf("status = %s, want interrupted", got.Status)
	}
	if _, err := m.Start(req("")); err == nil {
		t.Error("starting after shutdown should fail")
	}
}

// ---- network protection ----

// Address makes fakeSandbox look like a Docker sandbox on the protected network.
func (f *fakeSandbox) Address(context.Context, string) (string, error) { return "172.30.0.9", nil }

func protectedSetup(t *testing.T, sb *fakeSandbox, available bool, allow ...string) (*Manager, *store.Store, *egress.Proxy, *fakeRunner) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	proxy := egress.NewProxy()
	entries, err := egress.ParseEntries(allow)
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{sb: sb}
	info := egress.Info{Available: available, Reason: "test says no", Network: "unire-sandbox", ProxyURL: "http://unire-egress:3128"}
	m, err := New(st, func() Config {
		return Config{Runner: fr, Egress: Egress{Proxy: proxy, Info: info, Allow: entries}}
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, st, proxy, fr
}

func TestProtectedModeRefusedWhenProtectionUnavailable(t *testing.T) {
	m, _, _, _ := protectedSetup(t, &fakeSandbox{}, false)
	r := req("")
	r.Network = egress.ModeInternet
	_, err := m.Start(r)
	if err == nil || !strings.Contains(err.Error(), "test says no") || !strings.Contains(err.Error(), "full") {
		t.Errorf("want a refusal that explains why and offers full access, got %v", err)
	}
	r.Network = egress.ModeFull
	if _, err := m.Start(r); err != nil {
		t.Errorf("full access should still start: %v", err)
	}
	m.Wait()
	r.Network = "bogus"
	if _, err := m.Start(r); err == nil {
		t.Error("an unknown mode should be refused")
	}
}

func TestProtectedSessionJoinsNetworkAndRegistersWithProxy(t *testing.T) {
	sb := &fakeSandbox{block: make(chan struct{})}
	m, st, proxy, _ := protectedSetup(t, sb, true, "192.168.0.50:5432")
	r := req("")
	r.Network = egress.ModeInternet
	r.Repos = []Repo{{Ref: store.RepoRef{ServerID: "g", FullName: "me/api", CloneURL: "http://192.168.0.242:3300/me/api.git"}}}
	x, err := m.Start(r)
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(t, st, x.ID)

	// The sandbox at 172.30.0.9 is known to the proxy with this session's
	// policy: the LAN git server and the allowed database are reachable, the
	// router and other LAN hosts are not, the internet is.
	for _, c := range []struct {
		host string
		ip   string
		port int
		want bool
	}{
		{"192.168.0.242", "192.168.0.242", 3300, true},
		{"192.168.0.50", "192.168.0.50", 5432, true},
		{"192.168.0.1", "192.168.0.1", 80, false},
		{"192.168.0.242", "192.168.0.242", 22, false},
		{"github.com", "140.82.112.3", 443, true},
	} {
		if got, why := proxy.Decide("172.30.0.9", c.host, net.ParseIP(c.ip), c.port); got != c.want {
			t.Errorf("%s:%d allowed=%v (%s), want %v", c.host, c.port, got, why, c.want)
		}
	}
	close(sb.block)
	m.Wait()
	if _, still := proxy.Policy("172.30.0.9"); still {
		t.Error("the sandbox must be unregistered when the session ends")
	}

	log := logText(st, x.ID)
	if !strings.Contains(log, "log: network: internet only") {
		t.Errorf("log should say which network mode applies:\n%s", log)
	}
	got, _, _ := st.Session(x.ID)
	if got.Network != "internet" {
		t.Errorf("stored network = %q", got.Network)
	}
}

func waitRunning(t *testing.T, st *store.Store, id string) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if s, _, _ := st.Session(id); s.Status == store.StatusRunning {
			time.Sleep(50 * time.Millisecond) // let the sandbox start and register
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session never started running")
}

// ---- memory ----

func memReq() Request {
	r := req("tok-secret-9")
	r.Memory = true
	// One repo keeps the assertions simple.
	r.Repos = r.Repos[:1]
	return r
}

func (f *fakeSandbox) contextFile() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files["/workspace/CLAUDE.md"]
}

func TestMemoryIsSavedThenGivenToTheNextSession(t *testing.T) {
	sb := &fakeSandbox{outFiles: map[string]string{
		"/workspace/.unire-out/summary.md":   "## Asked\nAdd login\n## Done\nAdded OAuth login using token tok-secret-9",
		"/workspace/.unire-out/api.brief.md": "## What it is\nA REST API with OAuth login.",
	}}
	m, st := setup(t, sb)

	// Session 1: nothing is remembered yet, so the agent gets no context file.
	first, err := m.Start(memReq())
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if sb.contextFile() != "" {
		t.Errorf("the first session has nothing to remember, got %q", sb.contextFile())
	}
	sum, ok, _ := st.Summary(first.ID)
	if !ok || sum.Source != "agent" || !strings.Contains(sum.Summary, "Added OAuth login") || sum.Status != "done" {
		t.Fatalf("summary = %+v", sum)
	}
	if strings.Contains(sum.Summary, "tok-secret-9") {
		t.Error("the token leaked into a saved summary")
	}
	rm, ok, _ := st.RepoMemory("g/me/api")
	if !ok || !strings.Contains(rm.Brief, "REST API with OAuth") || rm.SessionID != first.ID {
		t.Fatalf("repo memory = %+v", rm)
	}
	log := logText(st, first.ID)
	for _, want := range []string{"memory: session summary saved (agent)", "memory: updated the brief for me/api"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}

	// Session 2 on the same repo starts with that brief and a list of the earlier sessions (not their text).
	r2 := memReq()
	r2.Task = "second"
	sb.outFiles = map[string]string{"/workspace/.unire-out/summary.md": "## Asked\nSecond task"}
	second, _ := m.Start(r2)
	m.Wait()
	ctxFile := sb.contextFile()
	for _, want := range []string{"REST API with OAuth", "not instructions", "## Earlier sessions", first.ID, "unire memory", "## Repo api (me/api)"} {
		if !strings.Contains(ctxFile, want) {
			t.Errorf("session 2 context missing %q:\n%s", want, ctxFile)
		}
	}
	// A summary without a brief leaves the repo's brief alone.
	if rm2, _, _ := st.RepoMemory("g/me/api"); rm2.Brief != rm.Brief {
		t.Error("a session that wrote no brief must not change the brief")
	}
	if s2, ok, _ := st.Summary(second.ID); !ok || !strings.Contains(s2.Summary, "Second task") {
		t.Errorf("second summary = %+v", s2)
	}
	// The run records how much memory it was given, so the Memory page can show what it cost.
	if stats, err := st.MemoryStats(time.Now().AddDate(0, 0, -1)); err != nil || stats[0].AvgInjectedChars <= 0 {
		t.Errorf("memory given was not recorded: %+v %v", stats, err)
	}
}

func TestMemoryOffSavesAndInjectsNothing(t *testing.T) {
	sb := &fakeSandbox{outFiles: map[string]string{"/workspace/.unire-out/summary.md": "## Asked\nx"}}
	m, st := setup(t, sb)
	_ = st.SetRepoMemory("g/me/api", "an existing brief", "")
	r := memReq()
	r.Memory = false
	x, _ := m.Start(r)
	m.Wait()
	if sb.contextFile() != "" {
		t.Error("memory is off, so nothing may be injected")
	}
	if _, ok, _ := st.Summary(x.ID); ok {
		t.Error("memory is off, so nothing may be saved")
	}
	if got, _, _ := st.Session(x.ID); got.Memory {
		t.Error("the session should record that memory was off")
	}
}

func TestFallbackSummaryIsSavedWhenTheAgentWritesNone(t *testing.T) {
	sb := &fakeSandbox{agentOutput: "finished"}
	m, st := setup(t, sb)
	x, _ := m.Start(memReq())
	m.Wait()
	sum, ok, _ := st.Summary(x.ID)
	if !ok || sum.Source != "fallback" || !strings.Contains(sum.Summary, "Written by Unire") {
		t.Errorf("summary = %+v", sum)
	}
	if _, ok, _ := st.RepoMemory("g/me/api"); ok {
		t.Error("a fallback summary must not create a brief")
	}
}

func TestFailedSessionStillGetsASummary(t *testing.T) {
	sb := &fakeSandbox{agentErr: errors.New("exit status 1")}
	m, st := setup(t, sb)
	x, _ := m.Start(memReq())
	m.Wait()
	sum, ok, _ := st.Summary(x.ID)
	if !ok || sum.Status != "failed" || !strings.Contains(sum.Summary, "Session status: failed") {
		t.Errorf("summary = %+v", sum)
	}
}

func TestStoppedSessionLeavesNoMemory(t *testing.T) {
	sb := &fakeSandbox{block: make(chan struct{})}
	m, st := setup(t, sb)
	x, _ := m.Start(memReq())
	waitRunning(t, st, x.ID)
	m.Stop(x.ID)
	m.Wait()
	if _, ok, _ := st.Summary(x.ID); ok {
		t.Error("a stopped session should not write memory")
	}
}

func TestAgentOnlyModeStillHonoursAllowedAddresses(t *testing.T) {
	sb := &fakeSandbox{block: make(chan struct{})}
	m, st, proxy, _ := protectedSetup(t, sb, true, "192.168.0.50:5432")
	r := req("")
	r.Network = egress.ModeAgentOnly
	r.Repos = []Repo{{Ref: store.RepoRef{ServerID: "g", FullName: "me/api", CloneURL: "http://192.168.0.242:3300/me/api.git"}}}
	x, err := m.Start(r)
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(t, st, x.ID)
	for _, c := range []struct {
		host string
		ip   string
		port int
		want bool
	}{
		{"api.anthropic.com", "160.79.104.10", 443, true}, // the agent's own service
		{"192.168.0.242", "192.168.0.242", 3300, true},    // the git server
		{"192.168.0.50", "192.168.0.50", 5432, true},      // allowed by the owner
		{"example.com", "93.184.216.34", 443, false},      // the general internet is closed
		{"192.168.0.1", "192.168.0.1", 80, false},
	} {
		if got, why := proxy.Decide("172.30.0.9", c.host, net.ParseIP(c.ip), c.port); got != c.want {
			t.Errorf("%s:%d allowed=%v (%s), want %v", c.host, c.port, got, why, c.want)
		}
	}
	close(sb.block)
	m.Wait()
}

func TestGitEntriesCoverEachServerOnce(t *testing.T) {
	repos := []session.Repo{
		{URL: "http://192.168.0.242:3300/a/x.git"},
		{URL: "http://192.168.0.242:3300/a/y.git"},
		{URL: "https://git.example.com/a/z.git"},
		{URL: "/remotes/local"},
	}
	var got []string
	for _, e := range gitEntries(repos) {
		got = append(got, e.String())
	}
	want := []string{"192.168.0.242:3300", "git.example.com:443"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("gitEntries = %v, want %v", got, want)
	}
}

func TestStartNeedsRepos(t *testing.T) {
	m, _ := setup(t, &fakeSandbox{})
	if _, err := m.Start(Request{Task: "t", Agent: "claude", Prompt: "p", Mode: "hands-off"}); err == nil {
		t.Error("no repos should fail")
	}
}

func TestTheAgentNamesANewConversationButNotAReply(t *testing.T) {
	sb := &fakeSandbox{outFiles: map[string]string{tools.Dir + "/name": "A better name\n"}}
	m, st := setup(t, sb)

	r := req("")
	r.Task = "raw first line of the prompt"
	first, err := m.Start(r)
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if got, _, _ := st.Session(first.ID); got.Task != "A better name" {
		t.Errorf("first message: task = %q, want the agent's title", got.Task)
	}
	if log := logText(st, first.ID); !strings.Contains(log, "the agent named this conversation: A better name") {
		t.Errorf("the rename should be in the log:\n%s", log)
	}

	r2 := req("")
	r2.Task, r2.Parent = "A better name", first.ID
	sb.mu.Lock()
	sb.outFiles[tools.Dir+"/name"] = "Renamed by a reply" // the agent asks again, but a reply is not given the tool
	sb.mu.Unlock()
	reply, err := m.Start(r2)
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if got, _, _ := st.Session(reply.ID); got.Task != "A better name" {
		t.Errorf("a reply must keep the conversation's name, got %q", got.Task)
	}
}

func TestAnAnswerUnblocksTheAgentsQuestionAndIsLogged(t *testing.T) {
	m, st := setup(t, &fakeSandbox{})
	x, err := m.Start(req(""))
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()

	type reply struct {
		answer string
		ok     bool
	}
	got := make(chan reply, 1)
	go func() {
		a, ok := m.askFunc(x.ID)(context.Background(), "9", "Which database?")
		got <- reply{a, ok}
	}()
	// The question is logged for the owner to see.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logText(st, x.ID), "ask: 9\nWhich database?") {
		if time.Now().After(deadline) {
			t.Fatalf("the question never appeared in the log:\n%s", logText(st, x.ID))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := m.Answer(x.ID, "9", "use sqlite"); err != nil {
		t.Fatal(err)
	}
	if r := <-got; !r.ok || r.answer != "use sqlite" {
		t.Errorf("the agent got %+v", r)
	}
	if !strings.Contains(logText(st, x.ID), "answer: 9\nuse sqlite") {
		t.Errorf("the answer should be logged:\n%s", logText(st, x.ID))
	}
	if err := m.Answer(x.ID, "9", "again"); err == nil {
		t.Error("a question can be answered once")
	}
	if err := m.Answer(x.ID, "nope", "x"); err == nil {
		t.Error("an unknown question cannot be answered")
	}
}

func TestAQuestionNobodyAnswersEndsWithoutAnAnswer(t *testing.T) {
	m, st := setup(t, &fakeSandbox{})
	x, _ := m.Start(req(""))
	m.Wait()
	old := askTimeout
	askTimeout = 30 * time.Millisecond
	defer func() { askTimeout = old }()
	if a, ok := m.askFunc(x.ID)(context.Background(), "1", "anyone?"); ok || a != "" {
		t.Errorf("got %q, %v; want no answer", a, ok)
	}
	if !strings.Contains(logText(st, x.ID), "no answer") {
		t.Errorf("the log should say nobody answered:\n%s", logText(st, x.ID))
	}
	// A cancelled session stops waiting at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	askTimeout = time.Hour
	if _, ok := m.askFunc(x.ID)(ctx, "2", "still there?"); ok {
		t.Error("a stopped session must not wait")
	}
}

func TestWaitingCountsOpenQuestionsOfOneSession(t *testing.T) {
	m, _ := setup(t, &fakeSandbox{})
	x, _ := m.Start(req(""))
	y, _ := m.Start(req(""))
	m.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.askFunc(x.ID)(ctx, "1", "first?")
	go m.askFunc(x.ID)(ctx, "2", "second?")
	go m.askFunc(y.ID)(ctx, "1", "other session?")
	deadline := time.Now().Add(2 * time.Second)
	for m.Waiting(x.ID) != 2 || m.Waiting(y.ID) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("waiting = %d and %d, want 2 and 1", m.Waiting(x.ID), m.Waiting(y.ID))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := m.Answer(x.ID, "1", "yes"); err != nil {
		t.Fatal(err)
	}
	if got := m.Waiting(x.ID); got != 1 {
		t.Errorf("after one answer, waiting = %d, want 1", got)
	}
}

func waitDone(t *testing.T, st *store.Store, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if x, _, _ := st.Session(id); x.Status == store.StatusDone {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s never finished", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestATurnFinishesBeforeItsMemoryIsSaved(t *testing.T) {
	gate := make(chan struct{})
	sb := &fakeSandbox{memoryGate: gate, outFiles: map[string]string{"/workspace/.unire-out/summary.md": "## Asked\nAdd login"}}
	m, st := setup(t, sb)
	x, err := m.Start(memReq())
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, st, x.ID) // done while the memory step is still held back
	if !m.Saving(x.ID) {
		t.Error("the session should report that it is still saving memory")
	}
	if _, saved, _ := st.Summary(x.ID); saved {
		t.Error("the summary must not exist before the memory step finishes")
	}
	close(gate)
	m.Wait()
	if m.Saving(x.ID) {
		t.Error("saving should be over")
	}
	if s, saved, _ := st.Summary(x.ID); !saved || !strings.Contains(s.Summary, "Add login") {
		t.Errorf("the summary should be saved in the background: %+v %v", s, saved)
	}
}

func TestAReplyDoesNotWaitForTheEarlierTurnsMemory(t *testing.T) {
	gate := make(chan struct{})
	sb := &fakeSandbox{memoryGate: gate, outFiles: map[string]string{"/workspace/.unire-out/summary.md": "## Asked\nAdd login"}}
	m, st := setup(t, sb)
	first, _ := m.Start(memReq())
	waitDone(t, st, first.ID) // done, still saving its memory (held back)

	r := memReq()
	r.Task, r.Parent, r.Memory = first.Task, first.ID, false
	reply, err := m.Start(r)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, st, reply.ID) // the reply finishes while the first turn is still saving
	if !m.Saving(first.ID) {
		t.Error("the earlier turn should still be saving")
	}
	close(gate)
	m.Wait()
}

func TestAConversationKeepsOneWorkspace(t *testing.T) {
	m, st := setup(t, &fakeSandbox{})
	first, _ := m.Start(req(""))
	m.Wait()
	r := req("")
	r.Parent = first.ID
	second, _ := m.Start(r)
	m.Wait()
	r.Parent = second.ID
	third, _ := m.Start(r)
	m.Wait()
	if got := m.rootOf("brand-new", ""); got != "brand-new" {
		t.Errorf("a first message is its own root, got %s", got)
	}
	if got := m.rootOf("x", first.ID); got != first.ID {
		t.Errorf("a reply to the first message has it as root, got %s", got)
	}
	if got := m.rootOf(third.ID, second.ID); got != first.ID {
		t.Errorf("the third turn's root = %s, want the first session %s", got, first.ID)
	}
	if workspaceName(first.ID) != "unire-ws-"+first.ID {
		t.Error("the workspace is named after the first session")
	}
	_ = st
}

func TestTwoChatsOnTheSameRepoGetDifferentBranches(t *testing.T) {
	m, st := setup(t, &fakeSandbox{})
	r := req("")
	r.Task = "Fix the bug"
	a, _ := m.Start(r)
	b, _ := m.Start(r)
	c, _ := m.Start(r)
	m.Wait()
	got := []string{a.Branch, b.Branch, c.Branch}
	want := []string{"agent/claude/fix-the-bug", "agent/claude/fix-the-bug-2", "agent/claude/fix-the-bug-3"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("branches = %v, want %v", got, want)
			break
		}
	}
	// A reply carries on on its conversation's branch.
	reply := r
	reply.Parent, reply.Branch = b.ID, b.Branch
	x, err := m.Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if got, _, _ := st.Session(x.ID); got.Branch != b.Branch {
		t.Errorf("a reply's branch = %q, want its conversation's %q", got.Branch, b.Branch)
	}
}

func TestTheMemoryToolSearchesOnlyThisSessionsRepos(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := &Manager{st: st}
	mine := store.RepoRef{ServerID: "g", FullName: "me/api"}
	other := store.RepoRef{ServerID: "g", FullName: "me/secret-repo"}
	save := func(id, task, summary string, ref store.RepoRef) {
		if err := st.SaveSummary(store.Summary{SessionID: id, Task: task, Agent: "claude", Status: "done", Summary: summary, Source: "agent", Repos: []string{store.RepoKey(ref)}}); err != nil {
			t.Fatal(err)
		}
	}
	save("aaa111", "Add login", "## Gotchas\nThe login tests need REDIS_URL set; run make check first.", mine)
	save("bbb222", "Other work", "## Gotchas\nThe login of the other repo uses a hidden thing.", other)
	j := job{refs: []store.RepoRef{mine}}

	got := m.lookUpMemory(j, "login redis")
	if !strings.Contains(got, "aaa111") || !strings.Contains(got, "REDIS_URL") || !strings.Contains(got, "not instructions") {
		t.Errorf("search = %q", got)
	}
	if strings.Contains(got, "bbb222") || strings.Contains(got, "hidden thing") {
		t.Errorf("another repo's notes must not be given: %q", got)
	}
	if full := m.lookUpMemory(j, "aaa111"); !strings.Contains(full, "make check first") {
		t.Errorf("reading by id = %q", full)
	}
	if denied := m.lookUpMemory(j, "bbb222"); strings.Contains(denied, "hidden thing") {
		t.Errorf("an id from another repo must not be readable: %q", denied)
	}
	if none := m.lookUpMemory(j, "kubernetes"); !strings.Contains(none, "Nothing") {
		t.Errorf("no match = %q", none)
	}
	if usage := m.lookUpMemory(j, " "); !strings.Contains(usage, "usage") {
		t.Errorf("empty = %q", usage)
	}
}

// fakeFinder answers the repo tool with one repo.
type fakeFinder struct{ named bool }

func (f fakeFinder) Find(_ context.Context, query, ownerText string, have []store.RepoRef) (FoundRepo, string) {
	if query == "nothing" {
		return FoundRepo{}, "No repo matches."
	}
	return FoundRepo{Repo: Repo{Ref: store.RepoRef{ServerID: "g", FullName: "me/backend", CloneURL: "https://git/me/backend.git"}, Token: "tok"}, Named: f.named, Why: "usually used with me/api"}, ""
}

func TestTheRepoToolAsksTheOwnerUnlessTheirWordsNameTheRepo(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := store.RepoRef{ServerID: "g", FullName: "me/api", CloneURL: "https://git/me/api.git"}
	newJob := func(id string) (*Manager, *job) {
		f := fakeFinder{}
		m, err := New(st, func() Config { return Config{Repos: f} })
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CreateSession(store.Session{ID: id, Task: "t", Agent: "claude", Mode: "hands-off", Prompt: "work on the api", Branch: "b", Repos: []store.RepoRef{api}}); err != nil {
			t.Fatal(err)
		}
		return m, &job{spec: session.Spec{ID: id}, refs: []store.RepoRef{api}}
	}

	// Named by the owner's own words: no question.
	m, j := newJob("s1")
	m.config = func() Config { return Config{Repos: fakeFinder{named: true}} }
	got, msg := m.addRepoFor(context.Background(), j, "the backend")
	if got == nil || msg != "" || got.Dir != "backend" || got.Token != "tok" {
		t.Fatalf("named repo = %+v %q", got, msg)
	}
	if rec, _, _ := st.Session("s1"); len(rec.Repos) != 2 || rec.Repos[1].FullName != "me/backend" {
		t.Errorf("the repo is kept for replies: %+v", rec.Repos)
	}
	if got, msg := m.addRepoFor(context.Background(), j, "nothing"); got != nil || msg != "No repo matches." {
		t.Errorf("a problem goes to the agent: %+v %q", got, msg)
	}

	// Not named: the owner is asked, and "no" means no.
	for answer, want := range map[string]bool{"yes please": true, "no": false} {
		m, j := newJob("s-" + strings.ReplaceAll(answer, " ", "-"))
		done := make(chan struct{})
		var res *workflow.AddedRepo
		var text string
		go func() {
			res, text = m.addRepoFor(context.Background(), j, "the backend")
			close(done)
		}()
		var qid string
		for i := 0; i < 200 && qid == ""; i++ {
			time.Sleep(10 * time.Millisecond)
			events, _ := st.Events(j.spec.ID, 0, 100)
			for _, e := range events {
				if e.Kind == "ask" && strings.Contains(e.Text, "me/backend") {
					qid, _, _ = strings.Cut(e.Text, "\n")
				}
			}
		}
		if qid == "" {
			t.Fatal("the owner should be asked")
		}
		if err := m.Answer(j.spec.ID, qid, answer); err != nil {
			t.Fatal(err)
		}
		<-done
		if (res != nil) != want {
			t.Errorf("answer %q: added=%v (%q), want %v", answer, res != nil, text, want)
		}
	}
}

func waitFinished(t *testing.T, st *store.Store, id string) store.Session {
	t.Helper()
	for i := 0; i < 400; i++ {
		if s, _, _ := st.Session(id); store.Finished(s.Status) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the session never finished")
	return store.Session{}
}

func TestARunThatTakesTooLongIsStoppedAndSaysWhy(t *testing.T) {
	sb := &fakeSandbox{block: make(chan struct{})} // the agent works until stopped
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, err := New(st, func() Config { return Config{Runner: &fakeRunner{sb: sb}, MaxRun: 200 * time.Millisecond} })
	if err != nil {
		t.Fatal(err)
	}
	x, err := m.Start(req("tok"))
	if err != nil {
		t.Fatal(err)
	}
	got := waitFinished(t, st, x.ID)
	if got.Status != store.StatusFailed || !strings.Contains(got.Error, "stopped after") || !strings.Contains(got.Error, "Limits") {
		t.Errorf("status = %q, error = %q: a run over its time fails and says which limit", got.Status, got.Error)
	}
}

func TestStopFreesTheSlotEvenWhenTheRunWillNotEnd(t *testing.T) {
	sb := &fakeSandbox{stuck: make(chan struct{})}
	defer close(sb.stuck) // let the abandoned run end when the test is done
	m, st := setup(t, sb)
	m.abandon = 150 * time.Millisecond
	x, err := m.Start(req("tok"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if s, _, _ := st.Session(x.ID); s.Status == store.StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.Stop(x.ID)
	got := waitFinished(t, st, x.ID)
	if got.Status != store.StatusStopped {
		t.Errorf("status = %q, want stopped", got.Status)
	}
	if !strings.Contains(logText(st, x.ID), "giving up on it") {
		t.Errorf("the log says the run was given up on:\n%s", logText(st, x.ID))
	}
	// The slot is free: another session can run.
	sb2 := &fakeSandbox{}
	_ = sb2
	if len(m.sem) != 0 {
		t.Errorf("the slot is still held: %d", len(m.sem))
	}
}

func TestOldActivityIsPrunedButAnswersAndPushedBranchesStay(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := store.Session{ID: "old", Task: "t", Agent: "claude", Mode: "hands-off", Prompt: "p", Branch: "b"}
	fresh := store.Session{ID: "new", Task: "t", Agent: "claude", Mode: "hands-off", Prompt: "p", Branch: "b"}
	for _, s := range []store.Session{old, fresh} {
		if err := st.CreateSession(s); err != nil {
			t.Fatal(err)
		}
		st.AddEvent(s.ID, "log", "running the agent")
		st.AddEvent(s.ID, "log", "api: pushed b")
		st.AddEvent(s.ID, "out", "the answer")
		st.MarkFinished(s.ID, store.StatusDone, "")
	}
	// Make "old" finish long ago.
	n, err := st.PruneLogs(time.Now().Add(time.Hour)) // a cutoff after both finished
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v: only the progress lines go", n, err)
	}
	ev, _ := st.Events("old", 0, 100)
	var texts []string
	for _, e := range ev {
		texts = append(texts, e.Kind+":"+e.Text)
	}
	got := strings.Join(texts, "|")
	if strings.Contains(got, "running the agent") || !strings.Contains(got, "api: pushed b") || !strings.Contains(got, "out:the answer") {
		t.Errorf("events after pruning: %s", got)
	}
	if n, _ := st.PruneLogs(time.Now().Add(-time.Hour)); n != 0 {
		t.Errorf("a cutoff before they finished prunes nothing, pruned %d", n)
	}
	roots, _ := st.IdleConversations(time.Now().Add(time.Hour))
	if len(roots) != 2 {
		t.Errorf("both conversations are idle: %v", roots)
	}
	if roots, _ := st.IdleConversations(time.Now().Add(-time.Hour)); len(roots) != 0 {
		t.Errorf("none has been idle that long: %v", roots)
	}
}

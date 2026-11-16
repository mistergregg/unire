package sessions

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/store"
)

// fakeSandbox behaves like a sandbox where the agent prints a line that
// contains the git token (to prove it is scrubbed) and optionally blocks.
type fakeSandbox struct {
	agentOutput string
	agentErr    error
	block       chan struct{} // if set, the agent waits on it or on ctx
}

func (f *fakeSandbox) Exec(ctx context.Context, o runner.ExecOpts) error {
	if len(o.Argv) > 0 && o.Argv[0] == "git" {
		for _, a := range o.Argv {
			if a == "rev-parse" {
				io.WriteString(o.Stdout, "abc\n")
			}
		}
		return nil
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
	return Request{Task: "demo", Agent: "claude", Prompt: "go", Mode: "hands-off", Repos: []Repo{
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

func TestStartNeedsRepos(t *testing.T) {
	m, _ := setup(t, &fakeSandbox{})
	if _, err := m.Start(Request{Task: "t", Agent: "claude", Prompt: "p", Mode: "hands-off"}); err == nil {
		t.Error("no repos should fail")
	}
}

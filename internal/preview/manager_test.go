package preview

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeEngine struct {
	mu        sync.Mutex
	have      bool
	pullErr   error
	runErr    error
	running   bool
	logs      string
	pulled    []string
	ran       []RunSpec
	removed   []string
	pullBlock chan struct{} // if set, Pull waits for it or for ctx
}

func (f *fakeEngine) HaveImage(context.Context, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.have
}
func (f *fakeEngine) Pull(ctx context.Context, image string) error {
	f.mu.Lock()
	f.pulled = append(f.pulled, image)
	block, err := f.pullBlock, f.pullErr
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func (f *fakeEngine) Run(_ context.Context, s RunSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, s)
	if f.runErr == nil {
		f.running = true
	}
	return f.runErr
}
func (f *fakeEngine) Running(context.Context, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}
func (f *fakeEngine) Logs(context.Context, string, int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logs
}
func (f *fakeEngine) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	return nil
}
func (f *fakeEngine) set(fn func(*fakeEngine)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

func newMgr(e *fakeEngine, code int, err error) *Manager {
	return &Manager{
		Engine:       e,
		HTTPGet:      func(context.Context, string) (int, error) { return code, err },
		PollEvery:    time.Millisecond,
		ReadyTimeout: 2 * time.Second,
	}
}

func waitState(t *testing.T, m *Manager, key string, want State) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := m.Status(key); ok && st.State == want {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	st, _ := m.Status(key)
	t.Fatalf("never reached %s; last status %+v", want, st)
	return st
}

var flutter = Preview{Name: "app", Dir: "app", Recipe: Catalog["flutter"]}
var opts = StartOpts{Network: "unire-net", Workspace: "ws-1", RepoDir: "myrepo"}

func TestReadyWhenAppAnswers(t *testing.T) {
	e := &fakeEngine{have: true}
	m := newMgr(e, 200, nil)
	m.Start("s1.app", flutter, opts)
	st := waitState(t, m, "s1.app", StateReady)
	if st.Target != "unire-preview-s1.app:8080" {
		t.Fatalf("target %q", st.Target)
	}
	if len(e.pulled) != 0 {
		t.Fatal("an image already on the host must not be pulled")
	}
	spec := e.ran[0]
	if spec.WorkDir != "/workspace/myrepo/app" || spec.User != "1000:1000" || spec.Workspace != "ws-1" || spec.Network != "unire-net" {
		t.Fatalf("spec %+v", spec)
	}
}

func TestDownloadsMissingImageFirst(t *testing.T) {
	block := make(chan struct{})
	e := &fakeEngine{pullBlock: block}
	m := newMgr(e, 200, nil)
	m.Start("s1.app", flutter, opts)
	st := waitState(t, m, "s1.app", StateDownloading)
	if !strings.Contains(st.Message, Catalog["flutter"].Image) {
		t.Fatalf("message %q should name the image", st.Message)
	}
	close(block)
	waitState(t, m, "s1.app", StateReady)
	if len(e.pulled) != 1 {
		t.Fatalf("pulled %v", e.pulled)
	}
}

func TestPullFailureIsReported(t *testing.T) {
	e := &fakeEngine{pullErr: errors.New("no route to host")}
	m := newMgr(e, 200, nil)
	m.Start("s1.app", flutter, opts)
	st := waitState(t, m, "s1.app", StateFailed)
	if !strings.Contains(st.Message, "no route to host") || len(e.ran) != 0 {
		t.Fatalf("status %+v, ran %d", st, len(e.ran))
	}
}

func TestRunFailureIsReported(t *testing.T) {
	e := &fakeEngine{have: true, runErr: errors.New("port is already allocated")}
	m := newMgr(e, 200, nil)
	m.Start("s1.app", flutter, opts)
	st := waitState(t, m, "s1.app", StateFailed)
	if !strings.Contains(st.Message, "port is already allocated") {
		t.Fatalf("status %+v", st)
	}
}

func TestContainerThatDiesFailsWithLogs(t *testing.T) {
	e := &fakeEngine{have: true, logs: "Error: pubspec.yaml not found"}
	m := newMgr(e, 0, errors.New("connection refused"))
	m.Start("s1.app", flutter, opts)
	waitState(t, m, "s1.app", StateStarting)
	e.set(func(f *fakeEngine) { f.running = false })
	st := waitState(t, m, "s1.app", StateFailed)
	if !strings.Contains(st.Logs, "pubspec.yaml not found") || !slices.Contains(e.removed, "unire-preview-s1.app") {
		t.Fatalf("status %+v removed %v", st, e.removed)
	}
}

func TestNeverAnsweringTimesOut(t *testing.T) {
	e := &fakeEngine{have: true}
	m := newMgr(e, 0, errors.New("connection refused"))
	m.ReadyTimeout = 30 * time.Millisecond
	m.Start("s1.app", flutter, opts)
	st := waitState(t, m, "s1.app", StateFailed)
	if !strings.Contains(st.Message, "did not start answering") {
		t.Fatalf("status %+v", st)
	}
}

func TestServerErrorIsNotReady(t *testing.T) {
	e := &fakeEngine{have: true}
	m := newMgr(e, 502, nil)
	m.ReadyTimeout = 30 * time.Millisecond
	m.Start("s1.app", flutter, opts)
	waitState(t, m, "s1.app", StateFailed)
}

func TestRedirectCountsAsReady(t *testing.T) {
	e := &fakeEngine{have: true}
	m := newMgr(e, 302, nil)
	m.Start("s1.app", flutter, opts)
	waitState(t, m, "s1.app", StateReady)
}

func TestLogReadiness(t *testing.T) {
	p := flutter
	p.Ready = Ready{LogContains: "is being served"}
	e := &fakeEngine{have: true}
	m := newMgr(e, 0, errors.New("never used"))
	m.Start("s1.app", p, opts)
	waitState(t, m, "s1.app", StateStarting)
	e.set(func(f *fakeEngine) { f.logs = "lib/main.dart is being served at 8080" })
	waitState(t, m, "s1.app", StateReady)
}

func TestStartTwiceDoesNotRunTwice(t *testing.T) {
	e := &fakeEngine{have: true}
	m := newMgr(e, 200, nil)
	m.Start("s1.app", flutter, opts)
	waitState(t, m, "s1.app", StateReady)
	m.Start("s1.app", flutter, opts)
	time.Sleep(20 * time.Millisecond)
	if len(e.ran) != 1 {
		t.Fatalf("ran %d times", len(e.ran))
	}
}

func TestStopWhileDownloadingStaysStopped(t *testing.T) {
	e := &fakeEngine{pullBlock: make(chan struct{})}
	m := newMgr(e, 200, nil)
	m.Start("s1.app", flutter, opts)
	waitState(t, m, "s1.app", StateDownloading)
	if err := m.Stop(context.Background(), "s1.app"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if st, _ := m.Status("s1.app"); st.State != StateStopped {
		t.Fatalf("status %+v: a stopped preview must not be revived or marked failed", st)
	}
	if len(e.ran) != 0 {
		t.Fatal("must not start a container after Stop")
	}
}

func TestStopRemovesContainerAndAllowsRestart(t *testing.T) {
	e := &fakeEngine{have: true}
	m := newMgr(e, 200, nil)
	m.Start("s1.app", flutter, opts)
	waitState(t, m, "s1.app", StateReady)
	if err := m.Stop(context.Background(), "s1.app"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.removed, "unire-preview-s1.app") {
		t.Fatalf("removed %v", e.removed)
	}
	m.Start("s1.app", flutter, opts)
	waitState(t, m, "s1.app", StateReady)
	if len(e.ran) != 2 {
		t.Fatalf("ran %d times", len(e.ran))
	}
}

func TestStopUnknownIsFine(t *testing.T) {
	m := newMgr(&fakeEngine{}, 200, nil)
	if err := m.Stop(context.Background(), "nope"); err != nil {
		t.Fatal(err)
	}
}

func TestContainerNameIsSafe(t *testing.T) {
	if got := ContainerName("s 1/../x;rm"); strings.ContainsAny(got, " /;") {
		t.Fatalf("unsafe name %q", got)
	}
}

func TestRunArgsKeepTheSandboxRules(t *testing.T) {
	args := runArgs(RunSpec{
		Name: "n", Image: "img", Network: "net", Workspace: "ws", WorkDir: "/workspace/r",
		User: "1000:1000", Command: []string{"flutter", "run"}, Memory: "2g", CPUs: "2",
		Env: map[string]string{"B": "2", "A": "1"},
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--cap-drop NET_RAW", "--security-opt no-new-privileges", "--network net", "-v ws:/workspace", "-w /workspace/r", "--user 1000:1000", "-e A=1 -e B=2", "--memory 2g", "--cpus 2", "img flutter run"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for _, bad := range []string{"docker.sock", "/home/node", "--privileged", "--volumes-from"} {
		if strings.Contains(joined, bad) {
			t.Errorf("a preview must not get %q: %s", bad, joined)
		}
	}
	if slices.Index(args, "img") < slices.Index(args, "--cpus") {
		t.Error("the image must come after all docker options")
	}
}

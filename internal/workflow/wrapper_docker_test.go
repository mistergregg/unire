package workflow

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/runner"
)

// These run the real wrapper (agentWrapper) in a real sandbox, because what
// they check is how docker exec behaves: it does not return while its input is
// open, even after the process in it has ended. They need Docker and the
// unire-sandbox image, so they run only with UNIRE_DOCKER_TESTS=1.

// output collects what a process prints and calls onLine for each line.
type output struct {
	mu     sync.Mutex
	buf    strings.Builder
	onLine func(line string)
	part   string
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	o.buf.Write(p)
	o.part += string(p)
	var lines []string
	for {
		i := strings.IndexByte(o.part, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, o.part[:i])
		o.part = o.part[i+1:]
	}
	cb := o.onLine
	o.mu.Unlock()
	if cb != nil {
		for _, l := range lines {
			cb(l)
		}
	}
	return len(p), nil
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func dockerWorker(t *testing.T) (*worker, func()) {
	t.Helper()
	if os.Getenv("UNIRE_DOCKER_TESTS") != "1" {
		t.Skip("set UNIRE_DOCKER_TESTS=1 (needs Docker and the unire-sandbox image)")
	}
	d := runner.Docker{Image: "unire-sandbox:latest", CredsVolume: "unire-wrapper-test-creds"}
	sb, err := d.Start(context.Background(), runner.StartSpec{SessionID: "wrappertest"})
	if err != nil {
		t.Fatal(err)
	}
	return &worker{sb: sb, opt: Options{Out: io.Discard, Err: io.Discard}}, func() {
		sb.Stop(context.Background())
		exec.Command("docker", "volume", "rm", "-f", "unire-wrapper-test-creds").Run()
	}
}

// runWrapped runs argv under the wrapper with an input that is closed only when
// the exit marker is seen (as runLiveProcess does) and reports what it printed.
func runWrapped(t *testing.T, w *worker, argv []string, before func(in *io.PipeWriter, out *output)) (string, time.Duration) {
	t.Helper()
	pr, pw := io.Pipe()
	out := &output{}
	var once sync.Once
	out.onLine = func(l string) {
		if strings.HasPrefix(l, agent.ExitMarker) {
			once.Do(func() { pw.Close() })
		}
	}
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- w.sb.Exec(context.Background(), runner.ExecOpts{Argv: agentWrapper(argv), Stdin: pr, Stdout: out, Stderr: io.Discard})
		pr.Close()
	}()
	if before != nil {
		go before(pw, out)
	}
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		t.Fatalf("docker exec did not return; it printed %q", out.String())
	}
	return out.String(), time.Since(start)
}

func TestWrapperPassesInputOnAndAnnouncesTheAgentEnding(t *testing.T) {
	w, cleanup := dockerWorker(t)
	defer cleanup()
	// `cat` is an agent that echoes its input and leaves when the input closes.
	// Here the wrapper's input is closed only when the exit marker is seen, which
	// cannot happen before cat ends, so something has to end cat: the test writes
	// a line, then closes the input itself.
	pr, pw := io.Pipe()
	out := &output{}
	done := make(chan error, 1)
	go func() {
		done <- w.sb.Exec(context.Background(), runner.ExecOpts{Argv: agentWrapper([]string{"cat"}), Stdin: pr, Stdout: out, Stderr: io.Discard})
		pr.Close()
	}()
	io.WriteString(pw, "ping\n")
	time.Sleep(1500 * time.Millisecond)
	pw.Close()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("did not return; printed %q", out.String())
	}
	got := out.String()
	if !strings.Contains(got, "ping\n") {
		t.Errorf("input did not reach the agent started in the background: %q", got)
	}
	if !strings.Contains(got, agent.ExitMarker+"0") {
		t.Errorf("the wrapper should say the agent ended with status 0: %q", got)
	}
}

// An agent that exits by itself, with its input still open, must not leave the
// run waiting: the marker lets the caller close the input.
func TestAnAgentThatExitsByItselfDoesNotLeaveDockerExecWaiting(t *testing.T) {
	w, cleanup := dockerWorker(t)
	defer cleanup()
	got, took := runWrapped(t, w, []string{"sh", "-c", "echo working; exit 3"}, nil)
	if !strings.Contains(got, "working") || !strings.Contains(got, agent.ExitMarker+"3") {
		t.Errorf("output = %q, want the agent's output and its exit status", got)
	}
	t.Logf("returned after %v", took.Round(100*time.Millisecond))
}

// The agent starts helpers that keep its output open. Ending only the agent
// leaves docker exec waiting; treeKill ends them all.
func TestTreeKillEndsAnAgentWithHelpersInARealSandbox(t *testing.T) {
	w, cleanup := dockerWorker(t)
	defer cleanup()
	got, took := runWrapped(t, w, []string{"sh", "-c", "sleep 300 & sleep 300 & echo started; wait"}, func(in *io.PipeWriter, out *output) {
		for i := 0; i < 50 && !strings.Contains(out.String(), "started"); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		time.Sleep(500 * time.Millisecond)
		w.signalAgent(context.Background(), "TERM")
	})
	if !strings.Contains(got, agent.ExitMarker) {
		t.Errorf("output = %q: the agent was not ended", got)
	}
	t.Logf("returned after %v", took.Round(100*time.Millisecond))
}

// The whole path, with the real programs (signed out): the real run loop, the
// real wrapper, a real sandbox and the real Claude Code, Antigravity and Codex.
// Each must be given its first message, report its sign-in error, and end by
// itself, with the run coming back.
func TestRealAgentsThroughTheRealRunLoop(t *testing.T) {
	w, cleanup := dockerWorker(t)
	defer cleanup()
	for _, id := range []string{"claude", "antigravity", "codex"} {
		t.Run(id, func(t *testing.T) {
			a, err := agent.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			argv, drv, err := a.(agent.Live).StartLive(agent.LiveOptions{Mode: agent.HandsOff, Dir: "/workspace"})
			if err != nil {
				t.Fatal(err)
			}
			var log strings.Builder
			w.opt.Err = &log
			var answers strings.Builder
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			start := time.Now()
			pending, _, err := w.runLiveProcess(ctx, argv, drv, "say hi", &answers, nil, false)
			t.Logf("%s: returned after %v; pending=%d err=%v; log=%q answers=%q", id, time.Since(start).Round(100*time.Millisecond), len(pending), err, strings.TrimSpace(log.String()), strings.TrimSpace(answers.String()))
			if ctx.Err() != nil {
				t.Fatalf("%s: the run loop did not come back in time", id)
			}
			if !drv.Done() {
				t.Errorf("%s: the driver never saw the agent finish", id)
			}
		})
	}
}

// scriptLive plays an agent with a shell script, with Antigravity's driver (so
// its protocol is the real one) and a real sandbox.
type scriptLive struct {
	script string
	real   agent.Live
}

func (l scriptLive) StartLive(o agent.LiveOptions) ([]string, agent.LiveDriver, error) {
	_, drv, err := l.real.StartLive(o)
	return []string{"sh", "-c", l.script, "sh"}, drv, err
}

// An agent like the one that froze twice: it runs a helper that keeps its output
// open, takes SIGINT by ending its turn but staying alive, and has to be ended
// and started again for a message sent now. Everything is real except the agent.
func TestSendNowInARealSandboxWithAnAgentThatKeepsHelpersAlive(t *testing.T) {
	w, cleanup := dockerWorker(t)
	defer cleanup()
	oldGrace, oldStop := interruptGrace, stopGrace
	interruptGrace, stopGrace = 3*time.Second, 2*time.Second
	defer func() { interruptGrace, stopGrace = oldGrace, oldStop }()

	script := `trap 'printf "%s\n" "{\"event\":\"result\",\"result\":{\"conversation_id\":\"conv-1\",\"status\":\"INTERRUPTED\",\"error\":\"interrupted\",\"response\":\"\"}}"; intr=1' INT
sleep 300 &
while IFS= read -r line; do
  printf '%s\n' "$line" >> /tmp/received.txt
  printf '%s\n' '{"event":"init","conversation_id":"conv-1","init":{}}'
  case "$line" in
    *RUNLONG*) intr=0; echo working; while [ "$intr" = 0 ]; do sleep 0.2; done ;;
    *) printf '%s\n' '{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"ok"}}' ;;
  esac
done`
	real, _ := agent.Get("antigravity")
	live := scriptLive{script: script, real: real.(agent.Live)}

	ctl := make(chan agent.Steer, 4)
	st := &states{}
	answers := &output{}
	var log output
	w.opt.Err, w.opt.Control, w.opt.OnMessage = &log, ctl, st.add
	spec := baseSpec()
	spec.Agent = "antigravity"

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- w.runLive(context.Background(), live, spec, "RUNLONG start", "", answers) }()
	for i := 0; i < 100 && !strings.Contains(answers.String(), "working"); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	ctl <- agent.Steer{ID: "m1", Text: "first thought"}
	ctl <- agent.Steer{ID: "m2", Text: "change course", Now: true}
	select {
	case err := <-done:
		t.Logf("returned after %v, err=%v", time.Since(start).Round(100*time.Millisecond), err)
	case <-time.After(60 * time.Second):
		t.Fatalf("the run froze; log: %s", log.String())
	}
	var recv output
	if err := w.sb.Exec(context.Background(), runner.ExecOpts{Argv: []string{"cat", "/tmp/received.txt"}, Stdout: &recv}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(recv.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "RUNLONG start") || !strings.Contains(lines[1], "first thought") || !strings.Contains(lines[1], "change course") {
		t.Errorf("the agent received %q: the task, then both messages together", lines)
	}
	for _, id := range []string{"m1", "m2"} {
		if got := st.of(id); len(got) == 0 || got[len(got)-1] != agent.MsgDelivered {
			t.Errorf("%s should end up delivered: %v", id, got)
		}
	}
	t.Logf("log:\n%s", strings.TrimSpace(log.String()))
}

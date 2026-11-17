package agent

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRealCLIs drives the real agent programs, signed out, with the live
// drivers: each must understand the first message well enough to answer (with
// an error, as nobody is signed in), and the run must end by itself once the
// driver says it is done. It runs only where the CLIs are installed and
// UNIRE_REAL_CLIS=1 is set (the sandbox image has them); see docs/tools.md.
func TestRealCLIs(t *testing.T) {
	if os.Getenv("UNIRE_REAL_CLIS") != "1" {
		t.Skip("set UNIRE_REAL_CLIS=1 to run against the real CLIs")
	}
	for _, id := range []string{"claude", "antigravity", "codex"} {
		t.Run(id, func(t *testing.T) {
			a, _ := Get(id)
			bin := map[string]string{"claude": "claude", "antigravity": "agy", "codex": "codex"}[id]
			if _, err := exec.LookPath(bin); err != nil {
				t.Skipf("%s is not installed", bin)
			}
			argv, d, err := a.(Live).StartLive(LiveOptions{Mode: HandsOff, Dir: os.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Dir = os.TempDir()
			stdin, _ := cmd.StdinPipe()
			stdout, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			closeIn := func() { once.Do(func() { stdin.Close() }) }

			var mu sync.Mutex
			var progress []string
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				sc := bufio.NewScanner(stdout)
				sc.Buffer(make([]byte, 1<<20), 1<<20)
				for sc.Scan() {
					ev := d.Line(sc.Text())
					mu.Lock()
					if ev.Progress != "" {
						progress = append(progress, ev.Progress)
					}
					if !ev.Known {
						progress = append(progress, "RAW: "+strings.TrimSpace(sc.Text()))
					}
					mu.Unlock()
					if d.Done() {
						closeIn()
						// Give it a moment to leave; the workflow does the same.
						time.AfterFunc(10*time.Second, func() { cmd.Process.Kill() })
					}
				}
			}()
			go func() {
				if err := d.Begin(stdin, "say hi"); err != nil {
					t.Logf("begin: %v", err)
				}
			}()
			select {
			case <-finished:
			case <-time.After(60 * time.Second):
				cmd.Process.Kill()
				mu.Lock()
				t.Fatalf("%s did not finish by itself; progress so far: %v", id, progress)
			}
			_ = cmd.Wait()
			mu.Lock()
			defer mu.Unlock()
			t.Logf("%s ended; done=%v; progress: %v", id, d.Done(), progress)
			if !d.Done() {
				t.Errorf("%s ended but the driver never saw it finish", id)
			}
		})
	}
}

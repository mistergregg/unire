package workflow

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// The agent starts helper processes that keep its output open, so docker exec
// does not return until they are gone. treeKill must end all of them, not just
// the agent. This runs real processes, so it needs Linux (and /proc).
func TestTreeKillEndsTheAgentAndItsHelpers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux and /proc")
	}
	for _, sig := range []string{"TERM", "KILL"} {
		t.Run(sig, func(t *testing.T) {
			// An "agent" (its pid in the pid file) with two helpers that outlive it.
			cmd := exec.Command("sh", "-c", `echo $$ > `+agentPidFile+`; exec sh -c "sleep 300 & sleep 300 & wait"`)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			time.Sleep(400 * time.Millisecond) // let the helpers start
			if b, err := os.ReadFile(agentPidFile); err != nil || len(b) == 0 {
				t.Fatalf("no pid file: %v", err)
			}
			if err := exec.Command("sh", "-c", treeKill(sig)).Run(); err != nil {
				t.Fatal(err)
			}
			// The agent's output closes only when every process that held it is gone.
			closed := make(chan struct{})
			go func() {
				sc := bufio.NewScanner(out)
				for sc.Scan() {
				}
				close(closed)
			}()
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("the agent's helpers are still holding its output open: docker exec would never return")
			}
			_ = cmd.Wait()
		})
	}
}

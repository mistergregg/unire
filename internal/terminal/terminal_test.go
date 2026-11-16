package terminal

import (
	"bytes"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These tests use a real pseudo-terminal, which exists on Linux and macOS (CI
// runs them on Linux). The dashboard's own container is Linux.
func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pseudo-terminals are not available on Windows")
	}
}

func readUntil(t *testing.T, r io.Reader, want string) string {
	t.Helper()
	var got bytes.Buffer
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := r.Read(buf)
			got.Write(buf[:n])
			if strings.Contains(got.String(), want) || err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %q, got %q", want, got.String())
	}
	return got.String()
}

func TestKeystrokesReachTheCommandAndOutputComesBack(t *testing.T) {
	skipOnWindows(t)
	p, err := StartCmd(exec.Command("cat"), 80, 24, nil) // cat echoes what it is given
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, p, "hello")
}

func TestResizeIsPassedToTheTerminal(t *testing.T) {
	skipOnWindows(t)
	p, err := StartCmd(exec.Command("sh", "-c", "sleep 0.3; stty size; sleep 2"), 80, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Resize(132, 43); err != nil {
		t.Fatal(err)
	}
	readUntil(t, p, "43 132") // stty prints "rows cols"
	if err := p.Resize(0, 5); err == nil {
		t.Error("a zero size must be refused")
	}
}

func TestCloseStopsTheCommandReturnsQuicklyAndRunsCleanupOnce(t *testing.T) {
	skipOnWindows(t)
	var cleanups atomic.Int32
	cleaned := make(chan struct{}, 2)
	p, err := StartCmd(exec.Command("sleep", "60"), 80, 24, func() {
		time.Sleep(300 * time.Millisecond) // slow cleanup, like removing a container
		cleanups.Add(1)
		cleaned <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	p.Close()
	p.Close() // twice is fine
	if time.Since(start) > 200*time.Millisecond {
		t.Errorf("Close took %v; slow cleanup must not hold up the caller", time.Since(start))
	}
	select {
	case <-cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup never ran")
	}
	time.Sleep(100 * time.Millisecond)
	if n := cleanups.Load(); n != 1 {
		t.Errorf("cleanup ran %d times, want 1", n)
	}
}

func TestProcessExitEndsReads(t *testing.T) {
	skipOnWindows(t)
	p, err := StartCmd(exec.Command("sh", "-c", "echo done"), 80, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	out := readUntil(t, p, "done")
	if !strings.Contains(out, "done") {
		t.Errorf("output = %q", out)
	}
	buf := make([]byte, 64)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := p.Read(buf); err != nil {
			return // the terminal reports the end, which is how the dashboard knows to close
		}
	}
	t.Error("reads should fail once the command has exited")
}

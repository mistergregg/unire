// Package terminal runs an interactive command on a pseudo-terminal, so the
// dashboard can show it in the browser. It is used to sign agents in with their
// own login commands.
package terminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	"github.com/mistergregg/unire/internal/safe"
)

// Process is a running interactive command.
type Process interface {
	io.ReadWriteCloser
	// Resize tells the command the terminal's new size.
	Resize(cols, rows uint16) error
	// Wait blocks until the command exits.
	Wait() error
}

// Starter starts a command on a terminal of the given size.
type Starter func(argv []string, cols, rows uint16) (Process, error)

type ptyProcess struct {
	cmd     *exec.Cmd
	f       *os.File
	once    sync.Once
	cleanup func()
}

// StartCmd runs cmd on a pseudo-terminal. cleanup (may be nil) runs once when
// the process is closed, for example to remove a container the command made.
func StartCmd(cmd *exec.Cmd, cols, rows uint16, cleanup func()) (Process, error) {
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return &ptyProcess{cmd: cmd, f: f, cleanup: cleanup}, nil
}

func (p *ptyProcess) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *ptyProcess) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *ptyProcess) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return errors.New("bad size")
	}
	return pty.Setsize(p.f, &pty.Winsize{Cols: cols, Rows: rows})
}

// Wait blocks until the command exits. Close reaps the process itself, so use
// one or the other, not both.
func (p *ptyProcess) Wait() error { return p.cmd.Wait() }

// Close ends the command and releases the terminal. It returns quickly: the
// cleanup (which may take seconds, such as removing a container) runs in the
// background, so a new terminal can open straight away. Safe to call twice.
func (p *ptyProcess) Close() error {
	p.once.Do(func() {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		_ = p.f.Close()
		go func() {
			defer safe.Recover("terminal", nil)
			_ = p.cmd.Wait() // reap the process
			if p.cleanup != nil {
				p.cleanup()
			}
		}()
	})
	return nil
}

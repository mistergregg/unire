package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/session"
)

// agentPidFile holds the process id of the agent inside the sandbox, so it can
// be signalled (docker exec's own process does not pass signals on).
const agentPidFile = "/tmp/unire-agent.pid"

// How long an agent gets to leave after its input is closed, or after it is
// asked to stop, before it is stopped harder.
const (
	exitGrace = 10 * time.Second
	stopGrace = 4 * time.Second
)

// runLive runs a steerable agent. The agent is a process that stays open and
// reads messages on its standard input: the first is the task, and while it
// works the owner can add more (opt.Control). When the agent has finished
// everything it was given, its input is closed and it leaves.
//
// An agent that can only be redirected by starting it again (see
// agent.ErrRestart) is stopped and started from its saved conversation, with the
// owner's message as the new prompt.
func (w *worker) runLive(ctx context.Context, live agent.Live, spec session.Spec, prompt, ref string, tailOut io.Writer) error {
	answers := io.MultiWriter(w.opt.Out, tailOut)
	var resume, restartID string
	for {
		argv, drv, err := live.StartLive(agent.LiveOptions{Prompt: prompt, Mode: spec.Mode, Ref: ref, Resume: resume, Dir: workspace})
		if err != nil {
			return err
		}
		err = w.runLiveProcess(ctx, argv, drv, prompt, answers, restartID)
		if !errors.Is(err, agent.ErrRestart) {
			return err
		}
		s, _ := drv.Pending()
		resume, prompt, restartID = drv.Resume(), s.Text, s.ID
		if resume == "" {
			// It never reported a conversation, so there is nothing to carry on from.
			fmt.Fprintln(w.opt.Err, "the agent had not started a conversation yet: starting it again with your message")
		} else {
			fmt.Fprintln(w.opt.Err, "stopped the agent to follow your message")
		}
	}
}

func (w *worker) runLiveProcess(ctx context.Context, argv []string, drv agent.LiveDriver, prompt string, answers io.Writer, deliverID string) error {
	pr, pw := io.Pipe()
	var closeOnce sync.Once
	closeInput := func() { closeOnce.Do(func() { _ = pw.Close() }) }

	var exited = make(chan struct{})
	sw := &streamWriter{s: liveParser{drv}, out: answers, log: w.opt.Err, immediate: true, onMessage: w.opt.OnMessage}
	var timer *time.Timer
	var timerMu sync.Mutex
	sw.afterLine = func() {
		if drv.Done() {
			closeInput()
			timerMu.Lock()
			if timer == nil {
				timer = time.AfterFunc(exitGrace, func() { w.signalAgent(context.Background(), "TERM") })
			}
			timerMu.Unlock()
		}
	}

	var stderr tail
	wrapped := append([]string{"sh", "-c", `echo $$ > ` + agentPidFile + `; exec "$@"`, "sh"}, argv...)
	var execErr error
	go func() {
		defer close(exited)
		execErr = w.sb.Exec(ctx, runner.ExecOpts{Argv: wrapped, Dir: workspace, Stdin: pr, Stdout: sw, Stderr: &stderr})
		_ = pr.Close() // anything still writing to the agent's input stops waiting
	}()

	// The first message is written in the background: it waits until the agent
	// reads its input. Messages from the owner are taken once it has gone in.
	begun := make(chan struct{})
	go func() {
		defer close(begun)
		if err := drv.Begin(pw, prompt); err != nil {
			closeInput()
		} else if deliverID != "" && w.opt.OnMessage != nil {
			w.opt.OnMessage(deliverID, agent.MsgDelivered)
		}
	}()

	var control <-chan agent.Steer // nil (never ready) until the first message is in
	restart := false
	for loop := true; loop; {
		select {
		case <-begun:
			control = w.opt.Control
			begun = nil
		case <-exited:
			loop = false
		case <-ctx.Done():
			<-exited
			loop = false
		case s := <-control:
			err := drv.Send(s)
			switch {
			case errors.Is(err, agent.ErrRestart):
				restart = true
				w.signalAgent(ctx, "INT")
				select {
				case <-exited:
				case <-time.After(stopGrace):
					w.signalAgent(context.Background(), "TERM")
					<-exited
				}
				loop = false
			case err != nil:
				if w.opt.OnMessage != nil {
					w.opt.OnMessage(s.ID, agent.MsgFailed)
				}
				fmt.Fprintf(w.opt.Err, "the agent could not take the message: %v\n", err)
			}
		}
	}
	closeInput()
	timerMu.Lock()
	if timer != nil {
		timer.Stop()
	}
	timerMu.Unlock()
	sw.Close()

	if restart {
		return agent.ErrRestart
	}
	if execErr != nil && ctx.Err() == nil {
		if t := lastLines(stderr.buf, 4); t != "" {
			fmt.Fprintln(w.opt.Err, t)
		}
	}
	return execErr
}

// signalAgent sends a signal to the agent process inside the sandbox.
func (w *worker) signalAgent(ctx context.Context, sig string) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = w.sb.Exec(cctx, runner.ExecOpts{Argv: []string{"sh", "-c", "kill -" + sig + " $(cat " + agentPidFile + ") 2>/dev/null; true"}})
}

// lastLines returns the last n non-empty lines of what the agent wrote to stderr.
func lastLines(b []byte, n int) string {
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

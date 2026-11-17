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

// Timings. After its input is closed an agent gets exitGrace to leave. After an
// interrupt it gets interruptGrace to end its turn and leave, and then it is
// stopped; either way it is started again with the messages that waited.
var (
	exitGrace      = 10 * time.Second
	interruptGrace = 5 * time.Second
	stopGrace      = 3 * time.Second // between asking a process to end, killing it, and giving up on it
)

// A process that dies with messages the agent never got is started again, from
// its saved conversation, at most this many times in a run.
const maxRestarts = 3

// runLive runs a steerable agent. The agent is a process that stays open and
// reads messages on its standard input: the first is the task, and while it
// works the owner can add more (opt.Control). When the agent has finished
// everything it was given, its input is closed and it leaves.
//
// If the process ends while messages are still waiting for it (it was stopped,
// or it died), it is started again from its saved conversation with those
// messages, all at once, as the new prompt.
func (w *worker) runLive(ctx context.Context, live agent.Live, spec session.Spec, prompt, ref string, tailOut io.Writer) error {
	answers := io.MultiWriter(w.opt.Out, tailOut)
	var resume string
	var deliver []string // ids of messages the new prompt carries
	for restarts := 0; ; restarts++ {
		argv, drv, err := live.StartLive(agent.LiveOptions{Prompt: prompt, Mode: spec.Mode, Ref: ref, Resume: resume, Dir: workspace})
		if err != nil {
			return err
		}
		pending, err := w.runLiveProcess(ctx, argv, drv, prompt, answers, deliver)
		if len(pending) == 0 || ctx.Err() != nil || restarts >= maxRestarts {
			if w.opt.OnMessage != nil {
				for _, s := range pending {
					w.opt.OnMessage(s.ID, agent.MsgFailed)
				}
			}
			return err
		}
		resume, prompt = drv.Resume(), agent.JoinSteers(pending)
		deliver = nil
		for _, s := range pending {
			deliver = append(deliver, s.ID)
		}
		if resume == "" {
			fmt.Fprintln(w.opt.Err, "the agent stopped before it had a conversation: starting it again with your message")
		} else {
			fmt.Fprintln(w.opt.Err, "the agent stopped: carrying on its conversation with your message")
		}
	}
}

// runLiveProcess runs one process until it ends. It returns the messages the
// agent was never given, so the caller can start it again with them.
func (w *worker) runLiveProcess(ctx context.Context, argv []string, drv agent.LiveDriver, prompt string, answers io.Writer, deliver []string) ([]agent.Steer, error) {
	pr, pw := io.Pipe()
	var closeOnce sync.Once
	closeInput := func() { closeOnce.Do(func() { _ = pw.Close() }) }

	exited := make(chan struct{})
	var timerMu sync.Mutex
	var timers []*time.Timer
	after := func(d time.Duration, f func()) {
		timerMu.Lock()
		timers = append(timers, time.AfterFunc(d, f))
		timerMu.Unlock()
	}
	isExited := func() bool {
		select {
		case <-exited:
			return true
		default:
			return false
		}
	}

	// The exec runs under its own context, so a process that cannot be ended can
	// be given up on.
	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()

	// stopHard ends the agent and everything it started. The agent starts helper
	// processes that keep its output open, so docker exec does not return until
	// they are gone: ask politely, then kill, and finally stop waiting for it.
	stopHard := func() {
		w.signalAgent(context.Background(), "TERM")
		after(stopGrace, func() {
			if isExited() {
				return
			}
			w.signalAgent(context.Background(), "KILL")
			after(stopGrace, func() {
				if !isExited() {
					cancelExec()
				}
			})
		})
	}

	sw := &streamWriter{s: liveParser{drv}, out: answers, log: w.opt.Err, immediate: true, onMessage: w.opt.OnMessage}
	var leaving sync.Once
	sw.afterLine = func() {
		if drv.Done() {
			closeInput()
			leaving.Do(func() { after(exitGrace, stopHard) })
		}
	}

	var stderr tail
	wrapped := append([]string{"sh", "-c", `echo $$ > ` + agentPidFile + `; exec "$@"`, "sh"}, argv...)
	var execErr error
	go func() {
		defer close(exited)
		execErr = w.sb.Exec(execCtx, runner.ExecOpts{Argv: wrapped, Dir: workspace, Stdin: pr, Stdout: sw, Stderr: &stderr})
		_ = pr.Close() // anything still writing to the agent's input stops waiting
	}()

	// The first message is written in the background: it waits until the agent
	// reads its input. Messages from the owner are taken once it has gone in.
	begun := make(chan struct{})
	go func() {
		defer close(begun)
		if err := drv.Begin(pw, prompt); err != nil {
			closeInput()
		} else if w.opt.OnMessage != nil {
			for _, id := range deliver {
				w.opt.OnMessage(id, agent.MsgDelivered)
			}
		}
	}()

	// interrupt signals the agent. If its turn has not ended (and the waiting
	// messages not been taken) after a while, it is stopped, and the caller
	// starts it again with them.
	interrupt := func() {
		w.signalAgent(ctx, "INT")
		after(interruptGrace, func() {
			if !isExited() && len(drv.Pending()) > 0 {
				stopHard()
			}
		})
	}

	var control <-chan agent.Steer // nil (never ready) until the first message is in
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
			var err error
			if s.Promote {
				err = drv.Promote(s.ID)
			} else {
				err = drv.Send(s)
			}
			switch {
			case errors.Is(err, agent.ErrInterrupt):
				interrupt()
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
	for _, t := range timers {
		t.Stop()
	}
	timerMu.Unlock()
	sw.Close()

	if execErr != nil && ctx.Err() == nil && len(drv.Pending()) == 0 {
		if t := lastLines(stderr.buf, 4); t != "" {
			fmt.Fprintln(w.opt.Err, t)
		}
	}
	return drv.Pending(), execErr
}

// signalAgent sends a signal to the agent process inside the sandbox. INT goes
// to the agent itself, which ends its turn. TERM and KILL go to it and to every
// process it started.
func (w *worker) signalAgent(ctx context.Context, sig string) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	script := "kill -INT $(cat " + agentPidFile + ") 2>/dev/null; true"
	if sig != "INT" {
		script = treeKill(sig)
	}
	_ = w.sb.Exec(cctx, runner.ExecOpts{Argv: []string{"sh", "-c", script}})
}

// treeKill is a shell script that sends sig to the agent and all its
// descendants. The sandbox has no ps or pkill, so the tree is read from /proc.
func treeKill(sig string) string {
	return `root=$(cat ` + agentPidFile + `) || exit 0; ` +
		`for f in /proc/[0-9]*/stat; do sed -n 's/^\([0-9]*\) (.*) [A-Za-z] \([0-9]*\) .*/\1 \2/p' "$f" 2>/dev/null; done | ` +
		`awk -v root="$root" '{p[$1]=$2} END {for (i in p) {x=i; while ((x in p) && x!=root && x>1) x=p[x]; if (x==root) print i} print root}' | sort -u | ` +
		`while read pid; do kill -` + sig + ` "$pid" 2>/dev/null; done; true`
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

package agent

import (
	"errors"
	"io"
)

// Steering: telling an agent something while it works.
//
// In plain mode an agent is run once, with its prompt on the command line, and
// nothing can be said to it until it ends. A live agent is run as a process that
// stays open and reads the owner's messages on its standard input. Each agent
// has its own protocol for that, so each gets a LiveDriver that speaks it.
//
// A message is sent one of two ways:
//   - Send: the agent reads it at its next step (or after the current turn, for
//     an agent that cannot take it sooner).
//   - Send now (Steer.Now): stop what it is doing and carry on from the message.

// Steer is something the owner says to a running agent.
type Steer struct {
	ID   string // an identifier for showing the message's state
	Text string
	Now  bool // interrupt, then carry on from this message
}

// Message states, shown next to the message in the conversation.
const (
	MsgQueued    = "queued"    // handed over, the agent has not read it yet
	MsgDelivered = "delivered" // the agent has taken it
	MsgFailed    = "failed"    // it could not be delivered
	MsgCancelled = "cancelled" // the agent dropped it
)

// MsgState reports a change in what happened to a message.
type MsgState struct {
	ID    string
	State string
}

// ErrRestart is returned by Send when the agent can only be redirected by
// stopping its process and starting it again from its saved conversation. The
// driver keeps the message; Unire restarts the agent with LiveOptions.Resume
// set and Prompt set to the message (see Pending).
var ErrRestart = errors.New("the agent has to be restarted to take this message")

// LiveOptions describe the run to start.
type LiveOptions struct {
	Prompt string
	Mode   string
	Ref    string // a conversation id Unire chose (for agents that accept one)
	Resume string // the agent's own conversation id, to carry on after a restart
	Dir    string // the working directory the agent works in
}

// Live is implemented by agents that can be steered.
type Live interface {
	// StartLive returns the command to run (a process that reads messages on
	// stdin) and the driver for it.
	StartLive(o LiveOptions) (argv []string, d LiveDriver, err error)
}

// LiveDriver speaks one agent's protocol for one run. Its methods may be called
// from different goroutines.
type LiveDriver interface {
	// Begin is called once the process is starting. It sends the first message
	// (and any handshake) on in.
	Begin(in io.Writer, prompt string) error
	// Line reads one line of the agent's output.
	Line(line string) Event
	// Send gives the agent a message from the owner. It returns ErrRestart when
	// the agent has to be restarted to take it.
	Send(s Steer) error
	// Done reports that the agent has finished everything it was given, so its
	// input can be closed.
	Done() bool
	// Resume is the agent's own conversation id, once it has reported one.
	Resume() string
	// Pending is the message an ErrRestart was returned for.
	Pending() (Steer, bool)
}

// CanSteer reports whether an agent can take messages while it works.
func CanSteer(name string) bool {
	a, err := Get(name)
	if err != nil {
		return false
	}
	_, ok := a.(Live)
	return ok
}

var errNotUnattended = errors.New("only unattended runs can be steered")

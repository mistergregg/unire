package agent

import (
	"errors"
	"io"
	"strings"
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
	Now  bool // interrupt, then carry on from this message (and anything waiting)
	// Promote: the message with this ID was already sent and is waiting; make the
	// agent take it now. Text is not used.
	Promote bool
	// Files are copied into the sandbox before the agent is given the message
	// (images the owner attached, which Text names).
	Files []File
}

// File is a file on the host to copy into the sandbox.
type File struct {
	From string // host path
	To   string // sandbox path
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

// ErrInterrupt is returned by Send and Promote when the agent has to be
// interrupted with a signal for the message to be taken. The driver holds the
// waiting messages and writes them, all at once, when the interrupted turn ends.
var ErrInterrupt = errors.New("the agent has to be interrupted to take this message")

// JoinSteers makes one message of several, in the order they were sent.
func JoinSteers(list []Steer) string {
	var parts []string
	for _, s := range list {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n\n")
}

// LiveOptions describe the run to start.
type LiveOptions struct {
	Prompt string
	Mode   string
	Model  string // model to use; empty means the agent's default
	Ref    string // a conversation id Unire chose (for agents that accept one)
	Resume string // the agent's own conversation id, to carry on (a reply, or a restart)
	// Fallback is the prompt to use if that conversation cannot be carried on (it was
	// lost): it tells a fresh agent what happened so far.
	Fallback string
	Dir      string // the working directory the agent works in
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
	// Send gives the agent a message from the owner. It returns ErrInterrupt when
	// the agent has to be interrupted with a signal to take it.
	Send(s Steer) error
	// Promote makes the agent take a message that was already sent and is waiting,
	// right away. It returns ErrInterrupt when that needs a signal.
	Promote(id string) error
	// Done reports that the agent has finished everything it was given, so its
	// input can be closed.
	Done() bool
	// Resume is the agent's own conversation id, once it has reported one.
	Resume() string
	// Pending is every message the driver holds that the agent has not been given
	// (for example because the process ended first).
	Pending() []Steer
}

// ExitMarker starts the line the sandbox prints when a steerable agent's process
// ends (followed by its exit status). docker exec does not return while its input
// is open, so seeing this, Unire closes the input.
const ExitMarker = "unire-agent-exited:"

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

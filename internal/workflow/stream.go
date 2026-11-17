package workflow

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/mistergregg/unire/internal/agent"
)

// eventParser turns one line of an agent's output into an event. A Streamer
// (an agent run once) and a LiveDriver (an agent that is steered) both are one.
type eventParser interface {
	ParseEvent(line string) agent.Event
}

// liveParser adapts a LiveDriver, whose method is called Line.
type liveParser struct{ d agent.LiveDriver }

func (p liveParser) ParseEvent(line string) agent.Event { return p.d.Line(line) }

// streamWriter reads an agent's event stream. Progress goes to the log as the
// agent works, and the final answer is written to out when the stream ends.
// A line that is not part of the stream is passed through as it is, so nothing
// the agent prints (an error, a sign-in prompt) is ever lost, and an agent whose
// stream is not understood at all behaves like one run in plain mode.
//
// For an agent that is steered (immediate), a turn's answer is written as soon
// as it arrives, because there can be several turns, and afterLine is called
// after every line (the live loop uses it to close the agent's input when the
// agent has finished).
type streamWriter struct {
	s        eventParser
	out, log io.Writer

	immediate bool
	onMessage func(id, state string)
	afterLine func()

	mu       sync.Mutex
	buf      []byte
	answer   string // the final answer, when the stream gave one
	answered bool   // an answer was already written (immediate mode)
	last     string // the last thing the agent wrote, used if there was no final answer
	frag     string // a reply that arrived in pieces, used if there is nothing better
	fragStep int
	progress string // the last progress line, so a repeat is not shown twice
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	var lines []string
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	w.mu.Unlock()
	for _, l := range lines {
		w.line(l)
		if w.afterLine != nil {
			w.afterLine()
		}
	}
	return len(p), nil
}

func (w *streamWriter) line(l string) {
	l = strings.TrimRight(l, "\r")
	if strings.TrimSpace(l) == "" {
		return
	}
	ev := w.s.ParseEvent(l)
	w.mu.Lock()
	defer w.mu.Unlock()
	if !ev.Known {
		fmt.Fprintln(w.out, l)
		return
	}
	if ev.Progress != "" && ev.Progress != w.progress {
		fmt.Fprintln(w.log, ev.Progress)
		w.progress = ev.Progress
	}
	if ev.Msg != nil && w.onMessage != nil {
		w.onMessage(ev.Msg.ID, ev.Msg.State)
	}
	if ev.Text != "" {
		w.last = ev.Text
	}
	if ev.Fragment != "" {
		if ev.Step != w.fragStep {
			w.frag, w.fragStep = "", ev.Step
		}
		w.frag += ev.Fragment
	}
	if ev.Answer != "" {
		if w.immediate {
			if w.answered {
				fmt.Fprintln(w.out) // a blank line between one turn's answer and the next
			}
			fmt.Fprintln(w.out, ev.Answer)
			w.answered = true
			w.last, w.frag = "", ""
		} else {
			w.answer = ev.Answer
		}
	}
}

// Close writes what is left: the final answer.
func (w *streamWriter) Close() {
	w.mu.Lock()
	rest := string(w.buf)
	w.buf = nil
	w.mu.Unlock()
	if rest != "" {
		w.line(rest)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	final := w.answer
	if final == "" {
		final = w.last
	}
	if final == "" {
		final = w.frag
	}
	if final != "" && !(w.immediate && w.answered && w.answer == "") {
		fmt.Fprintln(w.out, final)
	}
}

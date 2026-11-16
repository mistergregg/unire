package workflow

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/mistergregg/unire/internal/agent"
)

// streamWriter reads an agent's event stream. Progress goes to the log as the
// agent works, and the final answer is written to out when the stream ends.
// A line that is not part of the stream is passed through as it is, so nothing
// the agent prints (an error, a sign-in prompt) is ever lost, and an agent whose
// stream is not understood at all behaves like one run in plain mode.
type streamWriter struct {
	s        agent.Streamer
	out, log io.Writer

	buf      []byte
	answer   string // the final answer, when the stream gave one
	last     string // the last thing the agent wrote, used if there was no final answer
	progress string // the last progress line, so a repeat is not shown twice
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.line(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
}

func (w *streamWriter) line(l string) {
	l = strings.TrimRight(l, "\r")
	if strings.TrimSpace(l) == "" {
		return
	}
	ev := w.s.ParseEvent(l)
	if !ev.Known {
		fmt.Fprintln(w.out, l)
		return
	}
	if ev.Progress != "" && ev.Progress != w.progress {
		fmt.Fprintln(w.log, ev.Progress)
		w.progress = ev.Progress
	}
	if ev.Text != "" {
		w.last = ev.Text
	}
	if ev.Answer != "" {
		w.answer = ev.Answer
	}
}

// Close writes what is left: the final answer.
func (w *streamWriter) Close() {
	if len(w.buf) > 0 {
		w.line(string(w.buf))
		w.buf = nil
	}
	final := w.answer
	if final == "" {
		final = w.last
	}
	if final != "" {
		fmt.Fprintln(w.out, final)
	}
}

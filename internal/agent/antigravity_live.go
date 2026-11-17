package agent

import (
	"encoding/json"
	"io"
	"sync"
)

// Steering Antigravity. `agy --input-format stream-json` reads one message per
// line ({"event":"user","message":{"content":"..."}}) and runs a turn for each,
// in one conversation. Its documentation says to wait for a turn's result
// before writing the next message, and it describes no control message to
// interrupt a turn, so:
//   - Send: the message waits and is written after the current turn's result;
//   - Send now: the process gets SIGINT, which ends the turn ("INTERRUPTED").
//     Nothing documents what the process does after that, so it is not relied on:
//     the waiting messages are held (see Pending), the process is ended, and
//     Unire starts it again from the saved conversation (--conversation <id>)
//     with all of them at once as the new prompt.
// See antigravity.google/docs/cli/headless.

// StartLive implements Live.
func (a antigravity) StartLive(o LiveOptions) ([]string, LiveDriver, error) {
	if o.Mode != HandsOff {
		return nil, nil, errNotUnattended
	}
	// --print= (empty) because the messages come on stdin, and agy reads the
	// next argument as the prompt when the value is given separately.
	argv := []string{"agy", "--dangerously-skip-permissions", "--print=", "--input-format=stream-json", "--output-format=stream-json"}
	if o.Resume != "" {
		argv = append(argv, "--conversation", o.Resume)
	}
	return argv, &antigravityLive{a: a, conv: o.Resume}, nil
}

type antigravityLive struct {
	a antigravity

	writeMu sync.Mutex
	in      io.Writer

	mu          sync.Mutex
	turnActive  bool    // a message was written and its result has not come
	sawResult   bool    // at least one turn has ended
	queue       []Steer // messages the agent has not been given yet
	interrupted bool    // SIGINT was asked for: the turn's result is not a cue to write the queue
	conv        string  // the agent's conversation id
}

func (d *antigravityLive) write(text string) error {
	b, err := json.Marshal(map[string]any{"event": "user", "message": map[string]any{"content": text}})
	if err != nil {
		return err
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err = d.in.Write(append(b, '\n'))
	return err
}

func (d *antigravityLive) Begin(in io.Writer, prompt string) error {
	d.in = in
	d.mu.Lock()
	d.turnActive = true
	d.mu.Unlock()
	return d.write(prompt)
}

func (d *antigravityLive) Send(s Steer) error {
	d.mu.Lock()
	if d.turnActive {
		d.queue = append(d.queue, s)
		if s.Now {
			d.interrupted = true
		}
		d.mu.Unlock()
		if s.Now {
			return ErrInterrupt // the process is signalled, ended, and started again with the queue
		}
		return nil
	}
	// Idle: it can go straight in, together with anything that is waiting.
	batch := append(d.queue, s)
	d.queue, d.turnActive = nil, true
	d.mu.Unlock()
	return d.write(JoinSteers(batch))
}

func (d *antigravityLive) Promote(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.turnActive {
		return nil // nothing is running: the waiting messages are written as the turn ends
	}
	for _, s := range d.queue {
		if s.ID == id {
			d.interrupted = true
			return ErrInterrupt
		}
	}
	return nil
}

func (d *antigravityLive) Line(line string) Event {
	ev := d.a.ParseEvent(line)
	m, ok := parseObject(line)
	if !ok {
		return ev
	}
	// The conversation id appears at the top level of init, and inside the others.
	id, _ := m["conversation_id"].(string)
	if id == "" {
		for _, k := range []string{"step_update", "result"} {
			if inner, ok := m[k].(map[string]any); ok {
				if s, _ := inner["conversation_id"].(string); s != "" {
					id = s
				}
			}
		}
	}
	if id != "" {
		d.mu.Lock()
		d.conv = id
		d.mu.Unlock()
	}
	if m["event"] == "result" {
		d.mu.Lock()
		d.turnActive, d.sawResult = false, true
		if d.interrupted {
			// The turn was cut short on purpose: the messages stay held until the
			// process has been ended and started again.
			d.interrupted = false
			d.mu.Unlock()
			return ev
		}
		batch := d.queue
		if len(batch) > 0 {
			d.queue, d.turnActive = nil, true
		}
		d.mu.Unlock()
		if len(batch) > 0 {
			state := MsgDelivered
			if err := d.write(JoinSteers(batch)); err != nil {
				state = MsgFailed
				d.mu.Lock()
				d.queue, d.turnActive = append(batch, d.queue...), false
				d.mu.Unlock()
			}
			for _, s := range batch {
				ev.Msgs = append(ev.Msgs, MsgState{ID: s.ID, State: state})
			}
		}
	}
	return ev
}

func (d *antigravityLive) Done() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sawResult && !d.turnActive && len(d.queue) == 0
}

func (d *antigravityLive) Resume() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conv
}

func (d *antigravityLive) Pending() []Steer {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Steer(nil), d.queue...)
}

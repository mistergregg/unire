package agent

import (
	"encoding/json"
	"io"
	"sync"
)

// Steering Antigravity. `agy --input-format stream-json` reads one message per
// line ({"event":"user","message":{"content":"..."}}) and runs a turn for each,
// in one conversation. Its documentation says to wait for a turn's result
// before writing the next message and describes no way to interrupt a turn
// through that channel, so:
//   - Send: the message waits and is written after the current turn's result;
//   - Send now: the process is stopped (SIGINT) and started again from the saved
//     conversation (--conversation <id>) with the message as its prompt.
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

	mu         sync.Mutex
	turnActive bool    // a message was written and its result has not come
	sawResult  bool    // at least one turn has ended
	queue      []Steer // messages waiting for the current turn to end
	conv       string  // the agent's conversation id
	restart    *Steer  // the message a restart was asked for
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
	if s.Now {
		d.restart = &s
		d.mu.Unlock()
		return ErrRestart
	}
	if d.turnActive {
		d.queue = append(d.queue, s)
		d.mu.Unlock()
		return nil
	}
	d.turnActive = true // idle: it can go straight in
	d.mu.Unlock()
	return d.write(s.Text)
}

func (d *antigravityLive) Line(line string) Event {
	ev := d.a.ParseEvent(line)
	if m, ok := parseObject(line); ok {
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
			var next *Steer
			if len(d.queue) > 0 {
				s := d.queue[0]
				d.queue = d.queue[1:]
				next = &s
				d.turnActive = true
			}
			d.mu.Unlock()
			if next != nil {
				if err := d.write(next.Text); err != nil {
					ev.Msg = &MsgState{ID: next.ID, State: MsgFailed}
				} else {
					ev.Msg = &MsgState{ID: next.ID, State: MsgDelivered}
				}
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

func (d *antigravityLive) Pending() (Steer, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.restart == nil {
		return Steer{}, false
	}
	return *d.restart, true
}

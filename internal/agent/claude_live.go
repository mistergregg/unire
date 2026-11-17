package agent

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Steering Claude Code. `claude -p --input-format stream-json` stays open and
// reads one JSON message per line. A message sent while it works is read at its
// next step (priority "next", the default); with priority "now" and a human
// origin it moves background work aside and reads the message in the same turn,
// or interrupts the turn if it cannot. It reports each message's lifecycle
// ("queued", "started", "cancelled"). Documented at
// code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode.

// StartLive implements Live.
func (c claude) StartLive(o LiveOptions) ([]string, LiveDriver, error) {
	if o.Mode != HandsOff {
		return nil, nil, errNotUnattended
	}
	argv := []string{"claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
	if o.Ref != "" {
		argv = append(argv, "--session-id", o.Ref)
	}
	return argv, &claudeLive{c: c, state: map[string]string{}, owner: map[string]string{}}, nil
}

type claudeLive struct {
	c claude

	writeMu sync.Mutex // one message at a time on stdin
	in      io.Writer

	mu        sync.Mutex
	state     map[string]string // message uuid -> sent, queued, started, cancelled, done
	owner     map[string]string // message uuid -> the owner's id for it ("" for the first prompt)
	sawResult bool              // a turn has ended since the last one began
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (d *claudeLive) write(text string, now bool, ownerID string) error {
	id := newUUID()
	m := map[string]any{
		"type":               "user",
		"message":            map[string]any{"role": "user", "content": text},
		"parent_tool_use_id": nil,
		"uuid":               id,
	}
	if now {
		m["priority"] = "now"
		m["origin"] = map[string]any{"kind": "human"}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.state[id], d.owner[id] = "sent", ownerID
	d.mu.Unlock()
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err = d.in.Write(append(b, '\n'))
	return err
}

func (d *claudeLive) Begin(in io.Writer, prompt string) error {
	d.in = in
	return d.write(prompt, false, "")
}

func (d *claudeLive) Send(s Steer) error { return d.write(s.Text, s.Now, s.ID) }

func (d *claudeLive) Line(line string) Event {
	m, ok := parseObject(line)
	if !ok {
		return d.c.ParseEvent(line)
	}
	switch m["type"] {
	case "command_lifecycle":
		id, _ := m["command_uuid"].(string)
		st, _ := m["state"].(string)
		d.mu.Lock()
		defer d.mu.Unlock()
		if _, mine := d.state[id]; !mine {
			return Event{Known: true}
		}
		d.state[id] = st
		if st == "started" {
			d.sawResult = false // a turn is beginning
		}
		ev := Event{Known: true}
		if owner := d.owner[id]; owner != "" {
			switch st {
			case "queued":
				ev.Msg = &MsgState{ID: owner, State: MsgQueued}
			case "cancelled":
				ev.Msg = &MsgState{ID: owner, State: MsgCancelled}
			default: // started, or anything that means it was taken
				ev.Msg = &MsgState{ID: owner, State: MsgDelivered}
			}
		}
		return ev
	case "result":
		d.mu.Lock()
		d.sawResult = true
		d.mu.Unlock()
	}
	return d.c.ParseEvent(line)
}

// Done: a turn has ended and every message has been taken (or dropped).
func (d *claudeLive) Done() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.sawResult {
		return false
	}
	for _, st := range d.state {
		if st == "sent" || st == "queued" {
			return false
		}
	}
	return true
}

func (d *claudeLive) Resume() string         { return "" }
func (d *claudeLive) Pending() (Steer, bool) { return Steer{}, false }

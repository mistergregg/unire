package agent

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Steering Codex. `codex app-server` speaks JSON-RPC over stdin and stdout, one
// message per line (the "jsonrpc" field is left out). A client says initialize,
// starts a thread, starts a turn, and then can:
//   - turn/steer: add input to the turn that is running, with no new turn;
//   - turn/interrupt: cancel the turn (it ends with status "interrupted").
// The messages and their fields are in the schema `codex app-server
// generate-json-schema` prints; see also developers.openai.com/codex/app-server.

// StartLive implements Live.
func (c codex) StartLive(o LiveOptions) ([]string, LiveDriver, error) {
	if o.Mode != HandsOff {
		return nil, nil, errNotUnattended
	}
	dir := o.Dir
	if dir == "" {
		dir = "/workspace"
	}
	// app-server takes no --model: the model goes in thread/start, thread/resume
	// and turn/start (see their params in generate-json-schema).
	return []string{"codex", "app-server"}, &codexLive{dir: dir, model: c.Model, resume: o.Resume, fallback: o.Fallback, kinds: map[int]string{}}, nil
}

type codexLive struct {
	dir      string
	model    string
	resume   string // a thread to carry on (thread/resume) instead of starting one
	fallback string // the prompt to use instead if that thread cannot be carried on

	writeMu sync.Mutex
	in      io.Writer

	mu          sync.Mutex
	nextID      int
	kinds       map[int]string // request id -> what it was ("initialize", "thread", "turn", "steer:<id>", "interrupt")
	prompt      string
	threadID    string
	turnID      string
	active      bool    // a turn has been started and has not completed
	sawTurn     bool    // a turn has completed (or the run failed)
	queue       []Steer // messages to start as a turn once the current one ends
	afterStop   []Steer // messages to start, together, once an interrupt has finished the turn
	wantStop    bool    // interrupt as soon as the turn id is known
	failed      bool    // initialize, thread, or turn failed fatally
	lastMessage string  // the agent's latest message in this turn: its answer
}

// out collects what to write, so nothing is written while holding mu.
type out [][]byte

func (o *out) add(v any) {
	b, _ := json.Marshal(v)
	*o = append(*o, append(b, '\n'))
}

func (d *codexLive) flush(o out) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	for _, b := range o {
		if _, err := d.in.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// request adds a request to o and remembers what it was.
func (d *codexLive) request(o *out, method, kind string, params any) {
	d.nextID++
	d.kinds[d.nextID] = kind
	o.add(map[string]any{"method": method, "id": d.nextID, "params": params})
}

func textInput(text string) []any {
	return []any{map[string]any{"type": "text", "text": text, "text_elements": []any{}}}
}

// startTurn adds a turn/start. The caller holds mu.
func (d *codexLive) startTurn(o *out, text string) {
	d.active, d.turnID, d.lastMessage = true, "", ""
	params := map[string]any{"threadId": d.threadID, "input": textInput(text)}
	if d.model != "" {
		params["model"] = d.model
	}
	d.request(o, "turn/start", "turn", params)
}

func (d *codexLive) Begin(in io.Writer, prompt string) error {
	d.in = in
	d.mu.Lock()
	d.prompt = prompt
	var o out
	d.request(&o, "initialize", "initialize", map[string]any{"clientInfo": map[string]any{"name": "unire", "title": "Unire", "version": "1"}})
	d.mu.Unlock()
	return d.flush(o)
}

func (d *codexLive) Send(s Steer) error {
	d.mu.Lock()
	if d.failed {
		d.mu.Unlock()
		return errors.New("codex session has failed")
	}
	var o out
	switch {
	case d.threadID == "":
		d.queue = append(d.queue, s) // still starting up: it goes in as soon as it can
	case !d.active:
		d.startTurn(&o, s.Text) // between turns
	case s.Now:
		d.afterStop = append(d.afterStop, s)
		if d.turnID == "" {
			d.wantStop = true
		} else {
			d.request(&o, "turn/interrupt", "interrupt", map[string]any{"threadId": d.threadID, "turnId": d.turnID})
		}
	case d.turnID == "":
		d.queue = append(d.queue, s) // the turn has not reported its id yet
	default:
		d.request(&o, "turn/steer", "steer:"+s.ID, map[string]any{"threadId": d.threadID, "expectedTurnId": d.turnID, "input": textInput(s.Text)})
	}
	d.mu.Unlock()
	return d.flush(o)
}

func rpcErrorText(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		s, _ := e["message"].(string)
		return clip(s, 300)
	}
	return ""
}

func (d *codexLive) Line(line string) Event {
	m, ok := parseObject(line)
	if !ok {
		return Event{}
	}
	method, _ := m["method"].(string)
	idf, hasID := m["id"].(float64)
	var o out
	ev := Event{Known: true}

	d.mu.Lock()
	switch {
	case hasID && method == "": // a response to one of our requests
		kind := d.kinds[int(idf)]
		delete(d.kinds, int(idf))
		failure := rpcErrorText(m)
		result, _ := m["result"].(map[string]any)
		switch {
		case kind == "initialize" && failure == "":
			o.add(map[string]any{"method": "initialized"})
			if d.resume != "" {
				// Carry on the agent's own saved thread: it remembers the whole conversation.
				params := map[string]any{"threadId": d.resume, "cwd": d.dir, "approvalPolicy": "never", "sandbox": "danger-full-access", "excludeTurns": true}
				if d.model != "" {
					params["model"] = d.model
				}
				d.request(&o, "thread/resume", "resume", params)
			} else {
				params := map[string]any{"cwd": d.dir, "approvalPolicy": "never", "sandbox": "danger-full-access"}
				if d.model != "" {
					params["model"] = d.model
				}
				d.request(&o, "thread/start", "thread", params)
			}
		case kind == "resume" && failure != "":
			// The saved thread is gone (or unreadable): start a new one, with the prompt that
			// tells the agent what happened so far.
			ev.Progress = "could not carry on the earlier conversation (" + failure + "): starting a new one with a summary of it"
			if d.fallback != "" {
				d.prompt = d.fallback
			}
			params := map[string]any{"cwd": d.dir, "approvalPolicy": "never", "sandbox": "danger-full-access"}
			if d.model != "" {
				params["model"] = d.model
			}
			d.request(&o, "thread/start", "thread", params)
		case (kind == "thread" || kind == "resume") && failure == "":
			if th, ok := result["thread"].(map[string]any); ok {
				d.threadID, _ = th["id"].(string)
			}
			d.startTurn(&o, d.prompt)
		case kind == "turn" && failure == "":
			if t, ok := result["turn"].(map[string]any); ok {
				d.turnID, _ = t["id"].(string)
			}
			if d.wantStop && d.turnID != "" {
				d.wantStop = false
				d.request(&o, "turn/interrupt", "interrupt", map[string]any{"threadId": d.threadID, "turnId": d.turnID})
			}
		case strings.HasPrefix(kind, "steer:"):
			id := strings.TrimPrefix(kind, "steer:")
			if failure == "" {
				ev.Msgs = []MsgState{{ID: id, State: MsgDelivered}}
			} else {
				ev.Msgs = []MsgState{{ID: id, State: MsgFailed}}
				ev.Progress = "codex could not take the message: " + failure
			}
		}
		if failure != "" && (kind == "initialize" || kind == "thread" || kind == "turn") {
			ev.Progress = "codex error: " + failure
			d.active, d.sawTurn, d.failed = false, true, true // nothing more will happen
			for _, s := range append(append([]Steer(nil), d.afterStop...), d.queue...) {
				ev.Msgs = append(ev.Msgs, MsgState{ID: s.ID, State: MsgFailed})
			}
			d.afterStop, d.queue = nil, nil
		}
	case hasID && method != "": // a request from the server that Unire does not handle
		o.add(map[string]any{"id": idf, "error": map[string]any{"code": -32601, "message": "Unire does not handle " + method}})
		ev.Progress = "codex asked for something Unire cannot answer: " + method
	default:
		p, _ := m["params"].(map[string]any)
		switch method {
		case "account/rateLimits/updated":
			if u := ParseCodexUsage(p); u != nil {
				ev.Usage = u
			}
		case "turn/started":
			if t, ok := p["turn"].(map[string]any); ok {
				if id, _ := t["id"].(string); id != "" {
					d.turnID = id
				}
			}
		case "item/started", "item/completed":
			item, _ := p["item"].(map[string]any)
			ev.Progress, ev.Text = d.describeItem(method == "item/completed", item)
		case "turn/completed":
			t, _ := p["turn"].(map[string]any)
			status, _ := t["status"].(string)
			d.active, d.sawTurn, d.turnID = false, true, ""
			switch status {
			case "completed":
				ev.Answer = d.lastMessage
			case "failed":
				msg := ""
				if te, ok := t["error"].(map[string]any); ok {
					msg, _ = te["message"].(string)
				}
				ev.Progress = "the turn failed: " + clip(msg, 300)
			}
			// What to do next: carry on from the messages that wait, as one turn.
			if batch := append(d.afterStop, d.queue...); len(batch) > 0 {
				d.afterStop, d.queue = nil, nil
				d.startTurn(&o, JoinSteers(batch))
				for _, m := range batch {
					ev.Msgs = append(ev.Msgs, MsgState{ID: m.ID, State: MsgDelivered})
				}
			}
		case "error":
			if e, ok := p["error"].(map[string]any); ok {
				msg, _ := e["message"].(string)
				if willRetry, _ := p["willRetry"].(bool); !willRetry {
					ev.Progress = "codex error: " + clip(msg, 300)
				}
			}
		}
	}
	d.mu.Unlock()
	_ = d.flush(o)
	return ev
}

// describeItem turns a started or finished item into a progress line, and
// remembers what the agent said. The caller holds mu.
func (d *codexLive) describeItem(done bool, item map[string]any) (progress, text string) {
	kind, _ := item["type"].(string)
	switch kind {
	case "agentMessage":
		t, _ := item["text"].(string)
		if done && strings.TrimSpace(t) != "" {
			d.lastMessage = t
			return "says: " + clip(t, 200), t
		}
	case "commandExecution":
		c, _ := item["command"].(string)
		if !done {
			return "command: " + clip(c, 200), ""
		}
		if code, ok := item["exitCode"].(float64); ok && code != 0 {
			return "command failed (exit " + strconv.Itoa(int(code)) + "): " + clip(c, 160), ""
		}
	case "fileChange":
		if done {
			return "changed files", ""
		}
	case "mcpToolCall":
		if !done {
			server, _ := item["server"].(string)
			tool, _ := item["tool"].(string)
			return "tool: " + server + "/" + tool, ""
		}
	case "webSearch":
		if q, _ := item["query"].(string); q != "" && !done {
			return "search: " + clip(q, 160), ""
		}
	}
	return "", ""
}

func (d *codexLive) Done() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.failed || (d.sawTurn && !d.active && len(d.queue) == 0 && len(d.afterStop) == 0)
}

func (d *codexLive) Resume() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.threadID
}

func (d *codexLive) Pending() []Steer {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.afterStop) == 0 && len(d.queue) == 0 {
		return nil
	}
	out := make([]Steer, 0, len(d.afterStop)+len(d.queue))
	out = append(out, d.afterStop...)
	out = append(out, d.queue...)
	return out
}

// Promote takes a message that is waiting and interrupts the turn so it is read
// now (together with anything else that waits).
func (d *codexLive) Promote(id string) error {
	d.mu.Lock()
	var o out
	for i, s := range d.queue {
		if s.ID != id {
			continue
		}
		d.queue = append(d.queue[:i], d.queue[i+1:]...)
		switch {
		case !d.active:
			d.startTurn(&o, JoinSteers(append([]Steer{s}, d.queue...)))
			d.queue = nil
		case d.turnID == "":
			d.afterStop, d.wantStop = append(d.afterStop, s), true
		default:
			d.afterStop = append(d.afterStop, s)
			d.request(&o, "turn/interrupt", "interrupt", map[string]any{"threadId": d.threadID, "turnId": d.turnID})
		}
		break
	}
	d.mu.Unlock()
	return d.flush(o)
}

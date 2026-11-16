package agent

import (
	"encoding/json"
	"strings"
)

// Streamer is implemented by agents that can report what they are doing while
// they work. In plain mode an agent prints only its final answer, at the end, so
// the owner sees nothing for minutes. A streaming agent prints one JSON event
// per line; ParseEvent turns each into a short progress line for the log, and
// finds the final answer.
type Streamer interface {
	// StreamCommand is Command, asking for the JSON event stream. Only used for
	// unattended runs with a prompt.
	StreamCommand(prompt, mode, ref string) ([]string, error)
	// ParseEvent reads one line of the stream.
	ParseEvent(line string) Event
}

// Event is what one line of an agent's stream means.
type Event struct {
	Known    bool   // a recognised event; false means the line is not part of the stream
	Progress string // a short line about what the agent is doing, or ""
	Text     string // text the agent wrote (the answer so far, or something it said on the way)
	Answer   string // the final answer, on the last event
}

// clip makes s a single short line.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// firstString returns the first of the keys that holds a non-empty string.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func parseObject(line string) (map[string]any, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return nil, false
	}
	return m, true
}

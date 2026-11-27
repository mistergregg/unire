package agent

import (
	"fmt"
	"strings"
)

type claude struct {
	Model string
}

func init() { Register(claude{}) }

func (claude) Name() string           { return "claude" }
func (claude) MemoryFile() string     { return "CLAUDE.md" }
func (claude) MemoryMode() MemoryMode { return MemoryFile }

func (c claude) WithModel(model string) Adapter {
	c.Model = model
	return c
}

func (claude) Info() Info {
	return Info{
		ID: "claude", Label: "Claude Code",
		Description: "Anthropic. Uses your Claude subscription.",
		LoginHelp:   "Click the link, sign in, then paste the code it shows and press Enter.",
		LoginArgv:   []string{"claude", "auth", "login", "--claudeai"},
		StatusArgv:  []string{"claude", "auth", "status"},
		LoggedIn: func(out string, ok bool) bool {
			compact := strings.Join(strings.Fields(out), "")
			return ok && strings.Contains(compact, `"loggedIn":true`)
		},
		// The status output names the account, so it is shown to the owner only.
		InspectArgv: []string{"claude", "auth", "status"},
		ResetArgv:   []string{"claude", "auth", "logout"},
		ResetHelp:   "Signs Claude Code out of the saved login.",
		UsageArgv:   claudeUsageArgv,
		ParseUsage:  ParseClaudeUsageOutput,
		Models:      []string{"opus", "sonnet", "fable", "haiku"},
		ModelsArgv:  []string{"sh", "-c", claudeModelsScript},
		ParseModels: ParseClaudeModelsOutput,
		Verified:    Verified{Unattended: true, Status: true, Hosts: true},
	}
}

// Hosts are the services Claude Code needs. api.anthropic.com and
// mcp-proxy.anthropic.com were seen in a real session through the egress
// proxy; claude.ai and console.anthropic.com are its login services.
// Telemetry hosts (such as datadog) are deliberately not listed. If a session
// in agent-only mode needs another host, the owner can allow it from the log.
func (claude) Hosts() []string {
	return []string{"api.anthropic.com", "mcp-proxy.anthropic.com", "console.anthropic.com", "claude.ai"}
}

func (c claude) Command(prompt, mode, ref string) ([]string, error) {
	// --session-id stores the conversation under ref so FollowUp can resume
	// it. Conversations share one home folder across sessions, so resuming by
	// id, never "the latest", keeps sessions from mixing.
	id := []string{}
	if ref != "" {
		id = []string{"--session-id", ref}
	}
	var model []string
	if c.Model != "" {
		model = []string{"--model", c.Model}
	}
	switch mode {
	case HandsOff:
		if prompt == "" {
			return append(append([]string{"claude", "--dangerously-skip-permissions"}, id...), model...), nil
		}
		// -p runs once and exits, which is what an unattended session needs.
		// "--" keeps a prompt that starts with a dash from being read as a flag.
		argv := append(append([]string{"claude", "-p", "--dangerously-skip-permissions"}, id...), model...)
		return append(argv, "--", prompt), nil
	case AskFirst:
		argv := append(append([]string{"claude"}, id...), model...)
		if prompt == "" {
			return argv, nil
		}
		return append(argv, "--", prompt), nil
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

func (c claude) FollowUp(prompt, mode, ref string) ([]string, bool) {
	if ref == "" || prompt == "" || mode != HandsOff {
		return nil, false
	}
	argv := []string{"claude", "-p", "--dangerously-skip-permissions"}
	if c.Model != "" {
		argv = append(argv, "--model", c.Model)
	}
	argv = append(argv, "--resume", ref, "--", prompt)
	return argv, true
}

// StreamCommand asks Claude Code for its event stream (one JSON object per
// line; --verbose is required for it).
func (c claude) StreamCommand(prompt, mode, ref string) ([]string, error) {
	argv, err := c.Command(prompt, mode, ref)
	if err != nil || mode != HandsOff || prompt == "" {
		return argv, err
	}
	// Command is: claude -p --dangerously-skip-permissions [--session-id id] -- prompt
	out := []string{argv[0], argv[1], "--output-format", "stream-json", "--verbose"}
	return append(out, argv[2:]...), nil
}

// ParseEvent reads Claude Code's stream-json: "assistant" events carry what it
// says and the tools it calls, and a final "result" event carries the answer.
func (claude) ParseEvent(line string) Event {
	m, ok := parseObject(line)
	if !ok {
		return Event{}
	}
	sessID, _ := m["session_id"].(string)
	switch m["type"] {
	case "result":
		ans, _ := m["result"].(string)
		ev := Event{Known: true, Answer: ans, Conversation: sessID}
		if isErr, _ := m["is_error"].(bool); isErr && ans == "" {
			if msg := firstString(m, "error"); msg != "" {
				ev.Progress = "agent error: " + clip(msg, 300)
			} else if errs, _ := m["errors"].([]any); len(errs) > 0 {
				if first, ok := errs[0].(string); ok && strings.TrimSpace(first) != "" {
					ev.Progress = "agent error: " + clip(first, 300)
				}
			}
			if ev.Progress == "" {
				if sub, _ := m["subtype"].(string); sub != "" {
					ev.Progress = "agent error: " + sub
				}
			}
		}
		return ev
	case "assistant":
		msg, _ := m["message"].(map[string]any)
		blocks, _ := msg["content"].([]any)
		var ev Event
		ev.Known = true
		var said []string
		for _, b := range blocks {
			blk, _ := b.(map[string]any)
			switch blk["type"] {
			case "text":
				t, _ := blk["text"].(string)
				said = append(said, t)
			case "thinking":
				if t, _ := blk["thinking"].(string); strings.TrimSpace(t) != "" {
					ev.Progress = "thinking: " + clip(t, 200)
				}
			case "tool_use":
				name, _ := blk["name"].(string)
				input, _ := blk["input"].(map[string]any)
				ev.Progress = clip(name+" "+firstString(input, "file_path", "path", "command", "pattern", "url", "description", "query"), 200)
			}
		}
		if len(said) > 0 {
			ev.Text = strings.Join(said, "\n")
			if ev.Progress == "" {
				ev.Progress = "says: " + clip(ev.Text, 200)
			}
		}
		return ev
	case "system", "user":
		return Event{Known: true} // setup and tool results: nothing to show
	case "rate_limit_event":
		ev := Event{Known: true, Progress: claudeUsage(m)}
		if u := ParseClaudeUsage(m); u != nil {
			ev.Usage = u
		}
		return ev
	}
	// Any other event type (the stream has several kinds Unire does not use) is
	// part of the stream, so it must never be printed as if it were the answer.
	if _, ok := m["type"].(string); ok {
		return Event{Known: true}
	}
	return Event{}
}

// claudeUsage says how much of the subscription's limits are used, from the
// rate_limit_event Claude Code sends: {"rate_limit_info": {"unifiedWindows":
// {"five_hour": {"utilization": 0.34}, "seven_day": {"utilization": 0.72}}}}.
func claudeUsage(m map[string]any) string {
	info, _ := m["rate_limit_info"].(map[string]any)
	windows, _ := info["unifiedWindows"].(map[string]any)
	var parts []string
	for _, w := range []struct{ key, label string }{{"five_hour", "5-hour"}, {"seven_day", "7-day"}} {
		win, _ := windows[w.key].(map[string]any)
		if u, ok := win["utilization"].(float64); ok {
			parts = append(parts, fmt.Sprintf("%.0f%% of the %s limit", u*100, w.label))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "usage: " + strings.Join(parts, ", ")
}

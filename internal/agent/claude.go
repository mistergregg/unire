package agent

import (
	"fmt"
	"strings"
)

type claude struct{}

func init() { Register(claude{}) }

func (claude) Name() string           { return "claude" }
func (claude) MemoryFile() string     { return "CLAUDE.md" }
func (claude) MemoryMode() MemoryMode { return MemoryFile }

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

func (claude) Command(prompt, mode, ref string) ([]string, error) {
	// --session-id stores the conversation under ref so FollowUp can resume
	// it. Conversations share one home folder across sessions, so resuming by
	// id, never "the latest", keeps sessions from mixing.
	id := []string{}
	if ref != "" {
		id = []string{"--session-id", ref}
	}
	switch mode {
	case HandsOff:
		if prompt == "" {
			return append([]string{"claude", "--dangerously-skip-permissions"}, id...), nil
		}
		// -p runs once and exits, which is what an unattended session needs.
		// "--" keeps a prompt that starts with a dash from being read as a flag.
		argv := append([]string{"claude", "-p", "--dangerously-skip-permissions"}, id...)
		return append(argv, "--", prompt), nil
	case AskFirst:
		argv := append([]string{"claude"}, id...)
		if prompt == "" {
			return argv, nil
		}
		return append(argv, "--", prompt), nil
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

func (claude) FollowUp(prompt, mode, ref string) ([]string, bool) {
	if ref == "" || prompt == "" || mode != HandsOff {
		return nil, false
	}
	return []string{"claude", "-p", "--dangerously-skip-permissions", "--resume", ref, "--", prompt}, true
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
	switch m["type"] {
	case "result":
		ans, _ := m["result"].(string)
		return Event{Known: true, Answer: ans}
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
	}
	return Event{}
}

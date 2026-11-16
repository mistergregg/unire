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
		Description: "Anthropic's coding agent. Uses your Claude subscription.",
		LoginHelp:   "It prints a link. Open it on any device, sign in to your Claude account, then paste the code it shows back into the terminal.",
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

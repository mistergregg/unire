package agent

import "fmt"

type claude struct{}

func init() { Register(claude{}) }

func (claude) Name() string       { return "claude" }
func (claude) MemoryFile() string { return "CLAUDE.md" }

// Hosts are the services Claude Code needs. api.anthropic.com and
// mcp-proxy.anthropic.com were seen in a real session through the egress
// proxy; claude.ai and console.anthropic.com are its login services.
// Telemetry hosts (such as datadog) are deliberately not listed. If a session
// in agent-only mode needs another host, the owner can allow it from the log.
func (claude) Hosts() []string {
	return []string{"api.anthropic.com", "mcp-proxy.anthropic.com", "console.anthropic.com", "claude.ai"}
}

func (claude) Command(prompt, mode string) ([]string, error) {
	switch mode {
	case HandsOff:
		if prompt == "" {
			return []string{"claude", "--dangerously-skip-permissions"}, nil
		}
		// -p runs once and exits, which is what an unattended session needs.
		// "--" keeps a prompt that starts with a dash from being read as a flag.
		return []string{"claude", "-p", "--dangerously-skip-permissions", "--", prompt}, nil
	case AskFirst:
		if prompt == "" {
			return []string{"claude"}, nil
		}
		return []string{"claude", "--", prompt}, nil
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

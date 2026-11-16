package agent

import "fmt"

type claude struct{}

func init() { Register(claude{}) }

func (claude) Name() string       { return "claude" }
func (claude) MemoryFile() string { return "CLAUDE.md" }

func (claude) Command(prompt, mode string) ([]string, error) {
	switch mode {
	case HandsOff:
		if prompt == "" {
			return []string{"claude", "--dangerously-skip-permissions"}, nil
		}
		// -p runs once and exits, which is what an unattended session needs.
		return []string{"claude", "-p", prompt, "--dangerously-skip-permissions"}, nil
	case AskFirst:
		if prompt == "" {
			return []string{"claude"}, nil
		}
		return []string{"claude", prompt}, nil
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

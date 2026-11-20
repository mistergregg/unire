package agent

import (
	"fmt"
	"strings"
)

// Codex is OpenAI's coding agent (the Codex CLI). Its command line was read
// from the CLI's own --help (codex-cli 0.169): `codex exec` runs unattended,
// and the sandbox flags are turned off because Unire's container is the sandbox.
type codex struct {
	Model string
}

func init() { Register(codex{}) }

func (codex) Name() string           { return "codex" }
func (codex) MemoryFile() string     { return "AGENTS.md" }
func (codex) MemoryMode() MemoryMode { return MemoryPrompt } // AGENTS.md discovery outside a git repo is unverified

func (c codex) WithModel(model string) Adapter {
	c.Model = model
	return c
}

func (codex) Info() Info {
	return Info{
		ID: "codex", Label: "Codex",
		Description: "OpenAI. Uses your ChatGPT subscription.",
		LoginHelp:   "Open the link, sign in to ChatGPT, and enter the one-time code it shows.",
		LoginArgv:   []string{"codex", "login", "--device-auth"},
		StatusArgv:  []string{"codex", "login", "status"},
		LoggedIn: func(out string, ok bool) bool {
			o := strings.ToLower(out)
			return ok && strings.TrimSpace(o) != "" && !strings.Contains(o, "not logged in")
		},
		InspectArgv: []string{"codex", "login", "status"},
		ResetArgv:   []string{"codex", "logout"},
		ResetHelp:   "Signs Codex out of the saved login.",
		Models:      []string{"gpt-6-luna", "gpt-5.6-terra", "gpt-5.6-luna"},
		// The status text was seen when signed out; the signed-in text and a
		// real unattended run need an account to check.
		Verified: Verified{},
	}
}

// Hosts are Codex's likely services (ChatGPT sign-in and the OpenAI API).
// They have not been measured with a real session, so agent-only mode may need
// an address allowed from the log.
func (codex) Hosts() []string {
	return []string{"api.openai.com", "auth.openai.com", "chatgpt.com"}
}

func (c codex) Command(prompt, mode, ref string) ([]string, error) {
	var model []string
	if c.Model != "" {
		model = []string{"--model", c.Model}
	}
	switch mode {
	case HandsOff:
		if prompt == "" {
			return append([]string{"codex", "--dangerously-bypass-approvals-and-sandbox"}, model...), nil
		}
		// /workspace is not a git repo (the repos are inside it), hence
		// --skip-git-repo-check. "--" keeps a dash-leading prompt from being a flag.
		argv := append([]string{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check"}, model...)
		return append(argv, "--", prompt), nil
	case AskFirst:
		if prompt == "" {
			return append([]string{"codex"}, model...), nil
		}
		argv := append([]string{"codex"}, model...)
		return append(argv, "--", prompt), nil
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

// FollowUp: Codex chooses its own conversation id, and "resume the latest"
// would pick up another session's conversation (they share one home folder),
// so a follow-up starts a fresh conversation that is told what happened.
func (codex) FollowUp(prompt, mode, ref string) ([]string, bool) { return nil, false }

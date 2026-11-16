package agent

import "fmt"

// Gemini is Google's Gemini CLI. Its command line was read from the CLI's own
// --help (gemini 0.68): -p runs unattended, --approval-mode yolo approves
// everything, and --session-id names the conversation.
type gemini struct{}

func init() { Register(gemini{}) }

func (gemini) Name() string           { return "gemini" }
func (gemini) MemoryFile() string     { return "GEMINI.md" }
func (gemini) MemoryMode() MemoryMode { return MemoryPrompt } // GEMINI.md loading is unverified

func (gemini) Info() Info {
	return Info{
		ID: "gemini", Label: "Gemini CLI",
		Description: "Google's Gemini coding agent. Uses your Google account.",
		LoginHelp:   "Choose \"Sign in with Google\". It prints a link: open it on any device, sign in, and paste the code back. Then type /quit.",
		// --skip-trust avoids the "trust this folder" question before sign-in.
		LoginArgv: []string{"gemini", "--skip-trust"},
		// Gemini has no status command. A sign-in leaves this file behind.
		StatusArgv: []string{"test", "-f", "/home/node/.gemini/oauth_creds.json"},
		LoggedIn:   func(_ string, ok bool) bool { return ok },
		Verified:   Verified{},
	}
}

// Hosts are Gemini's likely services (Google sign-in and the Gemini API). They
// have not been measured with a real session.
func (gemini) Hosts() []string {
	return []string{"generativelanguage.googleapis.com", "cloudcode-pa.googleapis.com", "oauth2.googleapis.com", "accounts.google.com", "www.googleapis.com"}
}

func (gemini) Command(prompt, mode, ref string) ([]string, error) {
	// --skip-trust avoids the folder-trust prompt, which would hang an unattended run.
	base := []string{"gemini", "--skip-trust"}
	if ref != "" {
		base = append(base, "--session-id", ref)
	}
	switch mode {
	case HandsOff:
		base = append(base, "--approval-mode", "yolo")
		if prompt == "" {
			return base, nil
		}
		// --prompt=<text> keeps a dash-leading prompt from being read as a flag.
		return append(base, "--prompt="+prompt), nil
	case AskFirst:
		if prompt == "" {
			return base, nil
		}
		return append(base, "--prompt-interactive="+prompt), nil
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

// FollowUp: Gemini resumes by list position or "latest", not by the id we
// gave it, and "latest" could be another session's conversation, so a
// follow-up starts a fresh conversation that is told what happened.
func (gemini) FollowUp(prompt, mode, ref string) ([]string, bool) { return nil, false }

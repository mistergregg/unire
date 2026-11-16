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

// geminiLoginScript prepares Gemini's settings, then starts it. Gemini opens
// with a menu (1 Sign in with Google, 2 Gemini API key, 3 Vertex AI) where the
// number keys are shortcuts, so pasting a sign-in code while the menu is up can
// jump to "Enter Gemini API Key" (seen in practice). Choosing Google here, in
// Gemini's own settings file, skips the menu and goes straight to the link and
// the code prompt. It replaces any earlier choice (such as an API key picked by
// mistake) and keeps the other settings. --skip-trust avoids the "trust this
// folder" question.
const geminiLoginScript = `node -e 'const fs=require("fs"),p=process.env.HOME+"/.gemini/settings.json";let s={};try{s=JSON.parse(fs.readFileSync(p,"utf8"))}catch(e){}s.security=s.security||{};s.security.auth=s.security.auth||{};s.security.auth.selectedType="oauth-personal";fs.mkdirSync(require("path").dirname(p),{recursive:true});fs.writeFileSync(p,JSON.stringify(s,null,2)+"\n")' && exec gemini --skip-trust`

func (gemini) Info() Info {
	return Info{
		ID: "gemini", Label: "Gemini CLI",
		Description: "Google's Gemini coding agent. Uses your Google account.",
		LoginHelp:   "It prints a long Google link and waits at \"Enter the authorization code:\". Click the link, sign in with Google, copy the code Google shows you, then paste it at that prompt (Ctrl+Shift+V) and press Enter. Wait for the prompt before pasting. When it says you are signed in, type /quit.",
		LoginArgv:   []string{"sh", "-c", geminiLoginScript},
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

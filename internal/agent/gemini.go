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

// Why an API key: Google has ended Gemini CLI's sign-in for personal Google
// accounts. After a successful Google sign-in it answers "This client is no
// longer supported for Gemini Code Assist for individuals ... migrate to the
// Antigravity suite" (seen in practice). A Gemini API key from Google AI Studio
// still works, and the CLI keeps it in its own credentials file in the login
// volume. That file can only be read by containers with the same hostname as
// the one that wrote it (a different hostname gives "Corrupted credentials
// file", seen in practice), which is why every Unire container has a fixed
// hostname.
//
// geminiLoginScript sets Gemini's own sign-in choice to "API key" and starts it,
// which skips its three-way menu (where number keys are shortcuts) and opens the
// key box directly. It replaces any earlier choice and keeps other settings.
// --skip-trust avoids the "trust this folder" question.
const geminiLoginScript = `node -e 'const fs=require("fs"),p=process.env.HOME+"/.gemini/settings.json";let s={};try{s=JSON.parse(fs.readFileSync(p,"utf8"))}catch(e){}s.security=s.security||{};s.security.auth=s.security.auth||{};s.security.auth.selectedType="gemini-api-key";fs.mkdirSync(require("path").dirname(p),{recursive:true});fs.writeFileSync(p,JSON.stringify(s,null,2)+"\n")' && exec gemini --skip-trust`

// geminiInspectScript prints what Gemini has saved, without any secret: file
// names and sizes, the settings file (it holds the chosen sign-in type, not a
// key), and whether an API key variable is set in the sandbox.
const geminiInspectScript = `d="$HOME/.gemini"; echo "files in ~/.gemini (name, bytes):"; for f in "$d"/* "$d"/.[!.]*; do [ -f "$f" ] && echo "  $(basename "$f")  $(wc -c < "$f")"; done; echo; echo "settings.json:"; cat "$d/settings.json" 2>/dev/null || echo "  (none)"; echo; echo "API key variables set in this sandbox: $(env | grep -c '^GEMINI_API_KEY=\|^GOOGLE_API_KEY=')"`

// geminiResetScript removes Gemini's sign-in, stored API key and settings.
const geminiResetScript = `d="$HOME/.gemini"; rm -f "$d/settings.json" "$d/gemini-credentials.json" "$d/oauth_creds.json" "$d/state.json" "$d/mcp-oauth-tokens.json" && echo "Gemini's saved sign-in, API key and settings were removed."`

func (gemini) Info() Info {
	return Info{
		ID: "gemini", Label: "Gemini CLI",
		Description: "Google's Gemini coding agent, used with a Gemini API key from Google AI Studio (it has a free tier). Google has ended Google-account sign-in for personal accounts in Gemini CLI and points to Antigravity instead.",
		LoginHelp:   "Create a key at aistudio.google.com/app/apikey (click the link), then paste it into the box in this terminal with Ctrl+Shift+V and press Enter. It is saved in the login volume. When the Gemini prompt appears, type /quit. Nothing happens to your key outside your own server except calls to Google's Gemini API.",
		LoginArgv:   []string{"sh", "-c", geminiLoginScript},
		// Gemini has no status command. Storing a key leaves this file behind.
		StatusArgv:  []string{"test", "-s", "/home/node/.gemini/gemini-credentials.json"},
		LoggedIn:    func(_ string, ok bool) bool { return ok },
		InspectArgv: []string{"sh", "-c", geminiInspectScript},
		ResetArgv:   []string{"sh", "-c", geminiResetScript},
		ResetHelp:   "Deletes Gemini's saved sign-in, stored API key and settings, so the next sign-in starts clean. Its history is kept.",
		Verified:    Verified{},
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

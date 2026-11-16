package agent

import "fmt"

// Antigravity is Google's Antigravity CLI (`agy`), the replacement Google points
// to now that Gemini CLI no longer accepts personal Google accounts. Its command
// line was read from the CLI's own --help (agy 1.5.9) and exercised up to its
// sign-in screen; nothing here has been run with a signed-in account yet.
//
// What was seen directly:
//   - `agy --print=<prompt>` runs one prompt and exits; `--dangerously-skip-permissions`
//     approves everything.
//   - `-p <prompt>` and even `-p -- <prompt>` read a prompt that starts with a
//     dash as a FLAG (`-p "--version"` printed the version), so the prompt must be
//     attached with `--print=`, which treated every tricky prompt as text.
//   - Not signed in, it prints a Google link and says "paste the authorization code
//     here". The link's redirect is https://antigravity.google/oauth-callback (a
//     normal web page, not localhost), so signing in from another machine's
//     browser can work.
//   - Interactively it first shows "Select login method: 1. Google OAuth,
//     2. Use a Google Cloud project".
type antigravity struct{}

func init() { Register(antigravity{}) }

func (antigravity) Name() string           { return "antigravity" }
func (antigravity) MemoryFile() string     { return "AGENTS.md" } // unverified
func (antigravity) MemoryMode() MemoryMode { return MemoryPrompt }

// Where agy keeps its sign-in is not known yet (an OS keyring was reported for
// Linux). These scripts only touch paths with "antigravity" or "keyring" in
// their name, and print file names and sizes, never contents.
const antigravityInspectScript = `echo "files that look like Antigravity or keyring state (path, bytes):"; cd "$HOME" && find . -maxdepth 6 -type f \( -ipath "./*antigravity*" -o -ipath "./*keyring*" \) -not -ipath "*/staging/*" -not -ipath "*/builtin/*" -printf "  %P  %s\n" 2>/dev/null | head -40; echo; echo "API key variables set in this sandbox: $(env | grep -c '^GEMINI_API_KEY=\|^GOOGLE_API_KEY=')"`

const antigravityResetScript = `rm -rf "$HOME/.gemini/antigravity-cli" "$HOME"/.antigravity* "$HOME"/.config/antigravity* "$HOME"/.local/share/antigravity* "$HOME/.local/share/keyrings" && echo "Antigravity's saved sign-in and settings were removed."`

func (antigravity) Info() Info {
	return Info{
		ID: "antigravity", Label: "Antigravity CLI",
		Description: "Google's coding agent, the replacement for Gemini CLI. Uses your Google account.",
		LoginHelp:   "Press Enter on \"1. Google OAuth\". It prints a Google link and asks you to paste the authorization code. Click the link, sign in, and copy the code the page shows you. Wait until the terminal asks for it, then paste it (Ctrl+Shift+V) and press Enter. When you are signed in, press Ctrl+D twice to leave.",
		LoginArgv:   []string{"agy"},
		// agy has no sign-in status command, and where it keeps its login is not
		// known yet, so the page reports the sign-in as unknown.
		InspectArgv: []string{"sh", "-c", antigravityInspectScript},
		ResetArgv:   []string{"sh", "-c", antigravityResetScript},
		ResetHelp:   "Deletes Antigravity's saved sign-in and settings, so the next sign-in starts clean.",
		Verified:    Verified{},
	}
}

// Hosts are Antigravity's likely services. They have not been measured with a
// real session, so agent-only mode may need an address allowed from the log.
func (antigravity) Hosts() []string {
	return []string{
		"antigravity.google", "accounts.google.com", "oauth2.googleapis.com", "www.googleapis.com",
		"cloudcode-pa.googleapis.com", "daily-cloudcode-pa.googleapis.com",
		"antigravity-cli-auto-updater-974169037036.us-central1.run.app",
	}
}

func (antigravity) Command(prompt, mode, ref string) ([]string, error) {
	switch mode {
	case HandsOff:
		if prompt == "" {
			return []string{"agy", "--dangerously-skip-permissions"}, nil
		}
		// --print=<prompt> keeps a prompt that starts with a dash from being
		// read as a flag, which -p <prompt> and -p -- <prompt> do not.
		return []string{"agy", "--dangerously-skip-permissions", "--print=" + prompt}, nil
	case AskFirst:
		if prompt == "" {
			return []string{"agy"}, nil
		}
		return []string{"agy", "--prompt-interactive=" + prompt}, nil
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

// FollowUp: agy resumes by a conversation id that it chooses (--conversation <id>),
// or "the most recent" (--continue), which could be another session's
// conversation because sessions share one home folder. So a follow-up starts a
// fresh conversation that is told what happened.
func (antigravity) FollowUp(prompt, mode, ref string) ([]string, bool) { return nil, false }

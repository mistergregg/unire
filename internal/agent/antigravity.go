package agent

import "fmt"

// Antigravity is Google's Antigravity CLI (`agy`), the replacement Google points
// to now that Gemini CLI no longer accepts personal Google accounts (Unire used
// to carry Gemini CLI; it was removed for that reason). The command line was
// read from the CLI's own --help (agy 1.5.9) and exercised up to its sign-in
// screen; nothing here has been run with a signed-in account yet.
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
//     browser works.
//   - Interactively it first shows "Select login method: 1. Google OAuth,
//     2. Use a Google Cloud project".
//   - It keeps its login in the Linux Secret Service keyring when there is one
//     and otherwise reads an auth.json file ("failed to read auth.json" is in the
//     binary). A container has no keyring, so the file is what persists.
type antigravity struct{}

func init() { Register(antigravity{}) }

func (antigravity) Name() string           { return "antigravity" }
func (antigravity) MemoryFile() string     { return "AGENTS.md" } // unverified
func (antigravity) MemoryMode() MemoryMode { return MemoryPrompt }

// The scripts below only look at paths with "antigravity" or "keyring" in their
// name (below the home folder, never the home folder's own name), skip the
// built-in skills, cache and crash logs, and print file names and sizes, never
// contents.
const antigravityFind = `cd "$HOME" && find . -maxdepth 6 -type f -ipath "./*antigravity*" -not -ipath "*/builtin/*" -not -ipath "*/cache/*" -not -ipath "*/crashes/*" -not -ipath "*/staging/*"`

// antigravityStatusScript succeeds when a non-empty saved login file exists.
// The file name (auth.json) comes from the binary, not from a real sign-in, so
// this check is marked unverified.
const antigravityStatusScript = antigravityFind + ` -size +0 \( -iname "auth*.json" -o -iname "*credential*" \) | grep -q .`

const antigravityInspectScript = `echo "Antigravity files (name, bytes):"; ` + antigravityFind + ` -printf "  %P  %s\n" 2>/dev/null | head -40; cd "$HOME" && find . -maxdepth 6 -type f -ipath "./*keyring*" -printf "  %P  %s\n" 2>/dev/null | head -5; echo; echo "API key variables set in this sandbox: $(env | grep -c '^GEMINI_API_KEY=\|^GOOGLE_API_KEY=')"`

const antigravityResetScript = `rm -rf "$HOME/.gemini/antigravity-cli" "$HOME"/.antigravity* "$HOME"/.config/antigravity* "$HOME"/.local/share/antigravity* "$HOME/.local/share/keyrings" && echo "Antigravity's saved sign-in and settings were removed."`

func (antigravity) Info() Info {
	return Info{
		ID: "antigravity", Label: "Antigravity CLI",
		Description: "Google. Uses your Google account.",
		LoginHelp:   "Press Enter on Google OAuth, click the link, sign in, then paste the code it shows (Ctrl+Shift+V) and press Enter. Press Ctrl+D twice to leave.",
		LoginArgv:   []string{"agy"},
		StatusArgv:  []string{"sh", "-c", antigravityStatusScript},
		LoggedIn:    func(_ string, ok bool) bool { return ok },
		InspectArgv: []string{"sh", "-c", antigravityInspectScript},
		ResetArgv:   []string{"sh", "-c", antigravityResetScript},
		ResetHelp:   "Deletes Antigravity's saved sign-in and settings.",
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

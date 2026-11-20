package agent

import (
	"fmt"
	"sort"
	"strings"
)

// Antigravity is Google's Antigravity CLI (`agy`), the replacement Google points
// to now that Gemini CLI no longer accepts personal Google accounts (Unire used
// to carry Gemini CLI; it was removed for that reason). The command line was
// read from the CLI's own --help (agy 1.5.9). It has since run signed in, unattended
// and steered, on real work: it reads files and images, runs the unire tools (name,
// ask, share, memory, pr, pr-body), commits and pushes, and carries on its
// conversation in a reply.
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
type antigravity struct {
	Model string
}

func init() { Register(antigravity{}) }

func (antigravity) Name() string           { return "antigravity" }
func (antigravity) MemoryFile() string     { return "AGENTS.md" } // unverified: memory goes in the prompt (MemoryPrompt)
func (antigravity) MemoryMode() MemoryMode { return MemoryPrompt }

func (a antigravity) WithModel(model string) Adapter {
	a.Model = model
	return a
}

// The scripts below only look at paths with "antigravity" or "keyring" in their
// name (below the home folder, never the home folder's own name), skip the
// built-in skills, cache and crash logs, and print file names and sizes, never
// contents.
const antigravityFind = `cd "$HOME" && find . -maxdepth 6 -type f -ipath "./*antigravity*" -not -ipath "*/builtin/*" -not -ipath "*/cache/*" -not -ipath "*/crashes/*" -not -ipath "*/staging/*"`

// antigravityStatusScript succeeds when a non-empty saved login file exists.
// The file name (auth.json) comes from the binary, not from a real sign-in, so
// this check is marked unverified.
const antigravityStatusScript = antigravityFind + ` -size +0 \( -iname "auth*.json" -o -iname "*credential*" \) | grep -q .`

// agy writes a log file for every run, and they are kept, so the listing leaves them
// out and counts them: otherwise they filled it and the sign-in files never showed.
const antigravityInspectScript = `echo "Antigravity files (name, bytes):"; ` + antigravityFind + ` -not -ipath "*/log/*" -printf "  %P  %s\n" 2>/dev/null | head -40; echo "  (and $(` + antigravityFind + ` -ipath "*/log/*" 2>/dev/null | wc -l) log files)"; cd "$HOME" && find . -maxdepth 6 -type f -ipath "./*keyring*" -printf "  %P  %s\n" 2>/dev/null | head -5; echo; echo "API key variables set in this sandbox: $(env | grep -c '^GEMINI_API_KEY=\|^GOOGLE_API_KEY=')"`

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
		Models:      []string{"gemini-3.8-flash-high", "gemini-3.8-flash-medium", "gemini-3.8-flash-low", "claude-sonnet-4-6", "claude-opus-4-6-thinking"},
		Verified:    Verified{Unattended: true}, // signed-in unattended runs seen working (internet mode)
	}
}

// Hosts are Antigravity's services. A real session in internet mode connected to
// www.googleapis.com, daily-cloudcode-pa, aicode, play.googleapis.com and
// lh3.googleusercontent.com; the last two (telemetry and pictures) are not listed.
// Agent-only mode has not been run, so an address may still need allowing from the log.
func (antigravity) Hosts() []string {
	return []string{
		"antigravity.google", "accounts.google.com", "oauth2.googleapis.com", "www.googleapis.com",
		"cloudcode-pa.googleapis.com", "daily-cloudcode-pa.googleapis.com", "aicode.googleapis.com",
		"antigravity-cli-auto-updater-974169037036.us-central1.run.app",
	}
}

func (a antigravity) Command(prompt, mode, ref string) ([]string, error) {
	var model []string
	if a.Model != "" {
		model = []string{"--model", a.Model}
	}
	switch mode {
	case HandsOff:
		if prompt == "" {
			return append([]string{"agy", "--dangerously-skip-permissions"}, model...), nil
		}
		// --print=<prompt> keeps a prompt that starts with a dash from being
		// read as a flag, which -p <prompt> and -p -- <prompt> do not.
		argv := append([]string{"agy", "--dangerously-skip-permissions"}, model...)
		return append(argv, "--print="+prompt), nil
	case AskFirst:
		if prompt == "" {
			return append([]string{"agy"}, model...), nil
		}
		argv := append([]string{"agy"}, model...)
		return append(argv, "--prompt-interactive="+prompt), nil
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

// FollowUp: agy resumes by a conversation id that it chooses (--conversation <id>),
// or "the most recent" (--continue), which could be another session's
// conversation because sessions share one home folder. So a follow-up starts a
// fresh conversation that is told what happened.
func (antigravity) FollowUp(prompt, mode, ref string) ([]string, bool) { return nil, false }

// StreamCommand asks agy for its event stream (one JSON object per line).
func (a antigravity) StreamCommand(prompt, mode, ref string) ([]string, error) {
	argv, err := a.Command(prompt, mode, ref)
	if err != nil || mode != HandsOff || prompt == "" {
		return argv, err
	}
	// Command is: agy --dangerously-skip-permissions [--model <model>] --print=<prompt>
	out := []string{argv[0], argv[1], "--output-format=stream-json"}
	return append(out, argv[2:]...), nil
}

// ParseEvent reads agy's stream-json (documented at
// antigravity.google/docs/cli/headless). Each line is {"event": "<kind>",
// "<kind>": {...}}: one "init", then a "step_update" for each step (a tool call,
// a piece of the agent's reply, a checkpoint), then one "result" carrying the
// final answer. Its reasoning is not streamed; what it does (the tools it runs
// and the files it reads) is.
func (antigravity) ParseEvent(line string) Event {
	m, ok := parseObject(line)
	if !ok {
		return Event{}
	}
	kind, _ := m["event"].(string)
	switch kind {
	case "":
		return Event{}
	case "init":
		return Event{Known: true}
	case "result":
		res, _ := m["result"].(map[string]any)
		ev := Event{Known: true, Answer: firstString(res, "response")}
		status, _ := res["status"].(string)
		switch e := firstString(res, "error"); {
		case status == "INTERRUPTED" || status == "CANCELED" || strings.EqualFold(strings.TrimSpace(e), "interrupted"):
			ev.Progress = "interrupted" // stopped on purpose (a message sent now), not a failure
		case e != "":
			ev.Progress = "agent error: " + clip(e, 300)
		case ev.Answer == "" && status != "" && status != "SUCCESS":
			ev.Progress = "finished with status " + status
		}
		return ev
	case "step_update":
		step, _ := m["step_update"].(map[string]any)
		return antigravityStep(step)
	}
	// Some other kind: show it by name with whatever text it carries.
	detail := firstString(m, "text", "message", "content", "name", "title", "summary")
	p := kind
	if detail != "" {
		p += ": " + clip(detail, 200)
	}
	return Event{Known: true, Progress: p}
}

// antigravityStep turns one step_update into a progress line.
func antigravityStep(step map[string]any) Event {
	ev := Event{Known: true}
	state, _ := step["state"].(string)
	done := state == "DONE"
	n, _ := step["step_index"].(float64)
	ev.Step = int(n)
	if _, isSub := step["subagent_info"]; isSub {
		if done {
			ev.Progress = "subagent finished"
		}
		return ev
	}
	switch step["step_type"] {
	case "tool":
		name := firstString(step, "tool_name")
		info, _ := step["tool_info"].(map[string]any)
		if name == "" {
			name = firstString(info, "name")
		}
		if name == "" {
			return ev
		}
		params, _ := info["parameters"].(map[string]any)
		p := name
		if d := toolDetail(params); d != "" {
			p += " " + clip(d, 160)
		}
		if errInfo, ok := info["error"].(map[string]any); ok && done {
			p += " (failed: " + clip(firstString(errInfo, "message", "type"), 120) + ")"
		}
		ev.Progress = p
	case "agent_response":
		delta, _ := step["text_delta"].(string)
		if !done {
			ev.Fragment = delta // part of the reply; the final answer comes in the result
			return ev
		}
		ev.Fragment = delta
		if strings.TrimSpace(delta) != "" {
			ev.Text = delta
			ev.Progress = "says: " + clip(delta, 200)
		}
	}
	return ev
}

// toolDetail picks the argument that says what a tool call is about (a command,
// a file, a query), whatever the tool calls it.
func toolDetail(params map[string]any) string {
	for _, k := range []string{"CommandLine", "command", "AbsolutePath", "TargetFile", "File", "Path", "path", "DirectoryPath", "SearchPath", "Query", "query", "Pattern", "Url", "url", "description"} {
		if v, ok := params[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v, ok := params[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

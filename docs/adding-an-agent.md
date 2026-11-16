# Adding an agent

An agent adapter teaches Unire how to run one coding-agent CLI: how to start it unattended, how to sign it in, and how to tell whether it is signed in. Adding one is a Go file plus a line in the sandbox image. The Agents page, the New session list and the memory step all pick it up from the adapter.

## 1. Put the CLI in the sandbox image

Add it to the `npm install -g ...` line (or an equivalent install step) in `docker/sandbox/Dockerfile`. Check `<cli> --version` works as the `node` user.

## 2. Read the CLI's own help

Run the real CLI's `--help` in the image and write down:

- the flag that runs **one prompt and exits** (headless / non-interactive),
- the flag that **approves everything** (Unire's sandbox is the safety boundary),
- how a **prompt that starts with a dash** is passed safely (`--` before it, or `--prompt=<text>`),
- the **sign-in command** that works in a terminal (prefer the one that prints a link and a code, so it works without a browser on the server),
- a **sign-in status** command, or the file a sign-in leaves behind,
- whether it can **resume a conversation by an id you choose**. If it can only resume "the latest", do not use that: sessions share one home folder, so "latest" could be another session's conversation. Return `ok=false` from `FollowUp` and Unire starts a fresh conversation that is told what happened.

## 3. Write `internal/agent/<name>.go`

```go
type myagent struct{}

func init() { Register(myagent{}) }

func (myagent) Name() string            { return "myagent" }
func (myagent) MemoryFile() string      { return "MYAGENT.md" }
func (myagent) MemoryMode() MemoryMode  { return MemoryPrompt } // use MemoryFile only after verifying the CLI loads that file

func (myagent) Info() Info {
	return Info{
		ID: "myagent", Label: "My Agent", Description: "Who makes it. Which subscription it uses.",
		LoginHelp:  "What the owner will see in the terminal and what to do.",
		LoginArgv:  []string{"myagent", "login"},
		StatusArgv: []string{"myagent", "whoami"},
		LoggedIn:   func(out string, ok bool) bool { return ok && !strings.Contains(out, "not logged in") },
		Verified:   Verified{}, // fill in only what you have actually checked with a real account
	}
}

func (myagent) Hosts() []string { return []string{"api.example.com"} }

func (myagent) Command(prompt, mode, ref string) ([]string, error) { /* argv for hands-off and ask-first */ }
func (myagent) FollowUp(prompt, mode, ref string) ([]string, bool)  { return nil, false }
```

`Verified` is shown to the owner. Leave a field `false` until you have run the real thing: `Unattended` after a real unattended session worked, `Status` after you saw the status command report both states, `Hosts` after you measured the hosts a real session contacts (the session log lists them as "connected to ..."). An agent whose sign-in check is not verified never blocks a launch.

## 4. Test

Add argv tests next to the others in `internal/agent/agents_test.go`: every mode, a dash-leading prompt, and `FollowUp`. `TestAgentsAreCompleteAndSorted` checks the metadata and `TestUnverifiedAgentsSaySo` keeps you honest about `Verified`.

## What you get

The agent appears on the **Agents** page with its sign-in state and a **Sign in** button that runs `LoginArgv` in a terminal in the browser, shows in the **New session** list (with "not signed in" when known), gets project memory, and is covered by the network modes through `Hosts`.

## Not agents

- **Ollama** runs models; it is not an agent. It would be a model backend for an agent that can use one (Claude Code and Codex can), configured per agent. Not built yet.
- **Antigravity** (`agy`) keeps its sign-in in the operating system's keyring, which a plain container does not have, so a subscription sign-in is not expected to persist there. It could be added with an API key. Not built yet.

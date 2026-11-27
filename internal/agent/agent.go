// Package agent holds agent adapters: how to launch each agent CLI, how to
// sign it in, and how to tell whether it is signed in. Each agent is one file
// that registers itself; the rest of Unire (sessions, the Agents page) only
// uses this interface. See docs/adding-an-agent.md.
package agent

import (
	"fmt"
	"regexp"
	"sort"
)

const (
	HandsOff = "hands-off" // agent runs with its approve-everything mode
	AskFirst = "ask-first" // agent asks before risky actions
)

// MemoryMode is how an agent is given project memory.
type MemoryMode string

const (
	// MemoryFile writes the memory to the agent's native context file in
	// /workspace, which the agent loads on its own.
	MemoryFile MemoryMode = "file"
	// MemoryPrompt puts the memory at the start of the prompt. It works for
	// any agent, so it is the safe choice for one whose file loading has not
	// been verified.
	MemoryPrompt MemoryMode = "prompt"
)

// Info describes an agent to the Agents page and to the rest of Unire.
type Info struct {
	ID          string `json:"id"`    // stable id, e.g. "codex"
	Label       string `json:"label"` // shown in the UI
	Description string `json:"description"`
	// LoginHelp tells the owner what they will see in the login terminal.
	LoginHelp string `json:"loginHelp"`
	// LoginArgv is run in the terminal to sign the agent in with its own
	// subscription login.
	LoginArgv []string `json:"-"`
	// StatusArgv is run (without a terminal) to find out whether the agent is
	// signed in; empty if the agent has no way to say. LoggedIn reads its output.
	StatusArgv []string                              `json:"-"`
	LoggedIn   func(output string, exitOK bool) bool `json:"-"`
	// InspectArgv prints what the agent has saved (never secrets) so a stuck
	// sign-in can be diagnosed. ResetArgv clears the agent's saved sign-in and
	// settings so the owner can start clean. Both run with no network, in a
	// throwaway sandbox that has the login volume mounted.
	InspectArgv []string `json:"-"`
	ResetArgv   []string `json:"-"`
	// UsageArgv, when set, prints the agent's usage limits without using any of
	// them; ParseUsage reads its output. It needs the network and the login
	// volume, and runs in a throwaway sandbox with nothing else mounted.
	UsageArgv  []string                                 `json:"-"`
	ParseUsage func(output string) (*AgentUsage, error) `json:"-"`
	// ResetHelp says what a reset removes.
	ResetHelp string `json:"resetHelp"`
	// Models lists known model names to offer in the UI.
	Models []string `json:"models"`
	// ModelsArgv, when set, prints the models the CLI currently offers (without
	// running a turn or spending tokens); ParseModels reads its output. The
	// server refreshes this list in the background and falls back to Models.
	ModelsArgv         []string                              `json:"-"`
	ParseModels        func(output string) ([]string, error) `json:"-"`
	ModelsRequireLogin bool                                  `json:"-"`
	// Verified says what has been checked against the real agent.
	Verified Verified `json:"verified"`
}

// Verified records which parts of an adapter were confirmed by running the real
// agent, so the UI can be honest about the rest.
type Verified struct {
	Unattended bool `json:"unattended"` // a real unattended run worked
	Status     bool `json:"status"`     // the login check was confirmed
	Hosts      bool `json:"hosts"`      // the list of hosts it needs was measured
}

// Adapter knows how to run one agent CLI inside a sandbox.
type Adapter interface {
	Name() string
	Info() Info
	// Command returns the argv to run in the workspace. An empty prompt means
	// an interactive session. ref is a unique id (a UUID) the agent can store
	// the conversation under, so FollowUp can continue exactly that one.
	Command(prompt, mode, ref string) ([]string, error)
	// FollowUp returns the argv that continues the conversation started with
	// ref and sends it one more prompt. ok is false when the agent cannot
	// resume a conversation; the caller then starts a fresh one.
	FollowUp(prompt, mode, ref string) (argv []string, ok bool)
	// MemoryFile is the agent's native context file name.
	MemoryFile() string
	// MemoryMode says how to give the agent its memory.
	MemoryMode() MemoryMode
	// Hosts lists the host names the agent must reach to work at all (its AI
	// service and login). They stay reachable in the agent-only network mode.
	Hosts() []string
	// WithModel returns a copy of the adapter that runs the given model, in its
	// one-shot commands and its live start alike (empty means the agent's default).
	WithModel(model string) Adapter
}

var validModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)

// ValidateModel checks that a typed model name is short (64 characters at most),
// starts with a letter or digit and holds only letters, digits, dots, dashes,
// colons and slashes, so nothing odd reaches a command line. Empty is valid: it
// means the agent's own default.
func ValidateModel(name string) error {
	if name == "" {
		return nil
	}
	if len(name) > 64 {
		return fmt.Errorf("model name is too long (64 characters max)")
	}
	if !validModelRe.MatchString(name) {
		return fmt.Errorf("invalid model name %q", name)
	}
	return nil
}

var adapters = map[string]Adapter{}

// Register adds an adapter. Adapters call it from init.
func Register(a Adapter) { adapters[a.Name()] = a }

// Get returns the adapter with the given name.
func Get(name string) (Adapter, error) {
	a, ok := adapters[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", name)
	}
	return a, nil
}

// Names lists registered adapter names, sorted.
func Names() []string {
	n := make([]string, 0, len(adapters))
	for k := range adapters {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Infos lists the registered agents' descriptions, sorted by label.
func Infos() []Info {
	out := make([]Info, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, a.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

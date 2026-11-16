// Package agent holds agent adapters: how to launch each agent CLI.
package agent

import "fmt"

const (
	HandsOff = "hands-off" // agent runs with its approve-everything mode
	AskFirst = "ask-first" // agent asks before risky actions
)

// Adapter knows how to start one agent CLI inside a sandbox.
type Adapter interface {
	Name() string
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
	// Hosts lists the host names the agent must reach to work at all (its AI
	// service and login). They stay reachable in the agent-only network mode.
	Hosts() []string
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

// Names lists registered adapter names.
func Names() []string {
	var n []string
	for k := range adapters {
		n = append(n, k)
	}
	return n
}

// Package runner defines sandbox types. A Runner starts an isolated
// environment; the workflow then runs commands inside it.
package runner

import (
	"context"
	"io"
)

// Mount makes a host directory visible inside the sandbox.
type Mount struct {
	HostPath      string
	ContainerPath string
}

// StartSpec describes the sandbox to create.
type StartSpec struct {
	SessionID string
	Mounts    []Mount
	Memory    string // e.g. "4g"; empty means no limit
	CPUs      string // e.g. "2"; empty means no limit
}

// ExecOpts describes one command to run in the sandbox. Argv is passed
// directly with no shell, so values never need quoting.
type ExecOpts struct {
	Argv   []string
	Env    map[string]string
	Dir    string
	TTY    bool
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Sandbox is a running environment.
type Sandbox interface {
	Exec(ctx context.Context, o ExecOpts) error
	Stop(ctx context.Context) error
}

// Runner creates sandboxes.
type Runner interface {
	Start(ctx context.Context, s StartSpec) (Sandbox, error)
}

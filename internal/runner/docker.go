package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// Docker runs each session in one container. Every container for an agent
// login mounts the same credentials volume: per-session copies of a login
// break at the first token refresh (see experiments/m0-credentials).
type Docker struct {
	Image       string
	CredsVolume string // docker volume holding the agent login, mounted at /home/node
}

type dockerSandbox struct{ name string }

func containerName(id string) string { return "unire-" + id }

func (d Docker) runArgs(s StartSpec) []string {
	args := []string{
		"run", "-d",
		"--name", containerName(s.SessionID),
		"--label", "unire.session=" + s.SessionID,
		"-v", d.CredsVolume + ":/home/node",
		"-w", "/workspace",
	}
	// A sandbox has no need to craft raw packets or gain privileges.
	args = append(args, "--cap-drop", "NET_RAW", "--security-opt", "no-new-privileges")
	if s.Network != "" {
		args = append(args, "--network", s.Network)
	}
	envKeys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		args = append(args, "-e", k+"="+s.Env[k])
	}
	if s.Memory != "" {
		args = append(args, "--memory", s.Memory)
	}
	if s.CPUs != "" {
		args = append(args, "--cpus", s.CPUs)
	}
	for _, m := range s.Mounts {
		args = append(args, "-v", m.HostPath+":"+m.ContainerPath)
	}
	return append(args, d.Image, "sleep", "infinity")
}

func (d Docker) Start(ctx context.Context, s StartSpec) (Sandbox, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", d.runArgs(s)...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return dockerSandbox{name: containerName(s.SessionID)}, nil
}

func execArgs(name string, o ExecOpts) []string {
	args := []string{"exec"}
	if o.Stdin != nil || o.TTY {
		args = append(args, "-i")
	}
	if o.TTY {
		args = append(args, "-t")
	}
	if o.Dir != "" {
		args = append(args, "-w", o.Dir)
	}
	keys := make([]string, 0, len(o.Env))
	for k := range o.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// Name only: the value comes from our own environment, so it never
		// appears on the command line or in the process list.
		args = append(args, "-e", k)
	}
	args = append(args, name)
	return append(args, o.Argv...)
}

func (b dockerSandbox) Exec(ctx context.Context, o ExecOpts) error {
	cmd := exec.CommandContext(ctx, "docker", execArgs(b.name, o)...)
	cmd.Env = os.Environ()
	for k, v := range o.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = o.Stdin, o.Stdout, o.Stderr
	return cmd.Run()
}

// Address returns the sandbox's IP address on the named Docker network.
func (b dockerSandbox) Address(ctx context.Context, network string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--format",
		`{{(index .NetworkSettings.Networks "`+network+`").IPAddress}}`, b.name).Output()
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("the sandbox has no address on network %q", network)
	}
	return ip, nil
}

func (b dockerSandbox) Stop(ctx context.Context) error {
	return exec.CommandContext(ctx, "docker", "rm", "-f", b.name).Run()
}

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
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

// Hostname is the same in every container Unire starts. Some agent CLIs
// encrypt what they save in the login volume with a key derived from the
// machine they ran on; Docker gives each container a new random hostname, so
// without this a login saved by one container is unreadable in the next
// (seen with an agent CLI that stored an API key: "Corrupted credentials file").
const Hostname = "unire-sandbox"

func (d Docker) runArgs(s StartSpec) []string {
	args := []string{
		"run", "-d",
		"--name", containerName(s.SessionID),
		"--hostname", Hostname,
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
	if s.Workspace != "" {
		args = append(args, "-v", s.Workspace+":/workspace")
	}
	for _, m := range s.Mounts {
		args = append(args, "-v", m.HostPath+":"+m.ContainerPath)
	}
	return append(args, d.Image, "sleep", "infinity")
}

// Refresh makes sure the sandbox image is current without making anyone wait for a download (see EnsureFresh).
func (d Docker) Refresh(ctx context.Context) { EnsureFresh(ctx, d.Image) }

func (d Docker) Start(ctx context.Context, s StartSpec) (Sandbox, error) {
	if s.Log != nil && fromRegistry(d.Image) && !haveImage(ctx, d.Image) {
		fmt.Fprintf(s.Log, "downloading the sandbox image %s: this host does not have it yet (after an update), so the first start can take several minutes\n", d.Image)
		t := time.Now()
		d.Refresh(ctx)
		fmt.Fprintf(s.Log, "sandbox image downloaded (%.0fs)\n", time.Since(t).Seconds())
	} else {
		d.Refresh(ctx)
	}
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
	// When the command ends (or is cancelled), Run waits for the copies to and from
	// the process to finish. A copy of an input nobody closes would wait for ever,
	// so give up on them a little after the process has gone.
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

// CleanupOrphans removes sandbox and sign-in containers left behind by an
// earlier run of Unire (after a crash, a kill or an update). It is called at
// start-up, when no session of this run can exist yet, and returns how many it
// removed. It assumes one Unire per Docker host.
func CleanupOrphans(ctx context.Context) (int, error) {
	removed := 0
	for _, label := range []string{"unire.session", "unire.login", "unire.preview"} {
		out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label="+label).Output()
		if err != nil {
			return removed, err
		}
		ids := strings.Fields(string(out))
		if len(ids) == 0 {
			continue
		}
		if err := exec.CommandContext(ctx, "docker", append([]string{"rm", "-f"}, ids...)...).Run(); err != nil {
			return removed, err
		}
		removed += len(ids)
	}
	return removed, nil
}

// RunOnce runs argv in a throwaway container with the agent login volume and
// no network, and returns its combined output.
func (d Docker) RunOnce(ctx context.Context, argv []string) (string, bool, error) {
	return d.runShot(ctx, append(d.oneShotArgs(), argv...))
}

// RunOnline runs argv in a throwaway container with the agent login volume and
// the default network, as a sign-in terminal has, and returns its output.
func (d Docker) RunOnline(ctx context.Context, argv []string) (string, bool, error) {
	return d.runShot(ctx, append(d.onlineShotArgs(), argv...))
}

// onlineShotArgs: the login volume only (no repos, no tokens), the default
// network, and the sign-in label so a container left by a crash is removed at
// the next start.
func (d Docker) onlineShotArgs() []string {
	return []string{
		"run", "--rm", "--hostname", Hostname, "--label", "unire.login=1", "--cap-drop", "NET_RAW", "--security-opt", "no-new-privileges",
		"-v", d.CredsVolume + ":/home/node", "-w", "/home/node", d.Image,
	}
}

func (d Docker) runShot(ctx context.Context, args []string) (string, bool, error) {
	d.Refresh(ctx)
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.String(), false, nil // the command ran and failed: that is an answer, not an error
		}
		return out.String(), false, err // docker itself could not run
	}
	return out.String(), true, nil
}

func (d Docker) oneShotArgs() []string {
	return []string{
		"run", "--rm", "--hostname", Hostname, "--network", "none", "--cap-drop", "NET_RAW", "--security-opt", "no-new-privileges",
		"-v", d.CredsVolume + ":/home/node", "-w", "/home/node", d.Image,
	}
}

// LoginTerminalArgs builds the `docker run` arguments for an interactive login
// terminal. Only the agent logins are mounted: no repos and no git tokens.
func (d Docker) LoginTerminalArgs(name string, argv []string) []string {
	args := []string{
		"run", "-it", "--rm", "--name", name, "--hostname", Hostname, "--label", "unire.login=1",
		"--cap-drop", "NET_RAW", "--security-opt", "no-new-privileges",
		"-e", "TERM=xterm-256color", "-v", d.CredsVolume + ":/home/node", "-w", "/home/node", d.Image,
	}
	return append(args, argv...)
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

// RemoveWorkspace deletes a conversation's workspace volume. A volume that is
// already gone is not an error.
func (d Docker) RemoveWorkspace(ctx context.Context, name string) error {
	out, err := exec.CommandContext(ctx, "docker", "volume", "rm", "-f", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker volume rm: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

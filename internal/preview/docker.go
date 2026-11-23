package preview

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Docker is the Engine that talks to the local Docker through its command line,
// like the sandbox runner does. Containers get no agent login volume and no git
// tokens: only the workspace volume and the network the caller names.
type Docker struct{}

func (Docker) HaveImage(ctx context.Context, image string) bool {
	return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil
}

func (Docker) Pull(ctx context.Context, image string) error {
	return runDocker(ctx, "pull", "-q", image)
}

func (Docker) Run(ctx context.Context, s RunSpec) error {
	return runDocker(ctx, runArgs(s)...)
}

func (Docker) Running(ctx context.Context, name string) bool {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func (Docker) Logs(ctx context.Context, name string, tail int) string {
	var b bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", "logs", "--tail", strconv.Itoa(tail), name)
	cmd.Stdout, cmd.Stderr = &b, &b // docker writes the app's stderr to our stderr
	_ = cmd.Run()
	return b.String()
}

func (Docker) Remove(ctx context.Context, name string) error {
	// -f also stops it. A missing container is not an error worth reporting.
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	return nil
}

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func (Docker) Address(ctx context.Context, name, network string) (string, error) {
	if !safeName.MatchString(network) {
		return "", fmt.Errorf("unsafe network name %q", network)
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--format",
		`{{(index .NetworkSettings.Networks "`+network+`").IPAddress}}`, name).Output()
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("the preview has no address on network %q", network)
	}
	return ip, nil
}

func (Docker) Exec(ctx context.Context, name string, argv []string) error {
	return runDocker(ctx, append([]string{"exec", name}, argv...)...)
}

func runDocker(ctx context.Context, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// runArgs builds the `docker run` arguments. It is a separate function so the
// safety flags can be tested without Docker.
func runArgs(s RunSpec) []string {
	args := []string{
		"run", "-d",
		"--name", s.Name,
		"--label", "unire.preview=1",
		"--cap-drop", "NET_RAW", "--security-opt", "no-new-privileges",
	}
	if s.User != "" {
		args = append(args, "--user", s.User)
	}
	if s.Network != "" {
		args = append(args, "--network", s.Network)
	}
	if s.Workspace != "" {
		args = append(args, "-v", s.Workspace+":/workspace")
	}
	if s.WorkDir != "" {
		args = append(args, "-w", s.WorkDir)
	}
	if s.Memory != "" {
		args = append(args, "--memory", s.Memory)
	}
	if s.CPUs != "" {
		args = append(args, "--cpus", s.CPUs)
	}
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+s.Env[k])
	}
	args = append(args, s.Image)
	return append(args, s.Command...)
}

var probeClient = &http.Client{
	Timeout: 3 * time.Second,
	// A redirect to the app's own login or index page still means it is up.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func httpGet(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

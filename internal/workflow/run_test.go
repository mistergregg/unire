package workflow

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/session"
)

type fakeSandbox struct {
	calls []runner.ExecOpts
	heads int
}

func (f *fakeSandbox) Exec(_ context.Context, o runner.ExecOpts) error {
	f.calls = append(f.calls, o)
	if len(o.Argv) > 0 && o.Argv[0] == "git" && contains(o.Argv, "rev-parse") {
		f.heads++
		// First call per repo is the start HEAD; later calls differ, so the
		// workflow sees the agent's commit.
		if f.heads%2 == 1 {
			io.WriteString(o.Stdout, "aaa\n")
		} else {
			io.WriteString(o.Stdout, "bbb\n")
		}
	}
	return nil
}
func (f *fakeSandbox) Stop(context.Context) error { return nil }

type fakeRunner struct{ sb *fakeSandbox }

func (r fakeRunner) Start(context.Context, runner.StartSpec) (runner.Sandbox, error) {
	return r.sb, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestTokenNeverReachesAgentAndPushHappens(t *testing.T) {
	sb := &fakeSandbox{}
	spec := session.Spec{
		ID: "t1", Task: "demo", Agent: "claude", Prompt: "go", Mode: "hands-off",
		Repos: []session.Repo{{Name: "api", URL: "https://x/api.git"}},
	}
	err := Run(context.Background(), fakeRunner{sb}, spec, Workflow(io.Discard, "tok123"))
	if err != nil {
		t.Fatal(err)
	}
	pushed := false
	for _, c := range sb.calls {
		isAgent := len(c.Argv) > 0 && c.Argv[0] == "claude"
		if isAgent && len(c.Env) != 0 {
			t.Errorf("agent exec got env %v; token must not reach the agent", c.Env)
		}
		if contains(c.Argv, "push") {
			pushed = true
			if c.Env["UNIRE_GIT_TOKEN"] != "tok123" {
				t.Error("push should receive the token")
			}
		}
		for _, a := range c.Argv {
			if strings.Contains(a, "tok123") {
				t.Errorf("token appears in argv: %v", c.Argv)
			}
		}
	}
	if !pushed {
		t.Error("expected a push because HEAD moved")
	}
}

func TestNoPushSkipsPush(t *testing.T) {
	sb := &fakeSandbox{}
	spec := session.Spec{
		ID: "t2", Task: "demo", Agent: "claude", Prompt: "go", Mode: "hands-off", NoPush: true,
		Repos: []session.Repo{{Name: "api", URL: "u"}},
	}
	if err := Run(context.Background(), fakeRunner{sb}, spec, Workflow(io.Discard, "")); err != nil {
		t.Fatal(err)
	}
	for _, c := range sb.calls {
		if contains(c.Argv, "push") {
			t.Error("--no-push must not push")
		}
	}
}

// Workflow builds Options for tests.
func Workflow(w io.Writer, token string) Options {
	return Options{Out: w, Err: w, GitToken: token}
}

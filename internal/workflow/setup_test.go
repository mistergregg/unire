package workflow

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/agent/agenttest"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/tools"
)

// gitConfigCalls are the `git config --global …` commands the workflow ran.
func gitConfigCalls(sb *fakeSandbox) [][]string {
	var out [][]string
	for _, c := range sb.calls {
		if len(c.Argv) > 2 && c.Argv[0] == "git" && c.Argv[1] == "config" && c.Argv[2] == "--global" {
			out = append(out, c.Argv)
		}
	}
	return out
}

func TestAgentsCanCommitWithoutAnIdentityOfTheirOwn(t *testing.T) {
	sb := &fakeSandbox{noIdentity: true}
	if _, err := run(t, sb, baseSpec(), opts(nil)); err != nil {
		t.Fatal(err)
	}
	calls := gitConfigCalls(sb)
	for _, want := range [][]string{
		{"git", "config", "--global", "user.name", commitName},
		{"git", "config", "--global", "user.email", commitEmail},
	} {
		if !slices.ContainsFunc(calls, func(c []string) bool { return slices.Equal(c, want) }) {
			t.Errorf("missing %q in %q", want, calls)
		}
	}
	// The shared home folder must not grow a safe.directory line per session.
	if !slices.ContainsFunc(calls, func(c []string) bool {
		return slices.Contains(c, "--replace-all") && slices.Contains(c, "safe.directory")
	}) {
		t.Errorf("safe.directory should replace itself: %q", calls)
	}
	for _, c := range calls {
		if slices.Contains(c, "--add") {
			t.Errorf("nothing is added to the global git config: %q", c)
		}
	}
}

func TestAnIdentityTheOwnerSetIsKept(t *testing.T) {
	sb := &fakeSandbox{}
	if _, err := run(t, sb, baseSpec(), opts(nil)); err != nil {
		t.Fatal(err)
	}
	for _, c := range gitConfigCalls(sb) {
		if len(c) == 5 && strings.HasPrefix(c[3], "user.") {
			t.Errorf("an identity that is already set is not changed: %q", c)
		}
	}
}

func TestTheAgentIsToldWhereItsReposAre(t *testing.T) {
	var told []string
	sb := &fakeSandbox{}
	spec := baseSpec()
	spec.Repos = append(spec.Repos, spec.Repos[0])
	spec.Repos[1].Name, spec.Repos[1].Base = "web", "develop"
	o := opts(nil)
	o.Tools = tools.ForNewConversation()
	o.Control = make(chan agent.Steer)
	sb.live = func(ctx context.Context, _ []string, x runner.ExecOpts) error {
		return agenttest.Claude(ctx, x.Stdin, x.Stdout, func(u agenttest.User) string { told = append(told, u.Text); return "ok" }, nil, nil)
	}
	if _, err := run(t, sb, spec, o); err != nil {
		t.Fatal(err)
	}
	if len(told) == 0 {
		t.Fatal("the agent was told nothing")
	}
	for _, want := range []string{"# Where the code is", "You start in /workspace, which is not a git repo", "branch " + spec.Branch(), "- /workspace/api\n", "- /workspace/web (started from develop)\n"} {
		if !strings.Contains(told[0], want) {
			t.Errorf("the prompt is missing %q:\n%s", want, told[0])
		}
	}
}

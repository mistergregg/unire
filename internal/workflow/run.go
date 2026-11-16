// Package workflow runs one session end to end: start a sandbox, clone the
// repos onto a session branch, run the agent, then commit and push.
package workflow

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/session"
)

// Options are settings that are not part of the session itself.
type Options struct {
	Out, Err    io.Writer
	In          io.Reader
	Interactive bool // attach a terminal to the agent
	// GitTokens maps a repo's directory name to the token for its git server.
	// Tokens are used only for clone and push, only toward that repo's own
	// host, and are never given to the agent.
	GitTokens map[string]string
	Memory    string
	CPUs      string
}

const workspace = "/workspace"

// credentialHelper returns git config that feeds the token from the
// environment to git, for one host only. The token is never written into the
// repo config or the clone URL, and git will not offer it to any other host
// (for example one named by a submodule).
func credentialHelper(scope string) string {
	return "credential." + scope + `.helper=!f() { echo username=unire; echo "password=$UNIRE_GIT_TOKEN"; }; f`
}

// hostScope returns "https://host" for an http(s) clone URL, or "" otherwise.
func hostScope(cloneURL string) string {
	u, err := url.Parse(cloneURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Run executes the session. Work the agent produced is pushed even when the
// agent exits with an error, and the error is returned afterwards.
func Run(ctx context.Context, r runner.Runner, spec session.Spec, opt Options) error {
	adapter, err := agent.Get(spec.Agent)
	if err != nil {
		return err
	}
	argv, err := adapter.Command(spec.Prompt, spec.Mode)
	if err != nil {
		return err
	}

	var mounts []runner.Mount
	for _, repo := range spec.Repos {
		if repo.LocalPath != "" {
			mounts = append(mounts, runner.Mount{HostPath: repo.LocalPath, ContainerPath: repo.URL})
		}
	}

	fmt.Fprintf(opt.Err, "starting session %s (agent %s, branch %s)\n", spec.ID, spec.Agent, spec.Branch())
	sb, err := r.Start(ctx, runner.StartSpec{SessionID: spec.ID, Mounts: mounts, Memory: opt.Memory, CPUs: opt.CPUs})
	if err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = sb.Stop(stopCtx)
	}()

	w := &worker{sb: sb, opt: opt}

	// Mounted local repos are owned by a different user than the sandbox
	// user. A global setting is needed because git ignores per-command
	// settings for the process that reads the source repo.
	if _, err := w.git(ctx, "", nil, "config", "--global", "--add", "safe.directory", "*"); err != nil {
		return fmt.Errorf("git setup: %w", err)
	}

	startHeads := map[string]string{}
	for _, repo := range spec.Repos {
		repo := repo
		dir := workspace + "/" + repo.Name
		cloneArgs := []string{"clone"}
		if repo.Base != "" {
			cloneArgs = append(cloneArgs, "--branch", repo.Base)
		}
		cloneArgs = append(cloneArgs, repo.URL, dir)
		fmt.Fprintf(opt.Err, "cloning %s\n", repo.Name)
		if _, err := w.git(ctx, "", &repo, cloneArgs...); err != nil {
			return fmt.Errorf("clone %s: %w", repo.Name, err)
		}
		if _, err := w.git(ctx, dir, &repo, "checkout", "-b", spec.Branch()); err != nil {
			return fmt.Errorf("branch %s: %w", repo.Name, err)
		}
		head, err := w.git(ctx, dir, &repo, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		startHeads[repo.Name] = strings.TrimSpace(head)
	}

	fmt.Fprintf(opt.Err, "running %s\n", adapter.Name())
	agentOpts := runner.ExecOpts{
		Argv: argv, Dir: workspace, TTY: opt.Interactive,
		Stdout: opt.Out, Stderr: opt.Err,
	}
	if opt.Interactive {
		// Unattended runs get no stdin, or the agent waits on it.
		agentOpts.Stdin = opt.In
	}
	agentErr := sb.Exec(ctx, agentOpts)

	pushed, err := w.finish(ctx, spec, startHeads)
	if err != nil {
		return err
	}
	if len(pushed) == 0 {
		fmt.Fprintln(opt.Err, "no changes to push")
	}
	if agentErr != nil {
		return fmt.Errorf("agent exited with an error: %w", agentErr)
	}
	return nil
}

type worker struct {
	sb  runner.Sandbox
	opt Options
}

// git runs git in the sandbox. For a repo with a token, the token is passed
// for that repo's host only.
func (w *worker) git(ctx context.Context, dir string, repo *session.Repo, args ...string) (string, error) {
	argv := []string{"git"}
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	if repo != nil {
		if tok := w.opt.GitTokens[repo.Name]; tok != "" {
			if scope := hostScope(repo.URL); scope != "" {
				argv = append(argv, "-c", credentialHelper(scope))
				env["UNIRE_GIT_TOKEN"] = tok
			}
		}
	}
	argv = append(argv, args...)
	var out, errb bytes.Buffer
	err := w.sb.Exec(ctx, runner.ExecOpts{Argv: argv, Env: env, Dir: dir, Stdout: &out, Stderr: &errb})
	if err != nil {
		return out.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// finish commits leftover changes in each repo and pushes every repo whose
// HEAD moved. It returns the names of the repos that changed.
func (w *worker) finish(ctx context.Context, spec session.Spec, startHeads map[string]string) ([]string, error) {
	var changed []string
	for _, repo := range spec.Repos {
		repo := repo
		dir := workspace + "/" + repo.Name
		status, err := w.git(ctx, dir, &repo, "status", "--porcelain")
		if err != nil {
			return changed, err
		}
		if strings.TrimSpace(status) != "" {
			if _, err := w.git(ctx, dir, &repo, "add", "-A"); err != nil {
				return changed, err
			}
			msg := "unire: " + spec.Task
			if _, err := w.git(ctx, dir, &repo, "-c", "user.name=unire-agent", "-c", "user.email=agent@unire.local", "commit", "-m", msg); err != nil {
				return changed, err
			}
		}
		head, err := w.git(ctx, dir, &repo, "rev-parse", "HEAD")
		if err != nil {
			return changed, err
		}
		if strings.TrimSpace(head) == startHeads[repo.Name] {
			continue
		}
		changed = append(changed, repo.Name)
		if spec.NoPush {
			fmt.Fprintf(w.opt.Err, "%s: changes committed, not pushing (--no-push)\n", repo.Name)
			continue
		}
		if _, err := w.git(ctx, dir, &repo, "push", "-u", "origin", spec.Branch()); err != nil {
			return changed, fmt.Errorf("push %s: %w", repo.Name, err)
		}
		fmt.Fprintf(w.opt.Err, "%s: pushed %s\n", repo.Name, spec.Branch())
	}
	return changed, nil
}

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/session"
	"github.com/mistergregg/unire/internal/workflow"
)

type repoFlags []string

func (r *repoFlags) String() string     { return strings.Join(*r, ",") }
func (r *repoFlags) Set(v string) error { *r = append(*r, v); return nil }

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: unire run --repo <url|path>[#base] [--repo ...] --task <name> [--prompt <text>] [flags]")
		os.Exit(2)
	}
	if err := run(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var repos repoFlags
	fs.Var(&repos, "repo", "repo URL or local path, optionally with #base-branch (repeatable)")
	task := fs.String("task", "", "short task name, used in the branch name (required)")
	prompt := fs.String("prompt", "", "task for the agent; empty starts an interactive session")
	agentName := fs.String("agent", "claude", "agent to run: "+strings.Join(agent.Names(), ", "))
	mode := fs.String("mode", agent.HandsOff, "hands-off or ask-first")
	image := fs.String("image", "unire-sandbox", "sandbox image")
	creds := fs.String("creds", "unire-creds-claude", "docker volume holding the agent login")
	noPush := fs.Bool("no-push", false, "commit but do not push")
	memory := fs.String("memory", "", "container memory limit, e.g. 4g")
	cpus := fs.String("cpus", "", "container CPU limit, e.g. 2")
	_ = fs.Parse(args)

	if *task == "" || len(repos) == 0 {
		fs.Usage()
		return fmt.Errorf("--task and at least one --repo are required")
	}
	var parsed []session.Repo
	seen := map[string]bool{}
	for _, a := range repos {
		r, err := session.ParseRepo(a)
		if err != nil {
			return err
		}
		if seen[r.Name] {
			return fmt.Errorf("two repos are both named %q", r.Name)
		}
		seen[r.Name] = true
		parsed = append(parsed, r)
	}

	idb := make([]byte, 4)
	_, _ = rand.Read(idb)
	spec := session.Spec{
		ID: hex.EncodeToString(idb), Task: *task, Agent: *agentName,
		Prompt: *prompt, Mode: *mode, Repos: parsed, NoPush: *noPush,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	stdin, _ := os.Stdin.Stat()
	interactive := *prompt == "" && stdin != nil && stdin.Mode()&os.ModeCharDevice != 0
	return workflow.Run(ctx, runner.Docker{Image: *image, CredsVolume: *creds}, spec, workflow.Options{
		Out: os.Stdout, Err: os.Stderr, In: os.Stdin, Interactive: interactive,
		GitToken: os.Getenv("UNIRE_GIT_TOKEN"), Memory: *memory, CPUs: *cpus,
	})
}

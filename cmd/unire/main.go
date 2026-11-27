package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/server"
	"github.com/mistergregg/unire/internal/session"
	"github.com/mistergregg/unire/internal/workflow"
)

type repoFlags []string

func (r *repoFlags) String() string     { return strings.Join(*r, ",") }
func (r *repoFlags) Set(v string) error { *r = append(*r, v); return nil }

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "serve":
		err = serve(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  unire serve [--addr 127.0.0.1:8080] [--data ./data]    start the web dashboard
  unire run --repo <url|path>[#base] --task <name> [--prompt <text>] [flags]`)
	os.Exit(2)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address (keep it on localhost or a private network)")
	data := fs.String("data", "./data", "directory for the database (unire.db) and master key")
	_ = fs.Parse(args)

	s, err := server.New(*data)
	if err != nil {
		return err
	}
	defer s.Close()
	srv := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(os.Stderr, "Unire dashboard on http://%s (data in %s)\n", *addr, *data)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var repos repoFlags
	fs.Var(&repos, "repo", "repo URL or local path, optionally with #base-branch (repeatable)")
	task := fs.String("task", "", "short task name, used in the branch name (required)")
	prompt := fs.String("prompt", "", "task for the agent; empty starts an interactive session")
	agentName := fs.String("agent", "claude", "agent to run: "+strings.Join(agent.Names(), ", "))
	model := fs.String("model", "", "model name (empty uses the agent's default)")
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
	if err := agent.ValidateModel(strings.TrimSpace(*model)); err != nil {
		return err
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
		ID: hex.EncodeToString(idb), Task: *task, Agent: *agentName, Model: strings.TrimSpace(*model),
		Prompt: *prompt, Mode: *mode, Repos: parsed, NoPush: *noPush,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	stdin, _ := os.Stdin.Stat()
	interactive := *prompt == "" && stdin != nil && stdin.Mode()&os.ModeCharDevice != 0
	tokens := map[string]string{}
	if tok := os.Getenv("UNIRE_GIT_TOKEN"); tok != "" {
		for _, r := range parsed {
			tokens[r.Name] = tok
		}
	}
	_, err := workflow.Run(ctx, runner.Docker{Image: *image, CredsVolume: *creds}, spec, workflow.Options{
		Out: os.Stdout, Err: os.Stderr, In: os.Stdin, Interactive: interactive,
		GitTokens: tokens, Memory: *memory, CPUs: *cpus,
	})
	return err
}

# Unire

Self-hosted control panel for AI coding agents: isolated sandboxes, shared memory, live preview.

> **Status:** early design. Nothing is built yet. See [SPEC.md](SPEC.md) for the plan.

## What it will do

- Start a coding task from a browser and run it in its own disposable sandbox.
- Use the agent CLI you choose (Claude Code, Gemini CLI, Codex, and others via adapters), with your own subscriptions.
- Work on multiple repos at once, from any git server (Gitea, GitHub, GitLab, plain git, and more via plugins).
- Remember what was done and carry it across agents and sessions.
- Show a live preview of the app or site being built, with pluggable preview types.
- Hand results back as git branches and pull requests.
- Later: remote workers on your other servers.

## Try the launcher (Milestone 1, command line only)

Needs Go, Docker, and an agent login stored in a Docker volume (see `experiments/m0-credentials/`).

```bash
docker build -t unire-sandbox docker/sandbox
go build -o bin/unire ./cmd/unire
./bin/unire run --repo <git-url-or-local-path>#main --repo <another> \
  --task "add login page" --creds <login-volume> --prompt "Add a login page"
```

Each repo is cloned into its own disposable container on a branch `agent/<agent>/<task>`; the result is pushed to that branch. Set `UNIRE_GIT_TOKEN` for HTTPS remotes that need a token. The token is only used for clone and push and is never given to the agent.

## License

Unire is licensed under the [GNU AGPL v3.0](LICENSE). Commercial licensing is available from the project owner.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Contributions require agreeing to a contributor license agreement.

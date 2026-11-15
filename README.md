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

## License

Unire is licensed under the [GNU AGPL v3.0](LICENSE). Commercial licensing is available from the project owner.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Contributions require agreeing to a contributor license agreement.

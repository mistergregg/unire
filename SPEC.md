# Unire — Product 1 Spec

Status: draft v0.1. Single-user, self-hosted. Open source (license TBD: MIT/Apache-2.0 or AGPL).

## One-liner
A self-hosted control panel for AI coding agents. Start a coding task from a browser; it runs in its own isolated sandbox using the agent CLI you choose, remembers what was done across agents and sessions, and hands results back as git branches with a live preview.

## Goals
- Agent-agnostic: Claude Code, Gemini CLI, Codex, and others via adapters.
- Isolation: every session in its own disposable sandbox. Sessions never interfere.
- Git is the sync point. Agents never share a filesystem.
- Multi-repo workspaces, from any git server.
- Shared memory that works across agents and sessions.
- Web control from any device (phone, PC) over a private network.
- Easy to extend: new git servers, agents and preview types are single-file plugins.

## Non-goals (v1)
- Multiple users on one instance (never, see "Single owner by design"), billing, or hosting for other people (product 2).
- Our own chat UI on top of subscription tokens. Subscriptions are used only through the official CLIs running in the sandbox.
- Remote workers / VMs (designed for, built after v1).
- Creating new projects from templates or prompts (existing repos only).

## Single owner by design
Each Unire instance has exactly one account, the owner's. There is no "add user" feature and first-run setup can happen only once. Agent logins, git tokens, repos, memory and sessions all belong to that one person. Reasons: subscription logins are personal and cannot be shared between people, and a shared server would mix everyone's credentials and code in one place. Someone else who wants Unire runs their own instance. If Unire is ever offered as a hosted service (product 2), the model is one isolated instance per user, each with its own database and vault, with users bringing their own agent logins or API keys.

## Storage
One SQLite file (`unire.db`) in the data directory holds settings, git servers, encrypted secrets, login sessions and, as they are built, workspaces, coding sessions, logs and memory. Schema changes are numbered migrations applied at start-up and never edited afterwards; a database from a newer version is refused, not downgraded. Backup is the data directory (or the dashboard's export).

## Core concepts
- **Git server**: a configured source of repos (Gitea, Forgejo, GitHub, GitLab, Bitbucket, plain git).
- **Project / Workspace**: one or more repos, optionally saved as a named workspace.
- **Session**: one agent working in one sandbox on one branch per repo. Has status, log, preview URLs.
- **Agent adapter**: how to launch an agent CLI, where its login lives, how to detect done/waiting.
- **Preview provider**: how to start and expose a live preview.
- **Worker**: runs sandboxes. In v1 it is an in-process module on the controller machine, behind the same message interface a remote worker will use.
- **Vault**: stores agent logins, git tokens and secrets; hands them to a session only at start, scoped to it.
- **Memory**: compressed, cross-agent record of what has been done (see below).

## Typical flow
1. Open dashboard. New Session.
2. Search repos across all configured git servers, tick the ones needed, pick base branches (or load a saved workspace).
3. Pick an agent and a mode (Hands-off or Ask-first).
4. Sandbox starts, clones repos side by side under `/workspace/<repo>`, checks out `agent/<tool>/<task>` in each, injects memory, starts the agent.
5. Watch and steer in a web terminal; live preview(s) beside it.
6. Finish: session summary written to memory, branches pushed, one PR per changed repo (cross-linked), sandbox discarded.
7. Later: "Continue with…" starts a new session (possibly a different agent) on the same branches with memory loaded.

## Session modes
- Interactive and autonomous are the same session: you can always type into the terminal to steer.
- **Hands-off (default)**: agent CLI runs in its auto-approve mode; safe because the sandbox is disposable, scoped and network-limited. Notifies on done/stuck.
- **Ask-first**: approvals surface in the web UI.

## Plugin families
Each is one file implementing a small interface, plus a shared conformance test suite.

### Git providers
`listRepos(query)`, `getRepo(id)`, `cloneUrl(repo, auth)`, `createScopedToken(repos)` (or declared fallback), `createPullRequest(...)`, `linkPullRequests(...)` (optional), `capabilities()`.
- Settings screen: Add Git Server (type, base URL, token, optional custom CA / skip-verify) with Test Connection. Multiple servers; the repo picker searches across all.
- Generic "plain git" provider: clone/push only; no search or PR (shows compare link).
- UI warns when a server cannot scope tokens to selected repos.

### Agent adapters
One file per agent (see [docs/adding-an-agent.md](docs/adding-an-agent.md)): launch command and mode flags, the agent's own sign-in command and a way to check it, how memory is delivered (a native file such as CLAUDE.md, or the prompt), the hosts it needs, and a `Verified` record of what has been checked against the real CLI. Sign-in happens through a terminal in the browser on the Agents page, using each agent's own subscription login; the logins live in one shared volume. Implemented: Claude Code (verified), Codex and Gemini CLI (written from the CLIs' own help, not yet run with a signed-in account). Not yet: Antigravity (its sign-in uses the OS keyring, which a container lacks) and Ollama (a model backend for an agent, not an agent).

### Runners (sandbox types)
`prepare(session)`, `start(session)`, `attach(session)` (terminal stream), `exposePort(session, port)`, `stop(session)`, `capabilities()` (isolation level, resource limits).
- **Docker runner (default, v1):** one container per session. All containers for the same agent mount one shared credentials volume; per-session copies of a login break after the first token refresh (see `experiments/m0-credentials/RESULTS.md`).
- **Folder runner (later, lightweight):** a plain folder on the host using the host's own login. Weaker isolation, so not recommended with hands-off mode.
- **VM runner (later):** for remote workers or hard isolation.

### Preview providers
`detect(workspace)`, `start(session)`, `expose(session)`, `health()`, `stop()`.
- v1: web dev server (reverse proxy), static build output, multiple servers per session (tab each), custom via project config (`command`, `port`, `ready-when`).
- Later: Docker Compose stacks, VNC/noVNC for mobile/desktop apps, API docs / test reports, before/after screenshots.
- Each preview gets its own URL behind the dashboard login; subdomain-per-session (`s123.preview.<domain>`) for apps that break under path prefixes (needs wildcard DNS or local DNS entry).

## Memory (cross-agent, compressed)
Goal: any agent, on any later session, knows what has been done, why, and what is open, without replaying old transcripts. Implemented as described in [docs/memory.md](docs/memory.md); this section keeps the design intent and what is still to build.

Implemented:
1. **Raw log**: session events in the database, never injected wholesale.
2. **Session summary**: when a session finishes, Unire resumes the same agent conversation (by conversation id, never "the latest") and asks it to write a summary. Falls back to a plain summary built by Unire if the agent cannot.
3. **Repo brief**: a rolling brief per repo (at most 6000 characters), rewritten by the agent after each session, with one step of undo.
4. **Delivery**: the briefs and the latest summaries are written, as plain markdown, to the agent's native memory file in `/workspace` (above the repos, so never committed) at the start of a session. Size budget about 4000 tokens.
5. **Safety**: marked as background data, not instructions; secrets removed before saving; editable, clearable and deletable in the Memory tab; per-session on/off.
6. **Handoff**: Continue with a new session starts any agent from a finished session's pushed branch with the memory.

Still to build:
- **MCP server in the sandbox** (`get_brief`, `search_memory(query)`, `log_decision`, `add_todo`) so an agent can pull older detail on demand instead of carrying it all.
- **Workspace memory**: how the repos of a workspace relate, and cross-repo decisions.
- **Optional export** of the brief into the repo (committed), so memory survives outside Unire.
- **Memory for interactive sessions** and for agents other than Claude Code.

Rules:
- Memory is data, not instructions: injected under a header that says so; stale or wrong entries can be edited or deleted in the UI.
- Secrets are never saved to memory (scan before saving; a safety net, not a guarantee).
- Compression is lossy by design; the raw log and git history remain the ground truth.

## Security
- Sandboxes: Docker, resource limits, restricted network egress, no host mounts.
- Secrets delivered per session at start (memory/tmpfs), wiped at end; git tokens scoped to selected repos where the provider allows.
- Agent logins stored in the vault and mounted into sandboxes through one shared credentials volume per login. Milestone 0 showed this survives concurrent token refreshes; per-session copies do not. Remote workers still need a solution (long-lived token or a refresh broker).
- Dashboard: one owner account, password login. Logins are stored in the database (only a hash of each token), last 30 days sliding, survive restarts, and can be listed and revoked per device; changing the password logs out all other devices. Intended behind Tailscale/Cloudflare Access, not the open internet.
- Secrets are write-only: the API and UI show "token saved" and allow replacing it, never viewing it.
- Backup: the dashboard exports a zip of the database plus the master key (unless the key is supplied through `UNIRE_MASTER_KEY`).
- Workers dial out to the controller (no inbound ports); a compromised worker exposes only its running sessions.

## Architecture
Controller (web UI, API, DB, vault, memory, git/preview/agent plugins) talks to Workers via a defined message protocol (start/stop session, stream terminal, expose port). v1 worker is in-process; the transport is swapped for WebSocket/gRPC when remote workers arrive. Deploy target: Docker container on Unraid.

## Milestones
0. Credentials test: log in to Claude Code once; run two containers with the same login concurrently. Record findings.
1. Sandbox launcher from CLI: clone selected repos, branch, run agent, push.
2. Git provider interface + Gitea provider + generic provider; settings screen.
3. Web dashboard: sessions list, repo picker, web terminal.
4. Preview providers: web dev server + static; reverse proxy.
5. Memory: session summaries, project brief, injection, MCP server.
6. Continue-with-different-agent; Gemini and Codex adapters.
7. Remote workers; VM sandboxes.
8. Consider multi-user (product 2).

## Open questions
- License. (Name: Unire, from the Latin/Italian "to unite". Still to verify: GitHub org, domain, npm/PyPI/Docker Hub names.)
- Implementation language (leaning Go for controller and worker, simple web front end).
- Credentials for remote workers (Milestone 0 covered a single machine only), and the same question for Gemini CLI and Codex.
- Memory compression: which model and when, given subscription-only constraints (current plan: the session's own agent writes the summary).

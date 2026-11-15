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
- Multi-user accounts, billing, or serving other people (product 2).
- Our own chat UI on top of subscription tokens. Subscriptions are used only through the official CLIs running in the sandbox.
- Remote workers / VMs (designed for, built after v1).
- Creating new projects from templates or prompts (existing repos only).

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
Launch command, login capture/restore (credential files), mode flags (auto-approve), done/waiting detection, native memory file name (CLAUDE.md, GEMINI.md, AGENTS.md…).

### Preview providers
`detect(workspace)`, `start(session)`, `expose(session)`, `health()`, `stop()`.
- v1: web dev server (reverse proxy), static build output, multiple servers per session (tab each), custom via project config (`command`, `port`, `ready-when`).
- Later: Docker Compose stacks, VNC/noVNC for mobile/desktop apps, API docs / test reports, before/after screenshots.
- Each preview gets its own URL behind the dashboard login; subdomain-per-session (`s123.preview.<domain>`) for apps that break under path prefixes (needs wildcard DNS or local DNS entry).

## Memory (cross-agent, compressed)
Goal: any agent, on any later session, knows what has been done, why, and what is open, without replaying old transcripts.

Layers:
1. **Raw log**: terminal output and events per session, stored in the controller DB, kept for audit/search, never injected wholesale.
2. **Session summary**: written when a session ends (the session's own agent is prompted to write it as its last step, so no extra API cost or subscription misuse): what was asked, what changed (repos, files), decisions and why, gotchas, open TODOs, how to run/test.
3. **Project memory**: per-project rolling brief (architecture, conventions, commands, decisions log, open items). After each session the summary is merged in; older entries are rolled up so the brief stays under a token budget (e.g. ~2–4k tokens), with full history searchable.
4. **Workspace memory**: for multi-repo workspaces, how the repos relate and cross-repo decisions.

Delivery to agents:
- Agent-neutral markdown is the canonical format. The adapter writes it into the agent's native file (CLAUDE.md / GEMINI.md / AGENTS.md) inside a clearly marked managed block, leaving user-written content alone.
- Optional MCP server exposed in the sandbox: `get_brief`, `search_memory(query)`, `log_decision`, `add_todo`, so agents can pull detail on demand instead of carrying it all.
- Source of truth is the controller DB. Optional export of the managed block into the repo (committed) so memory survives outside the tool.

Rules:
- Memory is data, not instructions: injected under a "context from previous sessions" header; stale or wrong entries can be edited or deleted in the UI.
- Secrets are never written to memory (scan before saving).
- Compression is lossy by design; the raw log and git history remain the ground truth.

## Security
- Sandboxes: Docker, resource limits, restricted network egress, no host mounts.
- Secrets delivered per session at start (memory/tmpfs), wiped at end; git tokens scoped to selected repos where the provider allows.
- Agent logins stored in the vault; copied in at session start. Whether one login works across concurrent sessions/workers must be tested first (Milestone 0).
- Dashboard: single admin login; intended behind Tailscale/Cloudflare Access, not the open internet.
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
- Whether one agent login can run across concurrent sessions/workers (Milestone 0).
- Memory compression: which model and when, given subscription-only constraints (current plan: the session's own agent writes the summary).

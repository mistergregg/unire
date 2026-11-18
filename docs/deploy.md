# Deploying Unire (Unraid or any Docker host)

Unire runs as one container. It starts each coding session as a separate sandbox container by talking to the host's Docker.

## Read this first: the Docker socket

The controller needs `/var/run/docker.sock` to start sandboxes. Access to that socket is **root-level access to the host**. Unire is built for one trusted owner, so:

- Keep the dashboard on your LAN or behind Tailscale or Cloudflare Access. Do not forward its port to the internet.
- Use a strong owner password. Repeated wrong passwords lock the login for a minute.
- Agents run in auto-approve mode, so Unire limits what a sandbox can reach: by default the internet but not your private network. See [network.md](network.md) for the modes and the allowed-address list. This protection works when Unire runs as a container (as described here), not when the binary is run directly.

## Images

The GitHub Action in `.github/workflows/docker.yml` publishes two images when `main` changes:

- `ghcr.io/mistergregg/unire` is the controller (the dashboard).
- `ghcr.io/mistergregg/unire-sandbox` is the sandbox each session runs in (git plus the Claude Code, Codex and Antigravity CLIs). It is large, about 2.6 GB on disk, because it carries all three agents; Unire pulls it on start-up and keeps it current (it checks at most every 30 minutes, before a session or a sign-in starts), so you only update the `unire` container.

If pulling fails with "unauthorized", open the package on GitHub (Packages, then Package settings) and set its visibility to public.

## Unraid

1. Copy `deploy/unraid/unire.xml` to `/boot/config/plugins/dockerMan/templates-user/unire.xml` on the server.
2. In the Docker tab press Add Container, pick the `unire` template, and press Apply. The defaults are fine: port 8080, data in `/mnt/user/appdata/unire`.
3. Open the WebUI and set the owner password.
4. Sign the agents in, from the dashboard. Open the **Agents** tab and press **Sign in** on Claude Code, Codex or Antigravity CLI. A terminal opens in the browser running that agent's own login. It shows a link (and for Codex a one-time code): open the link on any device, sign in with your subscription, and paste any code back with Ctrl+Shift+V. The login is saved in the `unire-creds-claude` volume and shared by every session, and the badge on the page turns to "signed in". Close the terminal when you are done. There is no need to use the Unraid terminal.
5. Add a git server under Git servers. Use the server's IP address and port, for example `http://192.168.0.10:3000`, because sandboxes cannot look up Unraid container names. A git server on your LAN works in every network mode: the git server of each repo in a session is always allowed. For Gitea, create the token under Settings, Applications and give it repository Read and Write, user Read and organization Read. (A repository-only token also works, but Unire cannot show whose token it is.) If the test fails, the message includes the server's own reason, for example a missing permission, or a note that a proxy or firewall in front of the server is blocking Unire.
6. Press New session.

## Docker Compose

```bash
docker compose -f deploy/docker-compose.yml up -d
```

## Backups

Copy the data folder (`unire.db` and `master.key`), or use Settings, Download backup. The key decrypts your saved tokens, so treat the backup as secret. If you set `UNIRE_MASTER_KEY`, the key is not in the data folder and you must keep it yourself; losing it makes saved tokens unreadable (you would re-enter them).

## Updating

Pull the new image and recreate the container. The database upgrades itself at start-up; a database from a newer Unire is refused instead of being damaged.

## If an agent's sign-in gets stuck

On the Agents page, each agent has three tools:

- **Show saved state** lists what the agent has saved (file names, sizes and non-secret settings, never credentials).
- **Show last sign-in output** shows what the last sign-in terminal printed, as plain text with links, codes and email addresses removed. It is kept in memory only and is lost when Unire restarts. This is the place to find the error message when a sign-in ends somewhere unexpected.
- **Reset sign-in** clears the agent's saved sign-in so you can start clean.

## Known limits

- Sessions are unattended only (no interactive terminal yet).
- After a crash or kill, sandbox and sign-in containers can be left behind. Unire removes any it finds (labelled `unire.session` or `unire.login`) when it starts. This assumes one Unire per Docker host.

## Conversation workspaces

Each conversation keeps its cloned repos in a Docker volume named
`unire-ws-<id>`, so a reply continues where the last message stopped instead of
cloning everything again. The agent's work is still pushed to its branch after
every message. The volume is removed when you delete the conversation. If you
delete Unire's data by hand, you can remove leftover volumes with
`docker volume ls --filter name=unire-ws-`.

## Limits (Settings, Limits)

- **Longest a run may take** (default 240 minutes, 0 for no limit). A run over its time is stopped and marked failed with the reason. **Stop** also always frees the slot: if a stopped run does not come back within 90 seconds, Unire gives up on it, marks the chat stopped and lets another run use the slot (the sandbox is removed when the run does end).
- **Keep activity lines** (default 90 days). Progress and network lines of finished chats are deleted after this. Answers, questions, your messages, summaries, and the lines saying which branch was pushed are kept (pull requests are found from them).
- **Remove idle workspaces after** (default 14 days). The workspace volume of a conversation with nothing running for this long is removed; a reply clones the repos again from the branch that was pushed. Anything that was never pushed is lost, so push before leaving work idle.

Cleanup runs a couple of minutes after start-up and then every six hours.

## Memory limit

Each chat's sandbox is limited to 4 GB of memory by default, so one runaway agent cannot use all of the host's RAM and take down the other containers on it. Change it under Settings, Sandbox, **Memory limit per chat** (for example `8g` or `512m`); `0` means no limit and empty means the default. It applies to chats that start after the change.

## The Health page

When something does not work, open **Health**. It checks, and says what to do about each problem: Docker is reachable, the sandbox image is on the host (and how old), the agent login volume exists, sandbox containers match the running chats, network protection, free disk space, the database (size, counts and SQLite's integrity check), the limits and the last cleanup, each agent's sign-in, and each git server's token. Anything broken or worth a look comes first. Nothing on the page changes anything; it only reads. The agent and git server checks ask over the network, so the page can take several seconds.

## A push was refused ("Permission denied", 403)

The token saved for a git server must be allowed to write to the repo. For GitHub that means a fine-grained token with Contents set to read and write for that repo (and Pull requests and Issues read and write, to open pull requests and comment; Actions and Commit statuses read-only, to show build status; Workflows read and write only if agents may change workflow files). Fine-grained tokens have no Checks permission: Unire reads the Actions workflow runs for build status instead. When a push is refused, the chat fails with a message saying so. Replace the token on the **Git servers** page, press **Check pull requests** to see what it may do, then reply in the chat: Unire pushes commits an earlier push left behind, even when the new turn changes nothing, so you do not need the agent to redo the work.

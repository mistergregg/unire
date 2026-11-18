# Network access for sandboxes

Each coding session runs in a sandbox container. The agent inside works unattended with its approve-everything mode on, so Unire controls what the sandbox can reach on the network.

## How it works

- Sandboxes are attached to a Docker network named `unire-sandbox` that has **no route out**. A sandbox cannot reach the internet, your LAN, or the Docker host directly.
- The only way out is Unire's built-in filtering proxy, reached as `unire-egress:3128` (the sandbox's `HTTP_PROXY` and `HTTPS_PROXY` point at it). The proxy sees which sandbox is calling by its address on that network and applies that session's policy.
- For each connection the proxy resolves the destination itself and connects to an address the policy allows. It connects to the checked address, not the name, so a name cannot resolve differently the second time.
- Git, npm, pip, curl and Claude Code all use the proxy settings. Anything that ignores them (raw sockets, SSH, database clients) simply has no route and fails with "Network is unreachable".
- Blocked and first-time connections appear in the session log. A blocked one has an **Allow** button that adds the address to your allowed list for future sessions.

## Modes

| Mode | What the sandbox can reach |
| --- | --- |
| **Internet only** (default) | The public internet, the git servers of the session's repos, and your allowed addresses. Private networks are blocked: 10.x, 172.16-31.x, 192.168.x, loopback, link-local (including cloud metadata addresses), carrier-grade NAT (including Tailscale), and IPv6 equivalents. |
| **Agent only** | Only the agent's own service (for Claude Code: `api.anthropic.com`, `mcp-proxy.anthropic.com`, and its login hosts), the session's git servers, and your allowed addresses. Package installs and web fetches fail. |
| **Full access** | Everything the server can reach, with no filtering and no proxy. Use only for repos you trust. |

Set the default in Settings and override it per session on the New session page.

## Allowed addresses

A list in Settings, one per line, used in Internet only and Agent only mode. Each entry is a host name, an address or a range, optionally with a port:

```
nas.local
192.168.0.50:5432
192.168.0.0/24
```

The git server of each repo in a session is always allowed on its own host and port, even on your LAN, so cloning and pushing work without any entry.

## When protection is not available

Protection needs Unire to run **as a Docker container with the Docker socket mounted**, so it can create the sandbox network and join it. If it is not available (for example when running the binary directly on your PC), the Settings page says why, and sessions in Internet only or Agent only mode **refuse to start** instead of running unprotected. Full access still works.

Set `UNIRE_EGRESS=off` to disable protection on purpose. If Unire's container name is not its hostname, set `UNIRE_CONTAINER_NAME`.

## What this does not do

- It filters by destination, not by content. An agent can still send data to any allowed destination, including the public internet.
- Telemetry some tools send (for example Claude Code's) goes out in Internet only mode and is blocked in Agent only mode.
- The proxy trusts the sandbox's address on the sandbox network. The sandbox drops `NET_RAW` and cannot gain privileges, which stops it forging another address.
- The Docker socket given to the Unire container is still root-level access to the host. See [deploy.md](deploy.md).

## Tested

Verified with a real session through a real container setup (Gitea on the LAN, Claude Code in the sandbox):

- Internet only: example.com reachable, the router blocked and logged, the LAN git server reachable, `curl --noproxy '*'` and a raw TCP connection both failed.
- Agent only: Claude Code worked, example.com blocked, the git server reachable.
- Full access: everything reachable, including the router.
- After allowing the router's address, a new Internet only session could reach it.

## A server running inside the sandbox

An agent often starts a server in its sandbox to try its work (`unire serve`, a dev server) and then calls it on `localhost`. That traffic never leaves the sandbox, so it does not go through the proxy: the sandbox sets `NO_PROXY=localhost,127.0.0.1,::1`, and tools reach it directly. (Before, the proxy was told to proxy everything, refused it as a "private network address", and the Allow button could only ever open the *proxy's* own localhost.)

Because of that, loopback (`127.x`, `::1`, `localhost`), `0.0.0.0` and link-local addresses (`169.254.x.x`, where cloud metadata services live) **cannot be added to the allowed list**: the Allow button is not offered for them, the server refuses them with the reason, and entries saved by older versions are ignored. A LAN address you choose (for example your own git server) can still be allowed.

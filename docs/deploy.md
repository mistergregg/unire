# Deploying Unire (Unraid or any Docker host)

Unire runs as one container. It starts each coding session as a separate sandbox container by talking to the host's Docker.

## Read this first: the Docker socket

The controller needs `/var/run/docker.sock` to start sandboxes. Access to that socket is **root-level access to the host**. Unire is built for one trusted owner, so:

- Keep the dashboard on your LAN or behind Tailscale or Cloudflare Access. Do not forward its port to the internet.
- Use a strong owner password. Repeated wrong passwords lock the login for a minute.
- Sandboxes can reach your network. Restricting their network access is planned but not built yet, and agents run in auto-approve mode, so only point Unire at repos and servers you are happy for an agent to work on.

## Images

The GitHub Action in `.github/workflows/docker.yml` publishes two images when `main` changes:

- `ghcr.io/mistergregg/unire` is the controller (the dashboard).
- `ghcr.io/mistergregg/unire-sandbox` is the sandbox each session runs in (git plus Claude Code).

If pulling fails with "unauthorized", open the package on GitHub (Packages, then Package settings) and set its visibility to public.

## Unraid

1. Copy `deploy/unraid/unire.xml` to `/boot/config/plugins/dockerMan/templates-user/unire.xml` on the server.
2. In the Docker tab press Add Container, pick the `unire` template, and press Apply. The defaults are fine: port 8080, data in `/mnt/user/appdata/unire`.
3. Open the WebUI and set the owner password.
4. Log the agent in once. In the Unraid terminal run:
   ```bash
   docker run -it --rm -v unire-creds-claude:/home/node ghcr.io/mistergregg/unire-sandbox:latest claude
   ```
   Use `/login` inside, choose your subscription, finish in the browser, then `/exit`. The login is saved in the `unire-creds-claude` volume and shared by every session. (The same command is shown in Settings.)
5. Add a git server under Git servers. From a sandbox, a server on the same Unraid box must be reached by the server's IP address and port, for example `http://192.168.0.10:3000`, because sandboxes do not share Unraid's container name lookup.
6. Press New session.

## Docker Compose

```bash
docker compose -f deploy/docker-compose.yml up -d
```

## Backups

Copy the data folder (`unire.db` and `master.key`), or use Settings, Download backup. The key decrypts your saved tokens, so treat the backup as secret. If you set `UNIRE_MASTER_KEY`, the key is not in the data folder and you must keep it yourself; losing it makes saved tokens unreadable (you would re-enter them).

## Updating

Pull the new image and recreate the container. The database upgrades itself at start-up; a database from a newer Unire is refused instead of being damaged.

## Known limits

- Sessions are unattended only (no interactive terminal yet).
- A hard kill of the controller can leave a sandbox container running. Remove leftovers with `docker ps -a --filter label=unire.session` and `docker rm -f`.

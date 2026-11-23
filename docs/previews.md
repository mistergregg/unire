# Live previews

Unire can run the app an agent is building and let you open it in your browser while the agent works. This page covers what the server does and how to turn it on. The Preview tab in the chat is not built yet: for now previews are reached through the API below.

## What it does

- Looks in the chat's workspace for something to preview. A `pubspec.yaml` means a Flutter app, a lone `index.html` means a static site. A repo can also say what to run in a `unire.preview.json` (see below).
- Runs it in its own container, next to the agent's, using a ready-made image for that kind of project. The image is downloaded the first time it is needed.
- Gives it its own port, so the app thinks it lives at the root of a site (a Flutter web app needs that). You open it from the dashboard with a link that works for two minutes and then keeps you signed in to that one preview.

## Turn it on

Previews are off until you give Unire a range of ports for them:

1. Publish a range on the Unire container, for example `9100-9199`, and set `UNIRE_PREVIEW_PORTS=9100-9199` to the same range. In `deploy/docker-compose.yml` uncomment the two lines marked for previews.
2. On Unraid, add the range under **Extra Parameters** as `-p 9100-9199:9100-9199` and add a variable `UNIRE_PREVIEW_PORTS` with the same range. This has not been tried on Unraid yet.
3. Your phone or computer must be able to reach those ports on the Unire host, the same way it reaches the dashboard.

Previews also need what network protection needs: Unire running in Docker with the Docker socket mounted (see [network.md](network.md)).

## Network rules

A preview follows the network setting of the chat it belongs to. Its package downloads (Flutter's pub, npm) go through the same filtering proxy as the agent's:

| Chat's setting | The preview can |
| --- | --- |
| Internet only | download packages from the internet; not reach your home network, except addresses you allowed |
| Agent only | not download packages, so most apps will not start (the log says why) |
| Full access | reach everything |

A preview never gets the agent's login, your git tokens, or the Docker socket.

## `unire.preview.json`

Optional, in the repo root. It chooses a built-in recipe and may change its command, port and readiness check. It can not choose a container image: a repo, or an agent editing it, must not be able to run any image it likes next to your data.

```json
{
  "previews": [
    { "name": "app", "recipe": "flutter", "dir": "app", "port": 8080,
      "ready_when": { "http_path": "/" } }
  ]
}
```

Recipes: `flutter`, `static`. `dir` is a folder inside the repo.

## API

All of these need the dashboard login. `{id}` is any chat of the conversation; `{repo}` is the repo's folder name in the workspace.

- `GET /api/sessions/{id}/previews`: what was found, whether previews are available (and why not), and the state of each.
- `POST /api/sessions/{id}/previews/{repo}/{name}/start`: start it.
- `GET /api/sessions/{id}/previews/{repo}/{name}`: state (`downloading`, `starting`, `ready`, `failed`, `stopped`). When `ready` it includes a `url` to open.
- `GET /api/sessions/{id}/previews/{repo}/{name}/logs`: the app's output.
- `POST /api/sessions/{id}/previews/{repo}/{name}/stop`: stop it and free its port.

## Limits

- If the dashboard is served over HTTPS, the preview URL is still plain `http://` on its own port, and browsers block it inside the page. Open it in its own tab.
- Previews stop when Unire stops. Leftover preview containers are removed at the next start.
- Cookies are shared between ports of one host. The preview proxy hides Unire's own cookies from the app and stops the app setting any, but the app's own cookies are shared with other previews on the same host.
- The Flutter image and the `flutter run` options have not been tried against a real project yet.

# Add-ons

Settings, Sandboxes & Network, **Add-ons** turns on extra tool sets for the sandbox. Add-ons are independent: tick any mix and chats get all of them together.

| Add-on | Adds | Tools |
|---|---|---|
| 3D modelling and printing (`3d-print`) | about 1.5 GB | build123d, trimesh, OpenSCAD, CuraEngine, ADMesh, `unire-3d`, `unire-enclosure`, `unire-slice`, `unire-mesh-check`, `unire-stl-shot` |
| Electronics and CAD/CAM (`cad`) | about 3 GB | `kicad-cli` (DRC, Gerbers, STEP), FreeCAD (`freecadcmd`), `unire-kicad-check` |

## How it works

- **One image, built on your server.** Unire writes a Dockerfile `FROM` the sandbox image with an install step for each add-on that is on (`addons.Dockerfile`), and runs `docker build` through the Docker socket it already uses. Nothing is downloaded from a registry for add-ons, so any combination works. The agents, git and checking tools of the sandbox image are all still there.
- **When.** Saving the Add-ons card starts the build in the background. A chat that starts before the first build finishes waits for it (its activity log says so). The card shows the build state and, while building or after a failure, the end of the build output.
- **Updates.** Images are tagged `unire-sandbox-addons:<set>-<build>`, where `<set>` is the add-ons' files plus the sandbox image name and `<build>` adds the sandbox image's id. When the sandbox image is updated, chats keep using the previous build (`unire-sandbox-addons:<set>`) while the new one is built, so only the very first build makes anyone wait. Older builds of the same set are untagged after a new one succeeds.
- **Prompt.** Each add-on that is on adds a short guide to its commands and their limits to the agent's prompt, after the tool list.
- **Sign-in** and the login command keep using the plain sandbox image; the login volume is the same.

The first build needs internet access from the Docker host (Debian packages and Python wheels) and takes several minutes; the `cad` add-on is the slowest.

## Adding an add-on

1. Make a folder `addons/<id>/` with an `install.sh`. It runs as root during the build, with the folder copied to `/opt/unire-addons/<id>`. Put commands in `addons/<id>/bin/`; `install.sh` links them into `/usr/local/bin`.
2. Add an `Addon` to `All` in `addons/addons.go` (id, name, summary, size, `Env` if the tools need environment variables, and the guide for the agent) and add the folder to the `//go:embed` line (files starting with `.` or `_`, such as `__pycache__`, are left out).
3. Add `experiments/hardware-pipeline/ci/test-<id>.sh` or a similar check, and the id to the matrix in `.github/workflows/hardware-image.yml`.

Keep the guide short and honest about what the tools cannot do. To try a set by hand: `go run ./addons/cmd/addon-context 3d-print cad | docker build --build-arg BASE=unire-sandbox:latest -t test -`.

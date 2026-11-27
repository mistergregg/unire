# Add-ons

Settings, Sandboxes & Network, **Add-ons** turns on extra tool sets for the sandbox. An add-on does two things for every run (new chats and replies) once it is saved:

1. **Image.** The run uses the add-on's image instead of the sandbox image. Each add-on image is built `FROM` the normal sandbox image (`experiments/hardware-pipeline/Dockerfile.hardware`), so the agents, git and checking tools are all still there. The sign-in check and the login command keep using the normal sandbox image; the login volume is the same.
2. **Prompt.** The agent gets a short guide to the add-on's commands and their limits, after the tool list (`Guide` in `internal/addons`).

| Add-on | Image | Size | Tools |
|---|---|---|---|
| 3D modelling and printing (`3d-print`) | `ghcr.io/mistergregg/unire-sandbox-3d:latest` (target `variant-b`) | about 6 GB | build123d, trimesh, OpenSCAD, CuraEngine, ADMesh, `unire-3d`, `unire-enclosure`, `unire-slice`, `unire-mesh-check`, `unire-stl-shot` |
| Electronics and CAD/CAM (`hardware`) | `ghcr.io/mistergregg/unire-sandbox-hardware:latest` (target `variant-c`) | about 9 GB | all of the above, plus `kicad-cli` and FreeCAD (`freecadcmd`) |

With both on, the `hardware` image is used, since it carries both tool sets. Each add-on has an optional image field for an image you build yourself; empty uses the published one.

The images are published by `.github/workflows/hardware-publish.yml` after `Build images` succeeds on `main` (or by hand). The first run after turning an add-on on waits while the image downloads; after that it is kept current like the sandbox image.

## Adding an add-on

Add an `Addon` to `All` in `internal/addons/addons.go` (id, name, summary, size, image, guide, and `Covers` if its image includes another add-on's tools), build its image `FROM` the sandbox image, and publish it. Keep the guide short and honest about what the tools cannot do.

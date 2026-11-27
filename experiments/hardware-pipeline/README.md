# CLI 3D enclosure prototype

This prototype shows what an agent can generate and inspect from a Debian command line for two-part, FDM-printed electronics enclosures. It is an engineering prototype, not a manufacturing sign-off tool.

## Run it

From the repository root, with Python packages installed and CuraEngine on `PATH`:

```sh
python3 addons/3d-print/gen_enclosure.py experiments/hardware-pipeline/examples/esp32/esp32.json experiments/hardware-pipeline/examples/esp32
python3 experiments/hardware-pipeline/check_enclosure.py --test
```

To generate G-code and a slice summary:

```sh
addons/3d-print/bin/unire-slice experiments/hardware-pipeline/examples/esp32/base.stl /tmp/esp32.gcode
```

`unire-slice` uses the bundled CuraEngine 4.x definition files and `profiles/petg_0.2mm.json`. The printer geometry and temperatures are generic example settings; set them for the actual printer before using the G-code. The script reports time, layer count, and material volume from CuraEngine's slice output. It does not treat the G-code header's summary fields as authoritative.

The Cura definition files' source, LGPL-3.0 license, retrieval date, and checksums are recorded in [`addons/3d-print/profiles/NOTICE.md`](../../addons/3d-print/profiles/NOTICE.md). The PETG profile is project-authored.

## Using it in Unire

The tools now ship as add-ons: `addons/3d-print/` (generators, `unire-3d`, `unire-slice`, profiles) and `addons/cad/` (KiCad, FreeCAD). Turn them on in Settings, alone or together (see [`docs/addons.md`](../../docs/addons.md)); Unire builds one image with them and tells the agent about the commands. This folder keeps the examples, `check_enclosure.py`, the add-on checks in `ci/`, and the write-up.

## Other 3D & hardware scenarios (drones, gadgets, CNC plates)

- `unire-3d` (`mesh_tools.py`) adds `info`, `views`, `fix`, `scale`, `convert`, `dxf` (2D profile extraction for CNC/laser plates), `assemble` (multi-part interference/collision check + color-coded 4-view render), and `relief` (image-to-relief/lithophane). [`SCENARIOS.md`](SCENARIOS.md) maps each kind of request to the right tool and states its limits.
- `gen_drone.py` demonstrates a multi-part 2.5-inch micro-quadcopter (`examples/micro-quad/`): unibody X-frame plate exported to STL, STEP, and CNC-ready DXF, 20×20mm FC stack mockup, TPU canopy, prop-clearance math, and zero-collision assembly check.
- `unire-slice --material pla|petg|tpu` selects bundled generic PLA, PETG, or flexible 95A TPU profiles (`profiles/`).

## What works

- `gen_enclosure.py` reads board and enclosure JSON and writes base/lid STL and STEP files, three headless PNG previews using a Pillow z-buffer renderer, and `report.json`.
- Base has insert pilot holes, standoffs, port openings and a tongue. The lid has screw clearance holes at the same mounting locations and a matching groove.
- `check_enclosure.py` checks watertight meshes, cavity dimensions, standoffs and lid holes against the actual mesh, port opening clearance envelopes using ray/triangle probes, nominal wall thickness, and base/lid footprint and joint parameters.
- `report.json` starts with `checks_passed: false`; a successful checker run records the passing results and flips it to true. Failed checks leave it false.
- `check_enclosure.py --test` runs both examples and expects deliberately broken height, 5 mm port alignment, missing standoff, missing lid holes, and 1.2 mm wall cases to fail with diagnostic messages.
- CuraEngine 4.13.0 was extracted to `/tmp` without root along with its four runtime libraries (2,793,360 bytes downloaded; about 8.3 MB installed-size metadata). It sliced the generated ESP32 base to a 653,702 byte G-code file in under one second. With this bundled profile, its run output reported 3,255 seconds (54m 15s), 66 layers, and 7,724 mm³ of filament, about 3.21 m of 1.75 mm filament. These are slicer estimates for the example settings, not measured printer results. CuraEngine's G-code header for this stripped profile still contains placeholders (`TIME:6666`, `Filament used: 0m`, and sentinel bounds); the wrapper intentionally reports the run output instead. This G-code is a slicing proof, not a file to send to a printer.
- Both example previews were inspected. The first renders showed false diagonal seams from plotting STL triangles through matplotlib's 3D painter. The renderer now uses a z-buffered software rasterizer. The assembled views show the closed enclosure with four lid screw openings; separate lid previews show the same holes clearly.

## What is not verified

- Fit, strength, heat-set insert dimensions, port alignment to a real connector, lid retention, and print tolerances have not been validated by a physical print. The provided board/port dimensions are illustrative approximations.
- The checker confirms nominal wall thickness from the JSON rule, not every local wall in the STL. Port and hole probes check sample rays, not complete dimensional metrology or self-intersections.
- CuraEngine ran and produced G-code, but the machine profile is generic and not associated with a specific printer. Use a printer-specific profile and inspect the toolpath before printing.
- OpenSCAD's extracted AppImage was previously tested for command-line STL export; offscreen PNG rendering failed without an X server. PrusaSlicer failed to start because WebKit/GTK dependencies were absent. `kicad-cli` and FreeCAD were not run in this sandbox; their proposed image target is not evidence that they work here. `apt-get update` in this non-root sandbox fails because apt lists are not writable.
- The earlier pip run measured about 148 MB of downloads and 26 seconds. In this rerun the installed Python environment occupies 1,045,432,233 bytes (about 997 MiB). The larger download/install footprint is a material image cost; OpenSCAD and the slicer are much smaller by comparison.

## How to provide the Python stack

The three options have different costs:

| Method | Image size added | First session | Later sessions |
| --- | ---: | ---: | ---: |
| Bake Python stack into the image | about 997 MiB, measured | no install delay | no install delay |
| Cache it in a persistent volume | 0 MiB in the image | about 26 s and 148 MB download once | no install if retained |
| Install per session | 0 MiB in the image | about 26 s and 148 MB download | same cost every session |

For this prototype, the best tradeoff is a persistent cache at `/home/node/.cache/unire/hardware-venv`. Unire's Docker runner mounts the same configured agent login volume at `/home/node` for each session, and Sandbox Settings expose the image and that volume name. That makes the cache survive sessions without a ~1 GiB image layer. It does share the existing login volume; a separate cache volume would need a new mount setting in Unire. The add-ons (`addons/`) bake the stack into an image built on the owner's server instead, so it is there offline and only built once per sandbox image update.

The non-Python package figures in `COMPARISON.md` are apt metadata estimates collected during the earlier package audit, not measured image deltas. The current unprivileged sandbox has no apt package lists, so those figures could not be independently refreshed in this run.

## What not to do unattended

- Never send G-code to a physical printer unattended.
- Do not trust enclosure clearances or press fits without a test print and measurement.
- Do not treat mesh checks as electrical or mechanical certification.

## Next steps

1. Import real board geometry from KiCad with `kicad-cli` and validate PCB DRC in an image where it is installed and tested.
2. Replace the generic Cura settings with printer/material-specific profiles and compare estimates to actual prints.
3. Print a tolerance coupon for the lid joint, screw holes, and connector openings, then update the design clearances from measured results.

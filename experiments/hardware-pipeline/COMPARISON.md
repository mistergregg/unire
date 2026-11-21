# CLI hardware tools: measured prototype comparison

The goal is to decide what can be added to Unire's Debian sandbox for agent-driven enclosure work. These configurations are proposals backed by the runs described below; a proposed Docker target is not itself proof that its tools work.

## Capabilities observed

| Capability | Python-only stack | Add CuraEngine / OpenSCAD / ADMesh | Add KiCad / FreeCAD |
| --- | --- | --- | --- |
| Parametric enclosure STL and STEP | Demonstrated with build123d | Same | Same |
| Headless PNG previews | Demonstrated with trimesh and a Pillow z-buffer software rasterizer | Same | Not separately tested |
| Watertightness and design checks | Demonstrated by prototype checker | Same | Same |
| Slicer G-code, layer count, time and material volume | No slicer | CuraEngine 4.13.0 run on ESP32 base; output: 66 layers, 3,255 s, 7,724 mm³ filament | Not separately tested |
| OpenSCAD CSG and STL export | No | Extracted AppImage previously exported STL; offscreen PNG failed without X | Not separately tested |
| PCB DRC / Gerber / PCB STEP | No | No | **Not demonstrated in this sandbox** |
| FreeCAD CAM | No | No | **Not demonstrated in this sandbox** |

CuraEngine's time and material figures are slicer estimates for the bundled generic example profile. The G-code header itself contains placeholder time/material/bounds values with this stripped profile, so the wrapper uses CuraEngine's run output; do not send the resulting file to a printer. They are not exact physical print time or weight. No printer was connected.

## Size and startup costs

| Component / option | Download | Installed / image size | Evidence type |
| --- | ---: | ---: | --- |
| Python environment: build123d, trimesh, numpy, matplotlib and dependencies | about 148 MB | 1,045,432,233 bytes (about 997 MiB) | Download/time from earlier pip run; installed disk measured in this run |
| CuraEngine 4.13.0 plus libArcus, polyclipping, protobuf and gomp | 2,793,360 bytes | about 8.3 MB by Debian Installed-Size metadata | Deb files downloaded and extracted in `/tmp`; metadata total |
| OpenSCAD plus dependencies | about 18.7 MB | about 57 MB | Earlier apt metadata audit; not refreshed here |
| ADMesh plus dependencies | about 58 KB | about 152 KB | Earlier apt metadata audit; not refreshed here |
| Setup B apt tools (OpenSCAD, CuraEngine, ADMesh) | about 21.6 MB | about 65.6 MB | Earlier apt metadata audit; not refreshed here |
| Setup C additional KiCad + FreeCAD packages | about 270 MB | about 1,052 MB | Earlier apt metadata audit; not refreshed here; tools not run here |

The current non-root sandbox has no apt package lists and `apt-get update` fails because `/var/lib/apt/lists` is not writable. Therefore apt totals above are estimates from the earlier audit, not measured Docker image deltas. The Dockerfile proposal comments preserve that distinction.

### Python stack placement

- **Baked image:** add about 997 MiB of Python packages to every image, with no session install delay.
- **Persistent volume:** add 0 MiB to the image; first use costs about 26 seconds and 148 MB of downloads, later sessions reuse the venv.
- **Per session:** add 0 MiB to the image; pay about 26 seconds and 148 MB each session.

Recommended for current Unire: cache the environment at `/home/node/.cache/unire/hardware-venv`. `internal/runner/docker.go` mounts the same configured login volume at `/home/node` in sessions; Sandbox Settings expose the image and login volume name. This is a practical persistent location without the image cost, though it couples the cache to the login volume. A separate cache volume would require a new configurable mount. Bake the Python stack only for an opt-in image when offline first-use availability justifies the ~997 MiB.

## Real CuraEngine slice

The extracted CuraEngine executable and four Debian runtime libraries ran without root. `unire-slice` uses bundled Cura 4.13 definitions and `profiles/petg_0.2mm.json`. Slicing the generated ESP32 base completed in under one second and produced a 653,702 byte G-code file. CuraEngine reported 66 layers, 3,255 seconds and 7,724 mm³ of filament (about 3.21 m at the profile's 1.75 mm diameter). This verifies the CLI route, not the generic profile's suitability for a particular printer.

## Recommendation

Add CuraEngine and the tested `unire-slice` wrapper/profile to a small hardware tool image; its extracted runtime is about 8.3 MB installed by package metadata and it completed a real slice. OpenSCAD can add command-line CSG/STL export, while preview PNGs should use the tested Pillow z-buffer renderer. Keep the roughly 997 MiB Python environment in a persistent `/home/node` cache for general sessions, with an opt-in baked image for workflows that require immediate offline availability. Do not advertise KiCad or FreeCAD as verified until they have been run in the target image and their commands have passed real fixture checks.

# 3D Hardware Tooling & Sandbox Comparison

This document evaluates three configurations for enabling autonomous agents to design, verify, and fabricate 3D-printed electronics enclosures and PCBs entirely from the command line in Unire's Debian 12 (node:22-slim) container sandbox.

---

## 1. The Three Evaluated Setups

* **Setup A (Status Quo + pip only):** No base image modification. An agent creates a temporary Python virtual environment in `/tmp/hw` and installs `build123d`, `trimesh`, `numpy`, and `matplotlib`.
* **Setup B (Small Additions):** Setup A pre-baked into `/opt/hardware` plus lightweight CLI utilities installed via `apt`: **OpenSCAD** (CSG CAD), **CuraEngine** (headless CLI slicing & G-code engine), and **ADMesh** (STL manifold verification & repair).
* **Setup C (Heavy ECAD + MCAD + CAM):** Setup B plus **KiCad** (`kicad-cli` for PCB Design Rule Checks, Gerber export, and STEP 3D board export) and **FreeCAD** (headless Python API for CAM toolpaths and STEP interchange).

---

## 2. Comparison Matrix

The table below compares Setups A, B, and C evaluated on the two reference boards: **ESP32-DevKitC** (51.5 x 28.3 mm) and **Raspberry Pi Zero 2 W** (65.0 x 30.0 mm).

| Evaluation Criteria | Setup A (Pip Only, No Image Change) | Setup B (Small Additions: OpenSCAD + CuraEngine + ADMesh) | Setup C (Heavy: Setup B + KiCad + FreeCAD) |
| :--- | :--- | :--- | :--- |
| **Enclosure 3D Models** | **Yes** (Watertight STL + B-Rep STEP via `build123d`) | **Yes** (STL + STEP via `build123d` + OpenSCAD CSG) | **Yes** (STL + STEP via `build123d`, OpenSCAD, and FreeCAD) |
| **Preview Pictures** | **Yes** (Shaded isometric PNGs via `trimesh` + `matplotlib` Agg) | **Yes** (`trimesh` + `matplotlib` Agg; OpenSCAD requires X11/xvfb) | **Yes** (`matplotlib` + `kicad-cli pcb render` for 3D PCB) |
| **Filament Estimate** | **Approximation** (Volume mm³ × density × infill model) | **Exact** (Layer-by-layer G-code toolpath analysis via `CuraEngine`) | **Exact** (Layer-by-layer G-code toolpath analysis via `CuraEngine`) |
| **Print-Time & Slicing** | **No** (Cannot generate machine G-code) | **Yes** (Full `.gcode` generation & print time via `CuraEngine`) | **Yes** (Full `.gcode` generation & print time via `CuraEngine`) |
| **Mesh Validation** | **Yes** (`trimesh` manifold & watertight checks) | **Yes** (`trimesh` + dedicated fast C binary `admesh`) | **Yes** (`trimesh` + `admesh` + FreeCAD Mesh workbench) |
| **PCB Design-Rule Check** | **No** (Regex/syntax heuristic only) | **No** (Regex/syntax heuristic only) | **Yes** (Full electrical & clearance DRC via `kicad-cli pcb drc`) |
| **PCB STEP / Gerber Export**| **No** | **No** | **Yes** (`kicad-cli pcb export gerber/step`) |
| **CAM / CNC Milling** | **No** | **No** | **Yes** (FreeCAD Path workbench for CNC toolpaths) |
| **Apt Download Size** | **0 MB** | **~21.6 MB** | **~292 MB** |
| **Apt Installed Size** | **0 MB** | **~65.6 MB** | **~1,118 MB** (~1.12 GB) |
| **Pip Installed Size** | **~1,007 MB** (installed per session in `/tmp`) | **~1,007 MB** (baked into `/opt/hardware` layer) | **~1,007 MB** (baked into `/opt/hardware` layer) |
| **Total Disk Added to Image**| **0 MB** (ephemeral in `/tmp`) | **~1,073 MB** (~1.07 GB) | **~2,125 MB** (~2.12 GB) |
| **Install Time** | **~26.0s** per session on startup | **0s** (Pre-baked in image; build: ~45s) | **0s** (Pre-baked in image; build: ~2m 10s) |
| **Reliability & Execution** | **Worked first time** for CAD, STEP, STL, PNG rendering | **High** (All tools run headless with zero X11/GUI deps) | **Moderate** (KiCad DRC works; 3D renders need Mesa llvmpipe) |
| **Key Risks** | Session startup penalty (26s + 1GB download each run) | None (minimal size addition, zero network runtime cost) | 2.1 GB image bloat; heavier layers to pull and cache |

---

## 3. Package & Dependency Audit

Package metadata collected directly from Debian 12 (`bookworm`) and `bookworm-backports`:

### Setup B Packages:
1. **`cura-engine` (v4.13.0):**
   * Download: `1.67 MB` (total with deps: `2.81 MB`)
   * Installed: `4.56 MB` (total with deps: `8.51 MB`)
   * Dependencies: `libarcus3`, `libpolyclipping22`, `libprotobuf32`, `libgomp1`.
   * *Why CuraEngine over PrusaSlicer:* PrusaSlicer upstream AppImages and Debian packages depend on `libwebkit2gtk-4.1`, `wxWidgets`, and desktop GTK3 stacks (>100 MB download, >300 MB disk). Without a full desktop environment, PrusaSlicer crashes on startup with missing shared libraries (`libwebkit2gtk-4.1.so.0: cannot open shared object file`). CuraEngine is a dedicated headless C++ slicing engine with zero GUI dependencies that slices directly via CLI (`CuraEngine slice -j ... -o out.gcode -l model.stl`).
2. **`openscad` (v2021.01):**
   * Download: `3.08 MB` (total with deps: `18.7 MB`)
   * Installed: `9.34 MB` (total with deps: `57.0 MB`)
   * Note: OpenSCAD exports STL and CSG headless without issue. However, rendering offscreen PNGs requires an X display or `xvfb`; running `openscad -o out.png` in a headless container without an X server fails with `Segmentation fault` on `OpenGL OffscreenView`. The Python `matplotlib` Agg renderer avoids this completely.
3. **`admesh` (v0.98.4):**
   * Download: `22.8 KB` (total with deps: `58 KB`)
   * Installed: `60.0 KB` (total with deps: `152 KB`)
   * Fast, lightweight tool for headless STL manifold verification, hole detection, and normal repair.

### Setup C Packages (Added on top of Setup B):
1. **`kicad` (v9.0.8 via backports or v6.0.11 via Debian):**
   * Download: `40.5 MB` (total with deps: `~160 MB`)
   * Installed: `161.5 MB` (total with deps: `~642 MB`)
   * Provides: `/usr/bin/kicad-cli`, enabling CLI Design Rule Checks (`pcb drc`), Gerber fabrication outputs (`pcb export gerber/drill`), and board `.step` extraction (`pcb export step`).
2. **`freecad` + `freecad-python3` (v0.20.2):**
   * Download: `20.0 MB` (total with deps: `~110 MB`)
   * Installed: `129.2 MB` (total with deps: `~410 MB`)
   * Provides: Headless Python scripting interface (`import FreeCAD`, `import Path`) for CNC milling toolpaths, G-code generation, and B-Rep STEP analysis.

---

## 4. Proposed `unire-*` Commands

To give agents first-class CLI ergonomics consistent with `unire-diff`, `unire-shot`, and `unire-lsp`, four commands are implemented in `experiments/hardware-pipeline/bin/`:

### 1. `unire-enclosure <board.json> [outdir]`
* **Purpose:** Reads a board description JSON and generates production-ready `base.stl`, `lid.stl`, `base.step`, `lid.step`, isometric shaded preview PNGs, and `report.json` with dimensional and material calculations.
* **Setup Required:** **Setup A** (runs today!).
* **Example Output:**
  ```
  Generating enclosure: outer=56.5x33.3x16.9mm (wall=2.0mm)
  Generated successfully in examples/esp32/:
    STLs: base.stl (172 KB), lid.stl (2.3 KB)
    STEPs: base.step (116 KB), lid.step (44 KB)
    Previews: preview_assembled.png (55 KB)
    Report: report.json
  ```

### 2. `unire-stl-shot <model.stl> <out.png> [WIDTHxHEIGHT]`
* **Purpose:** Headless isometric rendering of any STL model to a shaded PNG with directional lighting. No display or X server required.
* **Setup Required:** **Setup A** (runs today!).
* **Example Output:** `ok out.png (840x700)`

### 3. `unire-mesh-check <model.stl>`
* **Purpose:** Validates 3D meshes for 3D printing: watertightness, 2-manifold topology, bounding dimensions, surface area, and volume. Exits 0 on manifold solid; exits 1 with diagnostic on holes or self-intersections.
* **Setup Required:** **Setup A** (`trimesh`), boosted by **Setup B** (`admesh`).
* **Example Output:**
  ```
  File:           base.stl
  Facets:         3,440
  Vertices:       1,720
  Bounding box:   56.50 x 33.30 x 13.30 mm
  Volume:         7622.71 mm3
  Watertight:     True
  [PASS] Mesh is a watertight, manifold solid.
  ```

### 4. `unire-kicad-check <board.kicad_pcb> [report.json]`
* **Purpose:** Runs formal Design Rule Checks on KiCad PCB layouts using `kicad-cli pcb drc`. In Setup A/B, performs S-expression format and clearance boundary validation and informs the agent of Setup C capabilities.
* **Setup Required:** **Setup C** for native DRC; basic validation in **Setup A/B**.

---

## 5. Text to Add to Agent Tool Prompt (`internal/tools checkingTools`)

When adding these commands to the sandbox image, append the following block to `checkingTools` in `internal/tools/tools.go`:

```markdown
- unire-enclosure <board.json> [outdir] - generate 3D printable enclosure STLs, STEP CAD models, preview PNGs, and report.json from a board description.
- unire-stl-shot <model.stl> <out.png> [WIDTHxHEIGHT] - render an STL 3D model to a shaded isometric PNG image headless.
- unire-mesh-check <model.stl> - verify that an STL is watertight and 2-manifold before printing or slicing. Exits 1 if non-manifold or has holes.
- unire-kicad-check <board.kicad_pcb> [report.json] - run electrical design rule check (DRC) and netlist clearance checks on a PCB layout.
```

---

## 6. Final Recommendation

### **Adopt Setup B (The Pragmatic Sweet Spot)**

**Rationale:**
1. **High Leverage, Minimal Bloat:** Setup B adds only **~66 MB of apt packages** (`openscad`, `cura-engine`, `admesh`). Baking the ~1 GB Python environment into the image eliminates the **26-second delay and 150 MB download** that every agent session currently incurs in Setup A.
2. **True Closed-Loop 3D Printing:** With `cura-engine`, the agent transitions from guessing print parameters to generating real G-code, observing exact nozzle toolpaths, layer counts, and real slicing times.
3. **Clean Headless Execution:** Unlike PrusaSlicer, CuraEngine has no WebKit/GTK dependencies and never segfaults headless.
4. **Setup C should be an Opt-in Specialist Image:** Setup C adds **1.05 GB of additional apt packages** (KiCad and FreeCAD), bringing the total addition to over **2.1 GB**. For general sandbox sessions that do not touch raw PCB routing or CNC milling, this overhead slows image pulls in CI and local environments. Setup C should be maintained as `unire-hardware:variant-c` (built by `.github/workflows/hardware-image.yml`) for dedicated hardware tasks.

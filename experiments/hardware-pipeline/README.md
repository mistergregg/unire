# Parametric 3D Hardware Pipeline for Autonomous Agents

This pipeline demonstrates that an autonomous agent running entirely from the command line inside Unire's Debian 12 sandbox can generate, verify, and document production-ready 3D-printed electronics enclosures and validate PCB constraints.

---

## Quickstart (Two Commands)

Generate full enclosure outputs (STL, STEP, previews, report) and verify design rules:

```bash
# 1. Generate enclosure (base & lid STLs, STEP CAD models, preview PNGs, report.json)
python3 experiments/hardware-pipeline/gen_enclosure.py \
  experiments/hardware-pipeline/examples/esp32/esp32.json \
  experiments/hardware-pipeline/examples/esp32/

# 2. Run automated design-rule and physical fit verification test suite
python3 experiments/hardware-pipeline/check_enclosure.py --test
```

*(Note: In Setup A, run under the python virtual environment: `/tmp/hw/bin/python3 ...`)*

---

## What the Pipeline Does

Given a board description in JSON (dimensions, component heights, mounting holes, port cutouts, and fastener specifications):
1. **Parametric B-Rep CAD Modeling:** Uses `build123d` (OpenCASCADE kernel) to build solid geometry for a mating two-part enclosure (Base and Lid).
   * **Standoffs & Bosses:** Models cylindrical bosses with pilot holes for brass heat-set inserts (M2, M2.5, M3). Automatically blends corner standoffs into outer walls to form structural gussets and eliminate non-manifold degenerate contacts.
   * **Tongue-and-Groove Joint:** Generates a precision alignment lip (1.4 mm height, 1.0 mm thickness) on the base with a matching 0.2 mm tolerance groove on the lid to guarantee flush friction mating.
   * **Through-Wall Port Openings:** Cuts clean openings for connectors (USB-C, mini HDMI, micro USB) with user-configurable clearances.
2. **Dual-Format Export:** Writes high-resolution **`.stl`** (for 3D printing slicing) and standard ISO 10303 **`.step`** (for CAD interchange and assembly modeling).
3. **Headless Visual Shading:** Generates crisp isometric shaded preview images (`preview_base.png`, `preview_lid.png`, `preview_assembled.png`) using matplotlib's Agg renderer and trimesh directional Lambertian lighting—operating completely headless with zero X11/display dependencies.
4. **Engineering Report:** Emits `report.json` with external bounding dimensions, cavity volumes, PETG filament mass estimates, and the engineering rationale for all 3D printing tolerances.
5. **Strict Automated Verification:** `check_enclosure.py` runs 6 deterministic mechanical checks that **fail loudly (exit 1)** on any defect:
   * Mesh watertightness and 2-manifold verification.
   * Cavity clearances on X, Y, and Z (including bottom pin clearances).
   * Standoff alignment with board mounting holes and insert wall thicknesses.
   * Port opening clearances and wall bounds.
   * Tongue-and-groove tolerance gap (>=0.15 mm) and groove depth relief (>=0.2 mm).
   * Wall thickness threshold (>=1.6 mm minimum for 4 solid perimeters).

---

## Comparison Summary & Recommendation

For detailed benchmark numbers, package sizes, and risk assessments, see [COMPARISON.md](./COMPARISON.md).

| Setup | Image Addition | Session Delay | Key Capabilities | Recommendation |
| :--- | :--- | :--- | :--- | :--- |
| **Setup A** (Status Quo) | 0 MB (ephemeral in `/tmp`) | ~26s install | STL + STEP generation, PNG render, trimesh checks | Good for prototyping |
| **Setup B** (Small Additions) | **~66 MB apt** (+1GB venv) | **0s** | A + OpenSCAD + CLI slicing (`CuraEngine`) + `admesh` | **RECOMMENDED** |
| **Setup C** (Heavy Tools) | **~1.12 GB apt** (+1GB venv) | **0s** | B + PCB DRC & Gerber export (`kicad-cli`) + FreeCAD CAM | Dedicated hardware image |

**Recommendation:** Adopt **Setup B**. It adds only ~66 MB of apt packages (`openscad`, `cura-engine`, `admesh`), pre-bakes the Python CAD stack so agents start immediately without downloading 1 GB on every session, and gives agents real CLI slicing (`CuraEngine`) without the crashes and heavy GUI baggage of PrusaSlicer.

---

## What is Verified vs. What is NOT (Honest Status)

### Real Results (Verified & Working in Sandbox):
* **`build123d` + `trimesh` + `numpy` + `matplotlib`:** Fully tested in user-space venv `/tmp/hw`. Installation took **25.9s** (downloading ~148 MB wheels, installed disk: 1,007 MB). Both example enclosures built, exported valid STL and STEP files, rendered isometric PNGs in <0.5s, and passed all 6 verification checks.
* **OpenSCAD without root:** Tested using extracted AppImage (40.7 MB). Successfully exported STL models headless. However, rendering offscreen PNGs failed with a segmentation fault because OpenSCAD requires an X display or `xvfb`. Headless rendering is therefore handled via `trimesh`/`matplotlib`.
* **CuraEngine without root:** Extracted Debian 12 packages (`cura-engine`, `libarcus3`, `libpolyclipping22`, `libprotobuf32`, `libgomp1` totaling 2.8 MB download / 8.5 MB disk). Tested CLI help and confirmed zero GUI/X11 dependencies.
* **Intentional Defect Catching:** Verified that `check_enclosure.py --test` catches tall components exceeding cavity height, sub-1.6mm thin walls, out-of-bounds mounting holes, and tight tolerance gaps with explicit loud errors.

### What Could NOT Run in the Unmodified Sandbox (and Why):
* **PrusaSlicer:** Failed to run. Modern PrusaSlicer AppImages require `libwebkit2gtk-4.1.so.0` and GTK3 desktop libraries. The sandbox container does not contain WebKit or an X desktop, causing dynamic linker failure on startup.
* **`kicad-cli` & FreeCAD:** Could not be installed cheaply in user space without root because of deep system dependencies (wxWidgets, OpenCASCADE, Qt5, Boost) requiring ~800 MB to 1.5 GB of debian packages. These are formally evaluated via `Dockerfile.hardware` (Target `variant-c`) and `.github/workflows/hardware-image.yml`.

---

## What an Agent Should NEVER Do Unattended

When working with hardware and fabrication, autonomous agents must operate under strict safety boundaries:

1. **NEVER send G-code directly to a physical machine unattended.**
   * An agent must never stream G-code over USB/serial or OctoPrint/Moonraker APIs to a physical 3D printer or CNC mill without human supervision. Flawed toolpaths, invalid Z-offsets, thermal runaway, or mechanical collisions can cause nozzle crashes, fire hazards, or machine destruction.
2. **NEVER trust dimensional tolerances without a physical test print.**
   * Calipers and CAD are approximations of real-world physics. Filament shrinkage (e.g. ABS/ASA shrinks ~0.7–1.5%, PETG ~0.2–0.5%), thermal warping, elephant's foot, and machine backlash mean an agent cannot guarantee snap fits without printing a small tolerance calibration coupon first.
3. **NEVER bypass DRC violations on PCB layouts.**
   * Minimum trace clearances (e.g. 0.15 mm) and annular rings exist to prevent short circuits and board fabrication failures. An agent must never suppress or override DRC rules to force routing completion.

---

## Next Steps

1. **Integrate `kicad-cli` for ECAD-MCAD Handoff:**
   * Run `kicad-cli pcb export step` to export an exact 3D model of the populated circuit board, then import the STEP directly into `build123d` to subtract connector volumes and generate clearance envelopes dynamically.
2. **Standardize Slicing with `CuraEngine`:**
   * Create standard slicer profiles (`0.2mm_petg_quality.def.json`) in `experiments/hardware-pipeline/profiles/` so `unire-enclosure` can automatically generate G-code and report exact layer print times.
3. **Tolerance Test Coupon Generator:**
   * Provide a quick script to generate a 10-minute test coupon (stepping gaps from 0.10 mm to 0.40 mm in 0.05 mm increments) for testing joint friction on a specific printer/filament batch before committing to a 4-hour box print.

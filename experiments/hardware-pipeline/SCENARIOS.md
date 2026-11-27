# What to do for each kind of 3D / hardware request

`unire-3d` (see `mesh_tools.py`) works on any STL/STEP/OBJ/3MF, so the same loop applies to every build:
model or import each part, `unire-3d info` it, check multi-part fit with `unire-3d assemble`, inspect `unire-3d views`, then slice or export DXF/STEP.

| Request | Route | Honest limits |
| --- | --- | --- |
| Box, case, bracket, mount, spacer, gadget shell with dimensions | Write `build123d` code (see `gen_enclosure.py`), export STL/STEP | Needs real measurements; physical fit is untested until printed |
| Electronics enclosure | `unire-enclosure` + `check_enclosure.py` | Illustrative board JSON unless imported from real calipers / KiCad STEP |
| Multi-part drone / robot / gadget assembly | `build123d` parts (see `gen_drone.py`) + `unire-3d assemble assembly.json out.png` | Checks static overlap and clearances; does not simulate aerodynamics, thrust, or vibration |
| Carbon-fiber / laser-cut / CNC 2D plates | `unire-3d dxf plate.step out.dxf` (or `build123d.ExportDXF`) | Produces 2D cut geometry; CAM toolpaths require FreeCAD Path (Setup C) + machine setup |
| Rigid vs flexible 3D print slicing | `unire-slice --material pla\|petg\|tpu model.stl out.gcode` | Generic profiles for time/filament estimates; re-slice in your own slicer before printing |
| Custom circuit board (flight controller, sensor, PDB) | `kicad-cli` (`unire-kicad-check`, Gerber export, STEP export) in Setup C | Layout and routing are code/S-expression or manual; always verify DRC + Gerbers visually |
| "Print this photo" (flat) | `unire-3d relief photo.png out.stl --mode lithophane` (backlit) or `--mode relief` | Heightmap only, not a true 3D model. Use high-contrast images, `--invert` if inverted |
| Photo of an object, with a size | Agent reads the photo and models simple geometry in `build123d` | Only simple prismatic shapes; ask for key measurements |
| Organic / sculpted / figurine from a photo | Not supported here (needs a generative 3D mesh or sculpting tool) | Download an STL instead, then use `info`, `fix`, `scale`, `views` |
| Downloaded STL (Printables, Thingiverse) | `info` -> `fix` -> `scale` -> `views` -> `unire-slice` | Unit mix-ups are flagged by `info` |
| STEP / CAD assembly file | `unire-3d convert in.step out.stl` or `unire-3d dxf` | Large assemblies can be slow |

Always re-slice STLs in your own slicer (Cura, PrusaSlicer, OrcaSlicer, Bambu Studio) with your printer's calibrated profile before printing, and never run unattended CNC/printer G-code.

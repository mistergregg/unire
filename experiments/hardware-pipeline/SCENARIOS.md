# What to do for each kind of 3D print request

`unire-3d` (see `mesh_tools.py`) works on any STL/STEP/OBJ/3MF, so the same loop applies to every scenario:
make or get a model, `unire-3d info` it, `unire-3d views` it and look at the PNG, fix or scale it, then slice.

| Request | Route | Honest limits |
| --- | --- | --- |
| Box, case, bracket, mount, spacer, adapter with dimensions | Write build123d code (see `gen_enclosure.py`), export STL/STEP | Needs real measurements; fit is untested until printed |
| Electronics enclosure | `unire-enclosure` + `check_enclosure.py` | Illustrative board data |
| "Print this photo" (flat) | `unire-3d relief photo.png out.stl --mode lithophane` (backlit picture, bright = thin) or `--mode relief` (bright = tall) | Heightmap only, not a true 3D model. Use high-contrast images, `--invert` if it looks wrong |
| Photo of an object, with a size | Agent reads the photo and models simple geometry in build123d | Only simple prismatic shapes; ask for key measurements |
| Organic/sculpted/figurine from a photo | Not supported here (needs a mesh generator or sculpting tool) | Download an STL instead, then use `info`, `fix`, `scale`, `views` |
| Downloaded STL (Printables, Thingiverse) | `info` -> `fix` -> `scale` -> `views` | Unit mix-ups are flagged by `info` |
| STEP/CAD file | `unire-3d convert in.step out.stl` | Large assemblies can be slow |
| Text, plaques, signs, keychains | build123d `Text` + extrude | Fonts depend on system fonts |
| Check "will it print?" | `unire-3d info` (watertight, bed fit, thin parts, overhang share) then `unire-slice` | Estimates, not printer-ready G-code |

Always re-slice the STL in the owner's own slicer with the printer's profile before printing.

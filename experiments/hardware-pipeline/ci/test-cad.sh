#!/bin/sh
# Checks the Electronics and CAD/CAM add-on inside an image that has it. Run from the repo root.
set -e
PCB=experiments/hardware-pipeline/examples/sample.kicad_pcb
kicad-cli --version
# Debian's FreeCAD 0.20 with Python 3.11 segfaults on `freecadcmd --version`
# (in Py_EncodeLocale while parsing that option); ask the Python API instead.
freecadcmd -c "import FreeCAD; print('FreeCAD', '.'.join(FreeCAD.Version()[:3]))"
unire-kicad-check $PCB
mkdir -p /tmp/kicad_out
kicad-cli pcb export gerbers -o /tmp/kicad_out/gerbers $PCB && ls /tmp/kicad_out/gerbers
kicad-cli pcb export step -o /tmp/kicad_out/sample.step $PCB && test -s /tmp/kicad_out/sample.step
freecadcmd -c "import FreeCAD, Part, Path; s = Part.read('/tmp/kicad_out/sample.step'); print('FreeCAD STEP solids:', len(s.Solids), 'Path module:', hasattr(Path, 'Command'))"
echo "cad add-on: all checks passed"

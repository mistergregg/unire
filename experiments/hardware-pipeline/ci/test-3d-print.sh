#!/bin/sh
# Checks the 3D modelling add-on inside an image that has it. Run from the repo root.
set -e
E=experiments/hardware-pipeline/examples
openscad --version
CuraEngine help | head -n 3
admesh --version
python3 -c "import build123d, trimesh, numpy, PIL; print('build123d', build123d.__version__, 'trimesh', trimesh.__version__)"

unire-enclosure $E/esp32/esp32.json /tmp/esp32
unire-enclosure $E/pizero/pizero.json /tmp/pizero
python3 experiments/hardware-pipeline/check_enclosure.py --test

unire-mesh-check /tmp/esp32/base.stl
unire-stl-shot /tmp/esp32/base.stl /tmp/test_shot.png 600x500 && test -s /tmp/test_shot.png
unire-slice /tmp/esp32/base.stl /tmp/esp32.gcode && test -s /tmp/esp32.gcode
unire-slice --material pla /tmp/esp32/base.stl /tmp/esp32_pla.gcode && test -s /tmp/esp32_pla.gcode
unire-slice --material tpu /tmp/esp32/lid.stl /tmp/esp32_tpu.gcode && test -s /tmp/esp32_tpu.gcode
unire-3d info --material petg /tmp/esp32/base.stl
unire-3d views /tmp/esp32/base.stl /tmp/views.png && test -s /tmp/views.png
python3 -c "import sys; from PIL import Image; Image.radial_gradient(sys.argv[1]).save(sys.argv[2])" L /tmp/pic.png
unire-3d relief /tmp/pic.png /tmp/lith.stl --mode lithophane --res 60
unire-3d info /tmp/lith.stl
python3 "$UNIRE_HARDWARE_HOME/gen_drone.py" $E/micro-quad/drone.json /tmp/quad
unire-3d assemble /tmp/quad/assembly.json /tmp/quad_assembly.png && test -s /tmp/quad_assembly.png
unire-3d dxf /tmp/quad/frame_plate.step /tmp/quad_frame.dxf && test -s /tmp/quad_frame.dxf
echo "3d-print add-on: all checks passed"

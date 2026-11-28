#!/bin/sh
# 3D modelling and printing add-on. Runs as root while the image is built; this
# folder is at /opt/unire-addons/3d-print. The env it needs (PATH,
# UNIRE_HARDWARE_HOME, UNIRE_HARDWARE_PROFILE_DIR) is set by the add-on (addons.go).
set -eu
here=/opt/unire-addons/3d-print

apt-get update
apt-get install -y --no-install-recommends openscad cura-engine admesh libgl1-mesa-dri
rm -rf /var/lib/apt/lists/*

# Python CAD stack in its own venv, so the system python stays as it was.
python3 -m venv /opt/hardware
/opt/hardware/bin/pip install --no-cache-dir --upgrade pip
/opt/hardware/bin/pip install --no-cache-dir build123d==0.13.0 trimesh==5.2.1 numpy==2.4.6 pillow

chmod 755 "$here"/bin/*
for f in "$here"/bin/*; do ln -sf "$f" /usr/local/bin/"$(basename "$f")"; done

#!/bin/sh
# Electronics and CAD add-on: KiCad (kicad-cli) and FreeCAD (freecadcmd).
# Runs as root while the image is built; this folder is at /opt/unire-addons/cad.
set -eu
here=/opt/unire-addons/cad

# kicad-cli is only in the backports kicad package. Its hwloc plugin must come
# from backports too, or apt mixes libhwloc15 versions and gives up.
echo "deb http://deb.debian.org/debian bookworm-backports main" > /etc/apt/sources.list.d/unire-backports.list
apt-get update
apt-get install -y --no-install-recommends -t bookworm-backports kicad libhwloc-plugins
apt-get install -y --no-install-recommends freecad freecad-python3
rm -rf /var/lib/apt/lists/*

chmod 755 "$here"/bin/*
for f in "$here"/bin/*; do ln -sf "$f" /usr/local/bin/"$(basename "$f")"; done

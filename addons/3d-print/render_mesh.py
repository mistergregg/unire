#!/usr/bin/env python3
"""Render one STL using the pipeline's z-buffered preview renderer."""
import sys

import trimesh

from gen_enclosure import render_mesh_to_png


def main():
    if len(sys.argv) != 4:
        raise SystemExit("Usage: render_mesh.py <model.stl> <out.png> <WIDTHxHEIGHT>")
    width, height = (int(value) for value in sys.argv[3].lower().split("x", 1))
    mesh = trimesh.load(sys.argv[1])
    render_mesh_to_png(mesh, sys.argv[2], figsize=(width / 140, height / 140))
    print(f"ok {sys.argv[2]} ({width}x{height})")


if __name__ == "__main__":
    main()

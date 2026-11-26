#!/usr/bin/env python3
"""General-purpose 3D print helpers for any scenario (not only enclosures).

  info   <model>                     size, volume, watertight, overhang share, bed fit
  views  <model> <out.png>           front/side/top/iso contact sheet to look at
  fix    <model> <out.stl>           merge vertices, fix normals, fill small holes
  scale  <model> <out.stl> [--max-dim MM | --factor F | --x/--y/--z MM]
  relief <image> <out.stl>           photo -> raised relief or lithophane
  convert <model> <out.stl>          STEP/OBJ/3MF/PLY -> STL (STEP via build123d)
"""
import argparse
import sys

import numpy as np
import trimesh


def load(path):
    if path.lower().endswith((".step", ".stp")):
        from build123d import export_stl, import_step
        import tempfile, os
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "m.stl")
            export_stl(import_step(path), out)
            return trimesh.load(out, force="mesh")
    mesh = trimesh.load(path, force="mesh")
    if not isinstance(mesh, trimesh.Trimesh) or len(mesh.faces) == 0:
        raise SystemExit(f"error: {path} has no triangles")
    return mesh


def cmd_info(a):
    m = load(a.model)
    size = m.extents
    bed = [float(v) for v in a.bed.lower().split("x")]
    # overhang: faces pointing down more than 45 deg from vertical, above the bed
    down = m.face_normals[:, 2] < -np.cos(np.radians(45))
    off_bed = m.triangles_center[:, 2] > m.bounds[0][2] + 0.05
    over = float(m.area_faces[down & off_bed].sum() / m.area) if m.area else 0.0
    print(f"File:        {a.model}")
    print(f"Size (mm):   {size[0]:.1f} x {size[1]:.1f} x {size[2]:.1f}")
    print(f"Triangles:   {len(m.faces):,}")
    print(f"Watertight:  {m.is_watertight}")
    problems = []
    if not m.is_watertight:
        problems.append("not watertight: run 'fix' (or the slicer may drop parts)")
    else:
        print(f"Volume:      {abs(m.volume) / 1000:.2f} cm3 solid "
              f"(~{abs(m.volume) * 1.24e-3 * a.infill:.1f} g PLA at ~{int(a.infill * 100)}% effective fill, rough)")
    print(f"Parts:       {len(m.split(only_watertight=False))} separate bodies")
    print(f"Overhang:    {over * 100:.0f}% of surface faces down >45 deg (supports likely above ~20%)")
    if min(size[0], size[1]) > min(bed[0], bed[1]) or max(size[0], size[1]) > max(bed[0], bed[1]) or size[2] > bed[2]:
        problems.append(f"does not fit a {a.bed} mm bed: scale it or split the part")
    if min(size) < 0.8:
        problems.append("a dimension is under 0.8 mm: too thin to print")
    if max(size) < 5 or max(size) > 1000:
        problems.append("unusual size: units may not be millimetres (try 'scale --factor 25.4' or 10)")
    for p in problems:
        print("WARN:", p)
    print("Verdict:    ", "check warnings above" if problems else "looks printable (no physical test done)")
    return 1 if problems else 0


def cmd_views(a):
    from gen_enclosure import render_meshes_to_png
    from PIL import Image
    import os, tempfile
    m = load(a.model)
    m.apply_translation(-m.bounds.mean(axis=0))
    views = [("iso", (28, 45)), ("front", (0, 0)), ("side", (0, 90)), ("top", (89, 0))]
    imgs = []
    with tempfile.TemporaryDirectory() as d:
        for name, v in views:
            p = os.path.join(d, name + ".png")
            render_meshes_to_png([(m, (0.22, 0.45, 0.75))], p, size=(420, 350), view=v)
            imgs.append(Image.open(p).convert("RGB").copy())
    w, h = imgs[0].size
    sheet = Image.new("RGB", (w * 2, h * 2), "white")
    for i, img in enumerate(imgs):
        sheet.paste(img, ((i % 2) * w, (i // 2) * h))
    sheet.save(a.out)
    s = m.extents
    print(f"ok {a.out}  ({s[0]:.1f} x {s[1]:.1f} x {s[2]:.1f} mm; views: iso, front, side, top)")


def cmd_fix(a):
    m = load(a.model)
    before = m.is_watertight
    m.merge_vertices()
    m.update_faces(m.nondegenerate_faces())
    m.remove_unreferenced_vertices()
    trimesh.repair.fix_normals(m)
    trimesh.repair.fill_holes(m)
    if m.is_watertight and m.volume < 0:
        m.invert()
    m.export(a.out)
    print(f"watertight: {before} -> {m.is_watertight}; wrote {a.out}")
    return 0 if m.is_watertight else 1


def cmd_scale(a):
    m = load(a.model)
    if a.factor:
        m.apply_scale(a.factor)
    elif a.max_dim:
        m.apply_scale(a.max_dim / m.extents.max())
    elif a.x or a.y or a.z:
        axis, val = [(i, v) for i, v in enumerate((a.x, a.y, a.z)) if v][0]
        m.apply_scale(val / m.extents[axis])
    else:
        raise SystemExit("give --factor, --max-dim, or one of --x/--y/--z")
    m.apply_translation([-m.bounds[0][0], -m.bounds[0][1], -m.bounds[0][2]])
    m.export(a.out)
    s = m.extents
    print(f"wrote {a.out}: {s[0]:.1f} x {s[1]:.1f} x {s[2]:.1f} mm")


def cmd_relief(a):
    from PIL import Image, ImageOps
    img = Image.open(a.image).convert("L")
    if a.invert:
        img = ImageOps.invert(img)
    img = ImageOps.autocontrast(img)
    w = a.res
    h = max(2, round(img.height * w / img.width))
    z = np.asarray(img.resize((w, h), Image.LANCZOS), dtype=float) / 255.0
    # lithophane: bright = thin (light passes). relief: bright = tall.
    z = (1 - z) if a.mode == "lithophane" else z
    height = a.base + z * (a.max_height - a.base)
    px = a.width / (w - 1)
    xs, ys = np.meshgrid(np.arange(w) * px, np.arange(h)[::-1] * px)
    top = np.stack([xs, ys, height], axis=-1).reshape(-1, 3)
    bot = top.copy()
    bot[:, 2] = 0
    n = w * h
    idx = np.arange(n).reshape(h, w)
    faces = []
    a_, b_, c_, d_ = idx[:-1, :-1].ravel(), idx[:-1, 1:].ravel(), idx[1:, 1:].ravel(), idx[1:, :-1].ravel()
    faces += [np.stack([a_, d_, c_], 1), np.stack([a_, c_, b_], 1)]                    # top (up)
    faces += [np.stack([a_ + n, c_ + n, d_ + n], 1), np.stack([a_ + n, b_ + n, c_ + n], 1)]  # bottom (down)
    def wall(i0, i1):  # outward for edge going i0->i1 along boundary (ccw seen from above)
        return [np.stack([i0, i0 + n, i1 + n], 1), np.stack([i0, i1 + n, i1], 1)]
    # boundary loop ccw seen from above (y up): bottom row L->R, right col, top row R->L, left col
    loop = np.concatenate([idx[-1, :], idx[-2::-1, -1], idx[0, -2::-1], idx[1:-1, 0]])
    nxt = np.roll(loop, -1)
    faces += wall(loop, nxt)
    mesh = trimesh.Trimesh(np.vstack([top, bot]), np.vstack(faces), process=True)
    trimesh.repair.fix_normals(mesh)
    mesh.export(a.out)
    s = mesh.extents
    print(f"wrote {a.out}: {s[0]:.1f} x {s[1]:.1f} x {s[2]:.1f} mm, watertight={mesh.is_watertight}, mode={a.mode}")
    return 0 if mesh.is_watertight else 1


def cmd_convert(a):
    m = load(a.model)
    m.export(a.out)
    print(f"wrote {a.out}: {len(m.faces):,} triangles")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("info"); s.add_argument("model")
    s.add_argument("--bed", default="220x220x250"); s.add_argument("--infill", type=float, default=0.3)
    s.set_defaults(f=cmd_info)
    s = sub.add_parser("views"); s.add_argument("model"); s.add_argument("out"); s.set_defaults(f=cmd_views)
    s = sub.add_parser("fix"); s.add_argument("model"); s.add_argument("out"); s.set_defaults(f=cmd_fix)
    s = sub.add_parser("scale"); s.add_argument("model"); s.add_argument("out")
    s.add_argument("--factor", type=float); s.add_argument("--max-dim", type=float)
    s.add_argument("--x", type=float); s.add_argument("--y", type=float); s.add_argument("--z", type=float)
    s.set_defaults(f=cmd_scale)
    s = sub.add_parser("relief"); s.add_argument("image"); s.add_argument("out")
    s.add_argument("--mode", choices=["relief", "lithophane"], default="relief")
    s.add_argument("--width", type=float, default=80, help="model width in mm")
    s.add_argument("--res", type=int, default=120, help="pixels across (more = slower)")
    s.add_argument("--base", type=float, default=0.8, help="thinnest thickness mm")
    s.add_argument("--max-height", type=float, default=3.0, help="thickest mm")
    s.add_argument("--invert", action="store_true")
    s.set_defaults(f=cmd_relief)
    s = sub.add_parser("convert"); s.add_argument("model"); s.add_argument("out"); s.set_defaults(f=cmd_convert)
    a = p.parse_args()
    sys.exit(a.f(a) or 0)


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""General-purpose 3D print & multi-part hardware helpers (drones, gadgets, enclosures).

  info     <model>                        size, volume, watertight, overhang share, bed fit
  views    <model> <out.png>              front/side/top/iso contact sheet to look at
  fix      <model> <out.stl>              merge vertices, fix normals, fill small holes
  scale    <model> <out.stl>              scale by --max-dim MM | --factor F | --x/--y/--z MM
  relief   <image> <out.stl>              photo -> raised relief or lithophane
  convert  <model> <out.stl>              STEP/OBJ/3MF/PLY -> STL (STEP via build123d)
  dxf      <model> <out.dxf>              extract 2D cut profile for CNC/laser plates
  assemble <assembly.json> <out.png>      check multi-part interference and render 4-view sheet
"""
import argparse
import json
import os
import sys

import numpy as np
import trimesh


MATERIAL_DENSITIES_G_CM3 = {
    "pla": 1.24,
    "petg": 1.27,
    "tpu": 1.21,
    "abs": 1.04,
    "cf-nylon": 1.20,
    "cf": 1.55,
}


def load(path):
    if path.lower().endswith((".step", ".stp")):
        from build123d import export_stl, import_step
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "m.stl")
            export_stl(import_step(path), out)
            return trimesh.load(out, force="mesh")
    mesh = trimesh.load(path, force="mesh")
    if not isinstance(mesh, trimesh.Trimesh) or len(mesh.faces) == 0:
        raise SystemExit(f"error: {path} has no triangles")
    return mesh


def _points_inside_mesh(mesh, points):
    """Dependency-free ray-parity check (+Z ray) against mesh triangles."""
    if len(points) == 0:
        return np.zeros(0, dtype=bool)
    triangles = mesh.triangles
    bmin, bmax = mesh.bounds
    in_box = np.all((points >= bmin - 1e-6) & (points <= bmax + 1e-6), axis=1)
    inside = np.zeros(len(points), dtype=bool)
    if not np.any(in_box):
        return inside

    pts = points[in_box]
    v0 = triangles[:, 0]
    e1 = triangles[:, 1] - v0
    e2 = triangles[:, 2] - v0
    # +Z ray: direction = (0, 0, 1), so h = dir x e2 = (-e2_y, e2_x, 0)
    det = -e1[:, 0] * e2[:, 1] + e1[:, 1] * e2[:, 0]
    valid = np.abs(det) > 1e-10
    v0 = v0[valid]
    e1 = e1[valid]
    e2 = e2[valid]
    inv_det = 1.0 / det[valid]

    tri_xy_min = np.minimum(np.minimum(v0[:, :2], (v0 + e1)[:, :2]), (v0 + e2)[:, :2])
    tri_xy_max = np.maximum(np.maximum(v0[:, :2], (v0 + e1)[:, :2]), (v0 + e2)[:, :2])

    hits_parity = np.zeros(len(pts), dtype=bool)
    for i, p in enumerate(pts):
        mask = (
            (p[0] >= tri_xy_min[:, 0] - 1e-7) & (p[0] <= tri_xy_max[:, 0] + 1e-7) &
            (p[1] >= tri_xy_min[:, 1] - 1e-7) & (p[1] <= tri_xy_max[:, 1] + 1e-7)
        )
        if not np.any(mask):
            continue
        sx = p[0] - v0[mask, 0]
        sy = p[1] - v0[mask, 1]
        sz = p[2] - v0[mask, 2]
        inv = inv_det[mask]
        u = inv * (-sx * e2[mask, 1] + sy * e2[mask, 0])
        v = inv * (e1[mask, 0] * sy - e1[mask, 1] * sx)
        qx = sy * e1[mask, 2] - sz * e1[mask, 1]
        qy = sz * e1[mask, 0] - sx * e1[mask, 2]
        qz = sx * e1[mask, 1] - sy * e1[mask, 0]
        t = inv * (e2[mask, 0] * qx + e2[mask, 1] * qy + e2[mask, 2] * qz)
        hit = (u >= 0.0) & (v >= 0.0) & (u + v <= 1.0) & (t > 1e-5)
        hits_parity[i] = (int(np.sum(hit)) % 2) == 1

    inside[in_box] = hits_parity
    return inside


def _meshes_collide(m1, m2, tol=0.08, samples=400):
    """Return True if m1 and m2 strictly overlap by more than tol mm."""
    b1, b2 = m1.bounds, m2.bounds
    if np.any(b1[1] <= b2[0] + tol) or np.any(b2[1] <= b1[0] + tol):
        return False
    rng = np.random.default_rng(42)
    for src, dst in ((m1, m2), (m2, m1)):
        pts, fidx = trimesh.sample.sample_surface(src, samples, seed=rng)
        inward_pts = pts - src.face_normals[fidx] * tol
        lo = np.maximum(b1[0], b2[0])
        hi = np.minimum(b1[1], b2[1])
        cand = inward_pts[np.all((inward_pts >= lo) & (inward_pts <= hi), axis=1)]
        if len(cand) > 0 and np.any(_points_inside_mesh(dst, cand)):
            return True
    return False


def cmd_info(a):
    m = load(a.model)
    size = m.extents
    bed = [float(v) for v in a.bed.lower().split("x")]
    density = MATERIAL_DENSITIES_G_CM3[a.material]
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
              f"(~{abs(m.volume) * (density / 1000.0) * a.infill:.1f} g {a.material.upper()} "
              f"at ~{int(a.infill * 100)}% effective fill, rough)")
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


def _render_four_views(mesh_colors, out_path, bounds_center=None):
    from gen_enclosure import render_meshes_to_png
    from PIL import Image
    import tempfile
    if bounds_center is None:
        all_v = np.vstack([m.vertices for m, _ in mesh_colors])
        bounds_center = (all_v.min(axis=0) + all_v.max(axis=0)) / 2.0
    shifted = []
    for m, col in mesh_colors:
        mc = m.copy()
        mc.apply_translation(-bounds_center)
        shifted.append((mc, col))
    views = [("iso", (28, 45)), ("front", (0, 0)), ("side", (0, 90)), ("top", (89, 0))]
    imgs = []
    with tempfile.TemporaryDirectory() as d:
        for name, v in views:
            p = os.path.join(d, name + ".png")
            render_meshes_to_png(shifted, p, size=(420, 350), view=v)
            imgs.append(Image.open(p).convert("RGB").copy())
    w, h = imgs[0].size
    sheet = Image.new("RGB", (w * 2, h * 2), "white")
    for i, img in enumerate(imgs):
        sheet.paste(img, ((i % 2) * w, (i // 2) * h))
    sheet.save(out_path)


def cmd_views(a):
    m = load(a.model)
    _render_four_views([(m, (0.22, 0.45, 0.75))], a.out)
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
    faces += [np.stack([a_, d_, c_], 1), np.stack([a_, c_, b_], 1)]
    faces += [np.stack([a_ + n, c_ + n, d_ + n], 1), np.stack([a_ + n, b_ + n, c_ + n], 1)]
    def wall(i0, i1):
        return [np.stack([i0, i0 + n, i1 + n], 1), np.stack([i0, i1 + n, i1], 1)]
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


def cmd_dxf(a):
    if a.model.lower().endswith((".step", ".stp")):
        import build123d as bd
        shape = bd.import_step(a.model)
        z = a.z if a.z is not None else float(shape.bounding_box().center().Z)
        sec = bd.Pos(0, 0, -z) * bd.section(shape, bd.Plane.XY.offset(z))
        exporter = bd.ExportDXF()
        exporter.add_shape(sec)
        exporter.write(a.out)
        print(f"wrote {a.out} (STEP section at Z={z:.2f} mm via build123d)")
        return 0
    m = load(a.model)
    z = a.z if a.z is not None else float(m.bounds[:, 2].mean())
    sec = m.section(plane_origin=[0, 0, z], plane_normal=[0, 0, 1])
    if sec is None or len(sec.entities) == 0:
        raise SystemExit(f"error: empty cross-section at Z={z:.2f} mm")
    p2d, _ = sec.to_2D()
    p2d.export(a.out)
    print(f"wrote {a.out} ({len(p2d.entities)} entities at Z={z:.2f} mm)")
    return 0


def cmd_assemble(a):
    with open(a.spec, encoding="utf-8") as f:
        spec = json.load(f)
    base_dir = os.path.dirname(os.path.abspath(a.spec))
    palette = [
        (0.20, 0.25, 0.32),
        (0.20, 0.55, 0.35),
        (0.85, 0.45, 0.15),
        (0.25, 0.50, 0.80),
        (0.65, 0.25, 0.65),
    ]
    loaded = []
    for idx, part in enumerate(spec.get("parts", [])):
        fpath = part["file"]
        if not os.path.isabs(fpath):
            fpath = os.path.join(base_dir, fpath)
        m = load(fpath).copy()
        rot = part.get("rotate_deg")
        if rot:
            mat = trimesh.transformations.euler_matrix(
                np.radians(rot[0]), np.radians(rot[1]), np.radians(rot[2])
            )
            m.apply_transform(mat)
        trans = part.get("translate")
        if trans:
            m.apply_translation(trans)
        col = tuple(part.get("color", palette[idx % len(palette)]))
        loaded.append((part["name"], m, col, bool(part.get("allow_touch", False)), part.get("material", "petg")))

    if not loaded:
        raise SystemExit("error: assembly spec has no parts")

    print(f"Assembly: {spec.get('name', os.path.basename(a.spec))} ({len(loaded)} parts)")
    total_mass = 0.0
    for name, m, _, _, mat in loaded:
        s = m.extents
        dens = MATERIAL_DENSITIES_G_CM3.get(mat.lower(), 1.27)
        mass = abs(m.volume) * (dens / 1000.0) if m.is_watertight else 0.0
        total_mass += mass
        print(f"  - {name:16s} {s[0]:5.1f} x {s[1]:5.1f} x {s[2]:5.1f} mm  watertight={m.is_watertight}  ~{mass:.1f}g ({mat.upper()})")

    ignored = {tuple(sorted(pair)) for pair in spec.get("ignore_pairs", [])}
    collisions = []
    for i in range(len(loaded)):
        for j in range(i + 1, len(loaded)):
            n1, m1, _, allow1, _ = loaded[i]
            n2, m2, _, allow2, _ = loaded[j]
            if allow1 or allow2 or tuple(sorted((n1, n2))) in ignored:
                continue
            if _meshes_collide(m1, m2):
                collisions.append((n1, n2))

    _render_four_views([(m, col) for _, m, col, _, _ in loaded], a.out)
    if a.out_stl:
        combined = trimesh.util.concatenate([m for _, m, _, _, _ in loaded])
        combined.export(a.out_stl)
        print(f"wrote combined STL: {a.out_stl}")

    if collisions:
        for n1, n2 in collisions:
            print(f"[COLLISION] Parts '{n1}' and '{n2}' overlap!", file=sys.stderr)
        print(f"ok {a.out} (rendered with collisions flagged)")
        return 1
    print(f"[PASS] Zero part collisions; total solid mass ~{total_mass:.1f}g")
    print(f"ok {a.out}")
    return 0


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("info"); s.add_argument("model")
    s.add_argument("--bed", default="220x220x250"); s.add_argument("--infill", type=float, default=0.3)
    s.add_argument("--material", choices=sorted(MATERIAL_DENSITIES_G_CM3), default="pla")
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
    s = sub.add_parser("dxf"); s.add_argument("model"); s.add_argument("out")
    s.add_argument("--z", type=float, help="Section plane height in mm (default: mid-Z)")
    s.set_defaults(f=cmd_dxf)
    s = sub.add_parser("assemble"); s.add_argument("spec"); s.add_argument("out")
    s.add_argument("--out-stl", help="Optional combined assembly STL")
    s.set_defaults(f=cmd_assemble)
    a = p.parse_args()
    sys.exit(a.f(a) or 0)


if __name__ == "__main__":
    main()

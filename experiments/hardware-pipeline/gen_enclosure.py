#!/usr/bin/env python3
"""
gen_enclosure.py: Parametric 3D printable electronics enclosure generator.

Reads a board JSON specification and generates:
- base.stl and lid.stl
- base.step and lid.step (standard B-Rep STEP format)
- preview_base.png, preview_lid.png, preview_assembled.png (isometric shaded views)
- report.json (outer dimensions, volumes, estimated PETG filament weight, parameter rationale)

Designed for FDM 3D printing with tongue-and-groove joint, heat-set threaded inserts,
and port openings with standard 3D printing tolerances.
"""

import sys
import os
import json
import math
import numpy as np
import trimesh
import build123d as bd

from PIL import Image

# Heat-set threaded insert standards (dimensions in mm)
# Sourced from standard CNC Kitchen / McMaster-Carr M2/M2.5/M3 brass insert specs:
INSERT_SPECS = {
    "M2": {
        "pilot_diameter": 3.1,  # Standard hole diameter for M2 brass insert
        "boss_diameter": 5.2,   # Gives ~1.05mm annular boss wall to prevent layer splitting
        "pilot_depth": 4.0,     # Depth for 3.0-4.0mm long insert
        "screw_clearance": 2.2  # Through-hole clearance diameter
    },
    "M2.5": {
        "pilot_diameter": 3.6,  # Standard hole diameter for M2.5 brass insert
        "boss_diameter": 6.0,   # Gives ~1.2mm annular boss wall
        "pilot_depth": 4.8,     # Depth for 4.0-4.5mm long insert
        "screw_clearance": 2.8
    },
    "M3": {
        "pilot_diameter": 4.0,  # Standard hole diameter for M3 brass insert
        "boss_diameter": 7.0,   # Gives ~1.5mm annular boss wall
        "pilot_depth": 5.5,     # Depth for 4.5-5.0mm long insert
        "screw_clearance": 3.4
    }
}

# 3D Printing Material Properties:
# PETG density: 1.27 g/cm3 (0.00127 g/mm3)
PETG_DENSITY_G_MM3 = 0.00127


def render_meshes_to_png(mesh_colors, out_path, size=(840, 700), view=(28, 45)):
    """Rasterize shaded STL triangles with a depth buffer to avoid false seams."""
    scale = 2
    width, height = size[0] * scale, size[1] * scale
    all_vertices = np.vstack([mesh.vertices for mesh, _ in mesh_colors])
    azimuth, elevation = np.radians(view[1]), np.radians(view[0])
    rotation = np.array([
        [np.cos(azimuth), np.sin(azimuth), 0],
        [-np.sin(azimuth) * np.sin(elevation), np.cos(azimuth) * np.sin(elevation), np.cos(elevation)],
        [np.sin(azimuth) * np.cos(elevation), -np.cos(azimuth) * np.cos(elevation), np.sin(elevation)],
    ])
    projected = all_vertices @ rotation.T
    low, high = projected[:, :2].min(axis=0), projected[:, :2].max(axis=0)
    span = np.maximum(high - low, 1e-6)
    pixels_per_unit = min((width - 48 * scale) / span[0], (height - 48 * scale) / span[1])
    center = (low + high) / 2
    depth = np.full((height, width), -np.inf, dtype=np.float32)
    canvas = np.full((height, width, 3), 255, dtype=np.uint8)
    light = np.array([0.45, 0.65, 0.95], dtype=float)
    light /= np.linalg.norm(light)

    for mesh, base_color in mesh_colors:
        transformed = mesh.vertices @ rotation.T
        screen = np.empty((len(transformed), 2), dtype=float)
        screen[:, 0] = width / 2 + (transformed[:, 0] - center[0]) * pixels_per_unit
        screen[:, 1] = height / 2 - (transformed[:, 1] - center[1]) * pixels_per_unit
        for face, normal in zip(mesh.faces, mesh.face_normals):
            tri = screen[face]
            x0 = max(0, int(np.floor(tri[:, 0].min())))
            x1 = min(width - 1, int(np.ceil(tri[:, 0].max())))
            y0 = max(0, int(np.floor(tri[:, 1].min())))
            y1 = min(height - 1, int(np.ceil(tri[:, 1].max())))
            if x0 > x1 or y0 > y1:
                continue
            yy, xx = np.mgrid[y0:y1 + 1, x0:x1 + 1]
            px, py = xx + 0.5, yy + 0.5
            denom = ((tri[1, 1] - tri[2, 1]) * (tri[0, 0] - tri[2, 0]) +
                     (tri[2, 0] - tri[1, 0]) * (tri[0, 1] - tri[2, 1]))
            if abs(denom) < 1e-9:
                continue
            a = ((tri[1, 1] - tri[2, 1]) * (px - tri[2, 0]) +
                 (tri[2, 0] - tri[1, 0]) * (py - tri[2, 1])) / denom
            b = ((tri[2, 1] - tri[0, 1]) * (px - tri[2, 0]) +
                 (tri[0, 0] - tri[2, 0]) * (py - tri[2, 1])) / denom
            c = 1 - a - b
            inside = (a >= -1e-5) & (b >= -1e-5) & (c >= -1e-5)
            depths = a * transformed[face[0], 2] + b * transformed[face[1], 2] + c * transformed[face[2], 2]
            zslice = depth[y0:y1 + 1, x0:x1 + 1]
            visible = inside & (depths >= zslice)
            if not np.any(visible):
                continue
            shade = float(np.clip(np.dot(normal, light), 0.3, 1.0))
            color = np.clip(np.asarray(base_color) * shade * 255, 0, 255).astype(np.uint8)
            zslice[visible] = depths[visible]
            canvas[y0:y1 + 1, x0:x1 + 1][visible] = color

    Image.fromarray(canvas).resize(size, Image.Resampling.LANCZOS).save(out_path)


def render_mesh_to_png(mesh, out_path, color_rgb=(0.22, 0.45, 0.75), view=(28, 45), figsize=(6, 5)):
    """Render a trimesh mesh to a shaded isometric PNG image."""
    size = (int(figsize[0] * 140), int(figsize[1] * 140))
    render_meshes_to_png([(mesh, color_rgb)], out_path, size=size, view=view)


def render_assembled_view(base_mesh, lid_mesh, out_path, base_height):
    """Render the lid seated on the base at the lip-and-groove joint."""
    lid_placed = lid_mesh.copy()
    lid_placed.apply_translation((0, 0, base_height))
    render_meshes_to_png([(base_mesh, (0.20, 0.42, 0.70)),
                          (lid_placed, (0.35, 0.68, 0.88))], out_path, size=(980, 840))


def generate_enclosure(spec, out_dir):
    """Main generation logic given parsed JSON specification."""
    os.makedirs(out_dir, exist_ok=True)
    
    board = spec["board"]
    enc = spec["enclosure"]
    
    board_l = float(board["length"])
    board_w = float(board["width"])
    board_t = float(board["thickness"])
    comp_top = float(board.get("component_height_top", 4.0))
    comp_bot = float(board.get("component_height_bottom", 1.5))
    
    wall = float(enc.get("wall_thickness", 2.0))
    clearance = float(enc.get("clearance", 0.5))
    standoff_h = float(enc.get("standoff_height", 4.0))
    screw_size = enc.get("screw_size", "M2.5").upper()
    
    if screw_size not in INSERT_SPECS:
        raise ValueError(f"Unsupported screw size: {screw_size}. Supported: {list(INSERT_SPECS.keys())}")
    
    insert_info = INSERT_SPECS[screw_size]
    boss_od = insert_info["boss_diameter"]
    pilot_id = insert_info["pilot_diameter"]
    pilot_depth = min(insert_info["pilot_depth"], standoff_h + wall * 0.5)
    
    # Cavity internal dimensions
    cavity_l = board_l + 2.0 * clearance
    cavity_w = board_w + 2.0 * clearance
    
    # Outer box dimensions
    outer_l = cavity_l + 2.0 * wall
    outer_w = cavity_w + 2.0 * wall
    
    # Tongue-and-Groove lip dimensions
    # Standard FDM fit: 1.4mm lip height, half wall thickness, 0.2mm tolerance gap
    lip_h = 1.4
    lip_t = wall / 2.0
    lip_gap = 0.20  # Horizontal tolerance gap for sliding/snap fit
    
    # Base height: floor + standoffs + board thickness + margin for ports
    ports = board.get("ports", [])
    max_port_top = 0.0
    for p in ports:
        ptop = float(p.get("height_above_board", 0.0)) + float(p["height"])
        if ptop > max_port_top:
            max_port_top = ptop
            
    base_cavity_h = max(standoff_h + board_t + max_port_top + 1.2, standoff_h + board_t + 2.5)
    base_h = base_cavity_h + wall
    
    # Lid cavity height: covers tallest top component plus clearance
    required_top_space = comp_top + clearance
    carried_by_base = base_cavity_h - (standoff_h + board_t)
    lid_cavity_h = max(required_top_space - carried_by_base, 3.0)
    lid_h = lid_cavity_h + wall
    
    total_assembled_h = base_h + lid_h
    
    print(f"Generating enclosure: outer={outer_l:.1f}x{outer_w:.1f}x{total_assembled_h:.1f}mm (wall={wall}mm)")
    
    # -------------------------------------------------------------------------
    # 1. BUILD BASE SOLID
    # -------------------------------------------------------------------------
    with bd.BuildPart() as base_builder:
        # Outer shell
        bd.Box(outer_l, outer_w, base_h, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN))
        # Inner cavity
        with bd.Locations((0, 0, wall)):
            bd.Box(cavity_l, cavity_w, base_h + 1.0, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)
        # Tongue (Lip) on base upper rim with 0.1mm overlap into rim to ensure clean manifold boolean union
        with bd.Locations((0, 0, base_h - 0.1)):
            bd.Box(cavity_l + 2.0 * lip_t, cavity_w + 2.0 * lip_t, lip_h + 0.1, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN))
            with bd.Locations((0, 0, -0.05)):
                bd.Box(cavity_l, cavity_w, lip_h + 0.2, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)
        
        # Standoffs with insert pilot holes (overlap 0.1mm into floor)
        mounting_holes = board.get("mounting_holes", [])
        for hole in mounting_holes:
            bx = float(hole["x"])
            by = float(hole["y"])
            # Map board origin (0,0) to centered cavity
            cx = bx - board_l / 2.0
            cy = by - board_w / 2.0
            
            # If the standoff is near or tangent to an inner cavity wall,
            # slightly expand the boss radius (+0.5mm) so it forms a seamless
            # structural gusset merged with the wall instead of a degenerate tangent contact.
            dist_x = cavity_l / 2.0 - abs(cx)
            dist_y = cavity_w / 2.0 - abs(cy)
            actual_boss_r = boss_od / 2.0
            if dist_x <= actual_boss_r + 0.6 or dist_y <= actual_boss_r + 0.6:
                actual_boss_r += 0.5
                
            with bd.Locations((cx, cy, wall - 0.1)):
                bd.Cylinder(actual_boss_r, standoff_h + 0.1, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN))
                # Pilot hole from top of standoff
                with bd.Locations((0, 0, standoff_h + 0.1 - pilot_depth)):
                    bd.Cylinder(pilot_id / 2.0, pilot_depth + 0.2, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)
        
        # Port cutouts through outer walls
        for port in ports:
            side = port["side"].lower()
            pos = float(port["position"])
            pw = float(port["width"]) + 2.0 * clearance
            ph = float(port["height"]) + 2.0 * clearance
            hab = float(port.get("height_above_board", 0.0))
            
            # Port vertical center
            port_z = wall + standoff_h + board_t + hab + (float(port["height"]) / 2.0)
            
            if side in ["x_min", "left", "west"]:
                # Cutout on -X face
                cy = pos - board_w / 2.0
                with bd.Locations((-outer_l / 2.0, cy, port_z)):
                    bd.Box(wall * 3.0, pw, ph, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.CENTER), mode=bd.Mode.SUBTRACT)
            elif side in ["x_max", "right", "east"]:
                # Cutout on +X face
                cy = pos - board_w / 2.0
                with bd.Locations((outer_l / 2.0, cy, port_z)):
                    bd.Box(wall * 3.0, pw, ph, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.CENTER), mode=bd.Mode.SUBTRACT)
            elif side in ["y_min", "bottom", "south"]:
                # Cutout on -Y face
                cx = pos - board_l / 2.0
                with bd.Locations((cx, -outer_w / 2.0, port_z)):
                    bd.Box(pw, wall * 3.0, ph, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.CENTER), mode=bd.Mode.SUBTRACT)
            elif side in ["y_max", "top", "north"]:
                # Cutout on +Y face
                cx = pos - board_l / 2.0
                with bd.Locations((cx, outer_w / 2.0, port_z)):
                    bd.Box(pw, wall * 3.0, ph, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.CENTER), mode=bd.Mode.SUBTRACT)
            else:
                raise ValueError(f"Unknown port side: {side}")
                
    base_solid = base_builder.part
    
    # -------------------------------------------------------------------------
    # 2. BUILD LID SOLID
    # -------------------------------------------------------------------------
    # Lid modeled opening downwards (or resting on base rim)
    with bd.BuildPart() as lid_builder:
        # Outer shell
        bd.Box(outer_l, outer_w, lid_h, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN))
        # Inner cavity
        with bd.Locations((0, 0, 0)):
            bd.Box(cavity_l, cavity_w, lid_cavity_h, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)
        # Groove to receive tongue on rim
        # Groove depth = lip_h + 0.3mm clearance to prevent bottoming out
        # Groove width = lip_t + lip_gap (0.2mm tolerance)
        groove_depth = lip_h + 0.3
        groove_outer_l = cavity_l + 2.0 * (lip_t + lip_gap)
        groove_outer_w = cavity_w + 2.0 * (lip_t + lip_gap)
        with bd.Locations((0, 0, -0.01)):
            bd.Box(groove_outer_l, groove_outer_w, groove_depth + 0.01, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)
            # Retain inner boundary if needed (cavity is already subtracted)

        # Clearance holes pass through the lid roof and align with the base's
        # heat-set insert pilots. The lid is printed upside down and flipped for assembly.
        for hole in board.get("mounting_holes", []):
            cx = float(hole["x"]) - board_l / 2.0
            cy = float(hole["y"]) - board_w / 2.0
            with bd.Locations((cx, cy, lid_cavity_h - 0.1)):
                bd.Cylinder(insert_info["screw_clearance"] / 2.0, wall + 0.2,
                            align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)
            
    lid_solid = lid_builder.part
    
    # -------------------------------------------------------------------------
    # 3. EXPORT STL AND STEP
    # -------------------------------------------------------------------------
    base_stl = os.path.join(out_dir, "base.stl")
    lid_stl = os.path.join(out_dir, "lid.stl")
    base_step = os.path.join(out_dir, "base.step")
    lid_step = os.path.join(out_dir, "lid.step")
    
    bd.export_stl(base_solid, base_stl)
    bd.export_stl(lid_solid, lid_stl)
    bd.export_step(base_solid, base_step)
    bd.export_step(lid_solid, lid_step)
    
    # Load with trimesh for mesh verification and volume calculation
    m_base = trimesh.load(base_stl)
    m_lid = trimesh.load(lid_stl)
    
    base_vol = float(m_base.volume)
    lid_vol = float(m_lid.volume)
    total_vol = base_vol + lid_vol
    
    # Filament estimation:
    # 100% solid weight in grams
    solid_grams = total_vol * PETG_DENSITY_G_MM3
    # At 20% infill: walls and floors are solid perimeters (~1.6-2.0mm thick).
    # Thin enclosure walls consist of ~80% solid shells and ~20% infill in the core.
    # Effective printed volume factor = 0.82
    est_printed_grams = solid_grams * 0.85
    
    # -------------------------------------------------------------------------
    # 4. RENDER PREVIEW IMAGES
    # -------------------------------------------------------------------------
    preview_base = os.path.join(out_dir, "preview_base.png")
    preview_lid = os.path.join(out_dir, "preview_lid.png")
    preview_assembled = os.path.join(out_dir, "preview_assembled.png")
    
    render_mesh_to_png(m_base, preview_base, color_rgb=(0.20, 0.42, 0.70))
    render_mesh_to_png(m_lid, preview_lid, color_rgb=(0.35, 0.68, 0.88))
    render_assembled_view(m_base, m_lid, preview_assembled, base_height=base_h)
    
    # -------------------------------------------------------------------------
    # 5. GENERATE REPORT.JSON
    # -------------------------------------------------------------------------
    report = {
        "board_name": spec.get("name", "Unnamed Board"),
        "outer_dimensions_mm": {
            "length": round(outer_l, 2),
            "width": round(outer_w, 2),
            "height": round(total_assembled_h, 2),
            "base_height": round(base_h, 2),
            "lid_height": round(lid_h, 2)
        },
        "cavity_dimensions_mm": {
            "length": round(cavity_l, 2),
            "width": round(cavity_w, 2),
            "total_height": round(base_cavity_h + lid_cavity_h, 2),
            "base_cavity_height": round(base_cavity_h, 2),
            "lid_cavity_height": round(lid_cavity_h, 2)
        },
        "volumes_mm3": {
            "base": round(base_vol, 2),
            "lid": round(lid_vol, 2),
            "total": round(total_vol, 2)
        },
        "estimated_filament": {
            "material": "PETG",
            "density_g_cm3": 1.27,
            "solid_weight_grams": round(solid_grams, 2),
            "estimated_20pct_infill_weight_grams": round(est_printed_grams, 2)
        },
        "design_rules_and_sources": {
            "wall_thickness_mm": {
                "value": wall,
                "rationale": "Standard FDM parameter: 4-5 perimeters with a 0.4mm nozzle to provide rigidity and eliminate warping without excessive print time."
            },
            "clearance_mm": {
                "value": clearance,
                "rationale": "0.5mm drop-in clearance absorbs typical ±0.15mm FDM printer dimensional deviation and ±0.2mm PCB CNC routing edge tolerance."
            },
            "standoff_height_mm": {
                "value": standoff_h,
                "rationale": f"Elevates PCB {standoff_h}mm above floor, clearing bottom through-hole solder pins and surface mount components ({comp_bot}mm max) plus ventilation."
            },
            "heat_set_insert_spec": {
                "screw_size": screw_size,
                "pilot_diameter_mm": pilot_id,
                "boss_diameter_mm": boss_od,
                "boss_wall_annulus_mm": round((boss_od - pilot_id) / 2.0, 2),
                "rationale": f"Follows CNC Kitchen / McMaster-Carr insert standards. Annular boss wall >=1.0mm prevents hoop stress blowout during hot soldering-iron insertion."
            },
            "tongue_and_groove_joint": {
                "lip_height_mm": lip_h,
                "lip_thickness_mm": lip_t,
                "tolerance_gap_mm": lip_gap,
                "groove_depth_relief_mm": 0.3,
                "rationale": "0.2mm horizontal tolerance gap ensures smooth lid sliding/friction mating without binding; 0.3mm groove depth relief guarantees rims mate flush."
            }
        },
        "checks_passed": False,
        "checks_status": "not run; run check_enclosure.py"
    }
    
    report_file = os.path.join(out_dir, "report.json")
    with open(report_file, "w") as f:
        json.dump(report, f, indent=2)
        
    print(f"Generated successfully in {out_dir}:")
    print(f"  STLs: {os.path.basename(base_stl)} ({os.path.getsize(base_stl)} bytes), {os.path.basename(lid_stl)} ({os.path.getsize(lid_stl)} bytes)")
    print(f"  STEPs: {os.path.basename(base_step)}, {os.path.basename(lid_step)}")
    print(f"  Previews: {os.path.basename(preview_assembled)} ({os.path.getsize(preview_assembled)} bytes)")
    print(f"  Report: {os.path.basename(report_file)}")
    return report


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: python3 gen_enclosure.py <board.json> [output_dir]")
        sys.exit(1)
        
    json_path = sys.argv[1]
    output_directory = sys.argv[2] if len(sys.argv) > 2 else os.path.dirname(json_path) or "."
    
    with open(json_path, "r") as fp:
        board_spec = json.load(fp)
        
    generate_enclosure(board_spec, output_directory)

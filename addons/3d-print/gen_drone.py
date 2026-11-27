#!/usr/bin/env python3
"""
gen_drone.py: Parametric micro-quadcopter frame, FC stack mockup, and TPU canopy generator.

Demonstrates multi-part drone/gadget design from CLI:
- Exports 3D-printable & CNC-cuttable unibody frame plate (.stl, .step, .dxf)
- Exports protective camera/VTX canopy (.stl, .step)
- Exports 20x20mm flight controller + ESC stack mockup (.stl)
- Generates assembly.json and verifies zero mechanical collisions + prop clearances
- Renders color-coded 4-view assembly sheet (preview_assembly.png) and report.json
"""

import json
import math
import os
import sys

import build123d as bd
import trimesh

from mesh_tools import _meshes_collide, _render_four_views


def build_frame_plate(spec):
    wb = float(spec["wheelbase_mm"])
    t = float(spec["frame_thickness_mm"])
    arm_w = float(spec["arm_width_mm"])
    body_l = float(spec["body_length_mm"])
    body_w = float(spec["body_width_mm"])
    fc_pat = float(spec["fc_mount_pattern_mm"])
    fc_hole = float(spec["fc_hole_diameter_mm"])
    mot_pat = float(spec["motor_mount_pattern_mm"])
    mot_hole = float(spec["motor_hole_diameter_mm"])
    shaft_d = float(spec["motor_shaft_hole_mm"])

    radius = wb / 2.0
    m_off = radius / math.sqrt(2.0)
    hub_d = mot_pat + 7.0
    arm_len = radius * 2.0

    with bd.BuildPart() as frame:
        # Center electronics fuselage plate (rounded rectangle)
        with bd.BuildSketch() as body_sk:
            bd.RectangleRounded(body_l, body_w, 4.0)
        bd.extrude(amount=t)

        # Two diagonal X-arms spanning motor-to-motor
        for angle in (45.0, -45.0):
            with bd.BuildSketch() as arm_sk:
                with bd.Locations(bd.Rotation(0, 0, angle)):
                    bd.Rectangle(arm_len, arm_w)
            bd.extrude(amount=t)

        # Motor mount hubs at the 4 arm tips
        motor_centers = [
            (m_off, m_off),
            (-m_off, m_off),
            (-m_off, -m_off),
            (m_off, -m_off),
        ]
        with bd.BuildSketch() as hubs_sk:
            with bd.Locations(*motor_centers):
                bd.Circle(hub_d / 2.0)
        bd.extrude(amount=t)

        # Cutouts: motor shaft + 4x M2 holes on a 12x12mm (or mot_pat) bolt circle,
        # 20x20mm FC stack holes, and twin battery strap slots.
        sub_mot = mot_pat / math.sqrt(2.0) / 2.0
        with bd.Locations((0, 0, -0.1)):
            # Motor holes
            for mx, my in motor_centers:
                with bd.Locations((mx, my, 0)):
                    bd.Cylinder(shaft_d / 2.0, t + 0.2, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)
                    for dx in (-sub_mot, sub_mot):
                        for dy in (-sub_mot, sub_mot):
                            with bd.Locations((dx, dy, 0)):
                                bd.Cylinder(mot_hole / 2.0, t + 0.2, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)

            # 20x20mm FC mounting holes
            fc_half = fc_pat / 2.0
            for dx in (-fc_half, fc_half):
                for dy in (-fc_half, fc_half):
                    with bd.Locations((dx, dy, 0)):
                        bd.Cylinder(fc_hole / 2.0, t + 0.2, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)

            # Battery strap slots (along X, fore/aft of FC stack)
            for sx in (-16.0, 16.0):
                with bd.Locations((sx, 0, 0)):
                    bd.Box(2.8, 15.0, t + 0.2, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)

    return frame.part, motor_centers


def build_fc_mockup(spec):
    t = float(spec["frame_thickness_mm"])
    standoff = float(spec["fc_standoff_mm"])
    board_s = float(spec["fc_board_size_mm"])
    stack_h = float(spec["fc_stack_height_mm"])
    z0 = t + standoff
    with bd.BuildPart() as fc:
        with bd.Locations((0, 0, z0)):
            bd.Box(board_s, board_s, stack_h, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN))
    return fc.part


def build_canopy(spec):
    t = float(spec["frame_thickness_mm"])
    standoff = float(spec["fc_standoff_mm"])
    stack_h = float(spec["fc_stack_height_mm"])
    fc_pat = float(spec["fc_mount_pattern_mm"])
    fc_hole = float(spec["fc_hole_diameter_mm"])
    board_s = float(spec["fc_board_size_mm"])
    wall = float(spec["canopy_wall_mm"])
    canopy_h = float(spec["canopy_height_mm"])

    # Canopy clears top of FC stack by 1.5mm, seated on top standoffs
    z_mount = t + standoff + stack_h + 1.5
    outer_s = board_s + 2.0 * wall + 2.0
    inner_s = board_s + 2.0

    with bd.BuildPart() as canopy:
        # Mounting tabs at the 4 20x20mm holes
        fc_half = fc_pat / 2.0
        for dx in (-fc_half, fc_half):
            for dy in (-fc_half, fc_half):
                with bd.Locations((dx, dy, z_mount)):
                    bd.Cylinder(3.0, 2.2, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN))

        # Main protective shell starting at z_mount + 1.0mm
        with bd.Locations((0, 0, z_mount + 1.0)):
            bd.Box(outer_s, outer_s, canopy_h, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN))
            with bd.Locations((0, 0, -0.1)):
                bd.Box(inner_s, inner_s, canopy_h - wall + 0.1, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)
            # Front camera cutout (+X face)
            with bd.Locations((outer_s / 2.0, 0, canopy_h * 0.45)):
                bd.Box(wall * 3.0, 14.5, 11.5, mode=bd.Mode.SUBTRACT)
            # Side cooling vents (+/-Y faces)
            for sy in (-outer_s / 2.0, outer_s / 2.0):
                with bd.Locations((0, sy, canopy_h * 0.45)):
                    bd.Box(12.0, wall * 3.0, 5.5, mode=bd.Mode.SUBTRACT)

        # Subtract mounting screw holes through tabs
        for dx in (-fc_half, fc_half):
            for dy in (-fc_half, fc_half):
                with bd.Locations((dx, dy, z_mount - 0.1)):
                    bd.Cylinder(fc_hole / 2.0, 2.5, align=(bd.Align.CENTER, bd.Align.CENTER, bd.Align.MIN), mode=bd.Mode.SUBTRACT)

    return canopy.part


def generate_drone(spec_path, out_dir):
    with open(spec_path, encoding="utf-8") as f:
        spec = json.load(f)
    os.makedirs(out_dir, exist_ok=True)

    frame_part, motor_centers = build_frame_plate(spec)
    fc_part = build_fc_mockup(spec)
    canopy_part = build_canopy(spec)

    frame_stl = os.path.join(out_dir, "frame_plate.stl")
    frame_step = os.path.join(out_dir, "frame_plate.step")
    frame_dxf = os.path.join(out_dir, "frame_plate.dxf")
    canopy_stl = os.path.join(out_dir, "canopy.stl")
    canopy_step = os.path.join(out_dir, "canopy.step")
    fc_stl = os.path.join(out_dir, "fc_mockup.stl")

    bd.export_stl(frame_part, frame_stl, tolerance=0.02, angular_tolerance=0.25)
    bd.export_step(frame_part, frame_step)
    bd.export_stl(canopy_part, canopy_stl, tolerance=0.02, angular_tolerance=0.25)
    bd.export_step(canopy_part, canopy_step)
    bd.export_stl(fc_part, fc_stl)

    # Export 2D CNC cutting profile from mid-plane of frame plate, shifted to Z=0
    sec_z = float(spec["frame_thickness_mm"]) / 2.0
    sec = bd.Pos(0, 0, -sec_z) * bd.section(frame_part, bd.Plane.XY.offset(sec_z))
    dxf = bd.ExportDXF()
    dxf.add_shape(sec)
    dxf.write(frame_dxf)

    m_frame = trimesh.load(frame_stl)
    m_canopy = trimesh.load(canopy_stl)
    m_fc = trimesh.load(fc_stl)

    if not m_frame.is_watertight or not m_canopy.is_watertight or not m_fc.is_watertight:
        raise SystemExit("ERROR: Generated drone mesh is not watertight!")

    # Verify zero unintended mesh collisions across frame, FC stack, and canopy
    pairs = [("frame_plate", m_frame, "fc_mockup", m_fc),
             ("frame_plate", m_frame, "canopy", m_canopy),
             ("fc_mockup", m_fc, "canopy", m_canopy)]
    for n1, m1, n2, m2 in pairs:
        if _meshes_collide(m1, m2):
            raise SystemExit(f"ERROR: Mechanical collision detected between {n1} and {n2}!")

    # Prop clearance checks
    wb = float(spec["wheelbase_mm"])
    prop_d = float(spec["prop_diameter_mm"])
    board_s = float(spec["fc_board_size_mm"])
    adj_dist = math.hypot(motor_centers[0][0] - motor_centers[1][0],
                          motor_centers[0][1] - motor_centers[1][1])
    prop_tip_gap = adj_dist - prop_d
    prop_stack_gap = (wb / 2.0) - (prop_d / 2.0) - (board_s * math.sqrt(2.0) / 2.0)
    if prop_tip_gap < 3.0:
        raise SystemExit(f"ERROR: Prop-to-prop tip clearance too tight: {prop_tip_gap:.2f} mm")
    if prop_stack_gap < 2.0:
        raise SystemExit(f"ERROR: Prop-to-FC-stack clearance too tight: {prop_stack_gap:.2f} mm")

    # Write assembly.json and render 4-view assembly sheet
    assembly = {
        "name": spec.get("name", "Micro-Quad"),
        "parts": [
            {"name": "frame_plate", "file": "frame_plate.stl", "color": [0.18, 0.22, 0.28], "material": "cf"},
            {"name": "fc_mockup", "file": "fc_mockup.stl", "color": [0.18, 0.56, 0.34], "material": "petg"},
            {"name": "canopy", "file": "canopy.stl", "color": [0.90, 0.45, 0.14], "material": "tpu"},
        ],
    }
    with open(os.path.join(out_dir, "assembly.json"), "w", encoding="utf-8") as f:
        json.dump(assembly, f, indent=2)

    preview_path = os.path.join(out_dir, "preview_assembly.png")
    _render_four_views(
        [(m_frame, (0.18, 0.22, 0.28)), (m_fc, (0.18, 0.56, 0.34)), (m_canopy, (0.90, 0.45, 0.14))],
        preview_path,
    )

    frame_vol_cm3 = float(m_frame.volume) / 1000.0
    canopy_vol_cm3 = float(m_canopy.volume) / 1000.0
    report = {
        "name": spec.get("name", "Micro-Quad"),
        "wheelbase_mm": wb,
        "prop_diameter_mm": prop_d,
        "clearances_mm": {
            "adjacent_prop_tip_gap": round(prop_tip_gap, 2),
            "prop_to_fc_stack_gap": round(prop_stack_gap, 2),
        },
        "parts": {
            "frame_plate": {
                "watertight": bool(m_frame.is_watertight),
                "volume_cm3": round(frame_vol_cm3, 2),
                "estimated_mass_cf_g": round(frame_vol_cm3 * 1.55, 2),
                "estimated_mass_petg_g": round(frame_vol_cm3 * 1.27, 2),
                "exports": ["frame_plate.stl", "frame_plate.step", "frame_plate.dxf"],
            },
            "canopy": {
                "watertight": bool(m_canopy.is_watertight),
                "volume_cm3": round(canopy_vol_cm3, 2),
                "estimated_mass_tpu_g": round(canopy_vol_cm3 * 1.21, 2),
                "exports": ["canopy.stl", "canopy.step"],
            },
        },
        "collisions_detected": 0,
        "checks_passed": True,
    }
    with open(os.path.join(out_dir, "report.json"), "w", encoding="utf-8") as f:
        json.dump(report, f, indent=2)

    print(f"Generated drone assembly in {out_dir}:")
    print(f"  Frame:  CF ~{report['parts']['frame_plate']['estimated_mass_cf_g']}g / PETG ~{report['parts']['frame_plate']['estimated_mass_petg_g']}g (STL, STEP, DXF)")
    print(f"  Canopy: TPU ~{report['parts']['canopy']['estimated_mass_tpu_g']}g (STL, STEP)")
    print(f"  Clearances: prop-prop={prop_tip_gap:.1f}mm, prop-stack={prop_stack_gap:.1f}mm, collisions=0")


if __name__ == "__main__":
    if len(sys.argv) < 2:
        raise SystemExit("Usage: python3 gen_drone.py <drone.json> [output_dir]")
    json_path = sys.argv[1]
    out_dir = sys.argv[2] if len(sys.argv) > 2 else os.path.dirname(json_path) or "."
    generate_drone(json_path, out_dir)

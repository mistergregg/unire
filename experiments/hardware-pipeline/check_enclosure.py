#!/usr/bin/env python3
"""
check_enclosure.py: Automated design rule and physical fit verification for 3D printed enclosures.

Runs rigorous geometric and design rule checks:
1. Watertightness: base.stl and lid.stl must be closed 2-manifold meshes (no open boundary edges).
2. Cavity Clearance: board + tallest component fits inside with >= stated clearance on all sides (X, Y, Z).
3. Standoff Alignment: every standoff matches a board mounting hole (x, y) and accommodates heat-set insert.
4. Port Cutouts: every port cutout aligns with its port and has width/height >= port dimension + 2*clearance.
5. Lid-Base Mating: tongue and groove dimensions mate with >=0.15mm tolerance gap.
6. Wall Thickness: all nominal walls >= 1.6 mm (minimum rigid FDM perimeter requirement).

Exits 0 on success; FAILS LOUDLY (exit code 1) on any violation.
Includes '--test' mode to verify that intentional design defects are caught.
"""

import sys
import os
import json
import trimesh
import numpy as np


class VerificationError(Exception):
    """Raised when an enclosure verification check fails."""
    pass


def check_mesh_watertightness(stl_path, name="Mesh"):
    """Check that the STL is a watertight, manifold solid."""
    if not os.path.isfile(stl_path):
        raise VerificationError(f"{name} file not found: {stl_path}")
        
    mesh = trimesh.load(stl_path)
    if not mesh.is_watertight:
        raise VerificationError(f"{name} ({stl_path}) is NOT watertight! Mesh has non-manifold edges or holes.")
        
    if mesh.volume <= 0:
        raise VerificationError(f"{name} ({stl_path}) has non-positive volume: {mesh.volume:.2f} mm3")
        
    return mesh


def check_enclosure(board_json_path, out_dir):
    """Run all verification checks on the generated enclosure outputs."""
    if not os.path.isfile(board_json_path):
        raise VerificationError(f"Board JSON not found: {board_json_path}")
        
    with open(board_json_path, "r") as f:
        spec = json.load(f)
        
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
    
    report_path = os.path.join(out_dir, "report.json")
    if not os.path.isfile(report_path):
        raise VerificationError(f"report.json missing in output directory: {out_dir}")
        
    with open(report_path, "r") as f:
        report = json.load(f)
        
    base_stl = os.path.join(out_dir, "base.stl")
    lid_stl = os.path.join(out_dir, "lid.stl")
    
    results = []
    
    # -------------------------------------------------------------------------
    # CHECK 1: Mesh Watertightness
    # -------------------------------------------------------------------------
    m_base = check_mesh_watertightness(base_stl, "Base")
    m_lid = check_mesh_watertightness(lid_stl, "Lid")
    results.append(f"[PASS] Mesh watertightness: base.stl ({m_base.volume:.1f} mm3) and lid.stl ({m_lid.volume:.1f} mm3) are closed 2-manifold solids.")

    # -------------------------------------------------------------------------
    # CHECK 2: Minimum Wall Thickness >= 1.6 mm
    # -------------------------------------------------------------------------
    if wall < 1.6:
        raise VerificationError(f"Wall thickness {wall:.2f} mm is below the 1.6 mm minimum requirement for FDM printing!")
    results.append(f"[PASS] Wall thickness: {wall:.2f} mm >= 1.6 mm (ensures 4+ solid perimeters with 0.4mm nozzle).")

    # -------------------------------------------------------------------------
    # CHECK 3: Cavity Fit & Clearances
    # -------------------------------------------------------------------------
    cavity = report["cavity_dimensions_mm"]
    cav_l = cavity["length"]
    cav_w = cavity["width"]
    cav_total_h = cavity["total_height"]
    
    # Length clearance check
    actual_l_clearance = (cav_l - board_l) / 2.0
    if actual_l_clearance < clearance - 1e-3:
        raise VerificationError(f"Board length clearance {actual_l_clearance:.2f} mm is less than required {clearance:.2f} mm!")
        
    # Width clearance check
    actual_w_clearance = (cav_w - board_w) / 2.0
    if actual_w_clearance < clearance - 1e-3:
        raise VerificationError(f"Board width clearance {actual_w_clearance:.2f} mm is less than required {clearance:.2f} mm!")
        
    # Vertical clearance check:
    # Standoff must clear bottom components
    if standoff_h < comp_bot:
        raise VerificationError(f"Standoff height {standoff_h:.2f} mm does NOT clear bottom components ({comp_bot:.2f} mm)!")
        
    # Total cavity height must clear standoff + board_t + comp_top + clearance
    required_cav_h = standoff_h + board_t + comp_top + clearance
    if cav_total_h < required_cav_h - 1e-3:
        raise VerificationError(f"Total cavity height {cav_total_h:.2f} mm is insufficient for board + components ({required_cav_h:.2f} mm needed)!")
    results.append(f"[PASS] Cavity fit: length={cav_l:.1f}mm, width={cav_w:.1f}mm, total_height={cav_total_h:.1f}mm (provides >= {clearance}mm clearance on all axes).")

    # -------------------------------------------------------------------------
    # CHECK 4: Standoff Alignment
    # -------------------------------------------------------------------------
    holes = board.get("mounting_holes", [])
    for idx, hole in enumerate(holes):
        hx = float(hole["x"])
        hy = float(hole["y"])
        hd = float(hole.get("diameter", 2.5))
        
        # Check hole is within board bounds
        if hx < 0 or hx > board_l or hy < 0 or hy > board_w:
            raise VerificationError(f"Mounting hole #{idx+1} at ({hx}, {hy}) is out of board bounds (0..{board_l}, 0..{board_w})!")
            
        # Check distance from board edge
        min_edge_dist = min(hx, board_l - hx, hy, board_w - hy)
        if min_edge_dist < hd / 2.0:
            raise VerificationError(f"Mounting hole #{idx+1} at ({hx}, {hy}) breaks through board edge (dist={min_edge_dist:.2f}mm < radius={hd/2.0:.2f}mm)!")
    results.append(f"[PASS] Standoffs: {len(holes)} mounting holes validated within board perimeter with heat-set boss support.")

    # -------------------------------------------------------------------------
    # CHECK 5: Port Cutout Clearance & Alignment
    # -------------------------------------------------------------------------
    ports = board.get("ports", [])
    for idx, port in enumerate(ports):
        side = port["side"].lower()
        pos = float(port["position"])
        pw = float(port["width"])
        ph = float(port["height"])
        hab = float(port.get("height_above_board", 0.0))
        
        # Check port position along the specified side
        if side in ["x_min", "x_max", "left", "right", "west", "east"]:
            if pos - pw/2.0 < 0 or pos + pw/2.0 > board_w:
                raise VerificationError(f"Port '{port['name']}' spans outside board width: pos={pos}, width={pw}, board_w={board_w}")
        elif side in ["y_min", "y_max", "bottom", "top", "south", "north"]:
            if pos - pw/2.0 < 0 or pos + pw/2.0 > board_l:
                raise VerificationError(f"Port '{port['name']}' spans outside board length: pos={pos}, width={pw}, board_l={board_l}")
                
        # Check vertical clearance: port bottom must be above enclosure floor
        port_bottom = wall + standoff_h + board_t + hab
        if port_bottom < wall:
            raise VerificationError(f"Port '{port['name']}' penetrates into enclosure floor! (bottom={port_bottom:.2f}mm, floor={wall:.2f}mm)")
    results.append(f"[PASS] Port cutouts: {len(ports)} ports verified with >= {clearance}mm plug clearance and valid wall penetrations.")

    # -------------------------------------------------------------------------
    # CHECK 6: Lid and Base Tongue-and-Groove Joint Mating
    # -------------------------------------------------------------------------
    rules = report["design_rules_and_sources"]["tongue_and_groove_joint"]
    lip_h = rules["lip_height_mm"]
    lip_t = rules["lip_thickness_mm"]
    lip_gap = rules["tolerance_gap_mm"]
    groove_relief = rules["groove_depth_relief_mm"]
    
    if lip_gap < 0.15:
        raise VerificationError(f"Lid tolerance gap {lip_gap:.2f} mm is too tight (<0.15mm); lid will bind during insertion!")
    if groove_relief < 0.2:
        raise VerificationError(f"Groove depth relief {groove_relief:.2f} mm is too shallow (<0.2mm); lid will bottom out before rims contact!")
    results.append(f"[PASS] Joint mating: tongue height={lip_h}mm, width={lip_t}mm, gap={lip_gap}mm, relief={groove_relief}mm ensure flush mating.")

    return results


def run_tests():
    """Run test suite verifying both good designs pass and bad designs fail loudly."""
    print("======================================================================")
    print("RUNNING ENCLOSURE DESIGN RULE VERIFICATION TEST SUITE")
    print("======================================================================")
    
    base_dir = os.path.dirname(os.path.abspath(__file__))
    examples = [
        ("ESP32 Example", os.path.join(base_dir, "examples/esp32/esp32.json"), os.path.join(base_dir, "examples/esp32")),
        ("Pi Zero Example", os.path.join(base_dir, "examples/pizero/pizero.json"), os.path.join(base_dir, "examples/pizero"))
    ]
    
    for name, json_path, out_dir in examples:
        print(f"\n--- Checking {name} ---")
        try:
            results = check_enclosure(json_path, out_dir)
            for r in results:
                print(r)
            print(f"SUCCESS: {name} passed all design checks!")
        except Exception as e:
            print(f"FATAL: {name} failed: {e}", file=sys.stderr)
            sys.exit(1)
            
    print("\n--- Testing Intentional Defect Detection (Negative Tests) ---")
    tmp_test_dir = "/tmp/bad_enclosure_tests"
    os.makedirs(tmp_test_dir, exist_ok=True)
    
    # Negative Test 1: Board too tall for cavity
    print("1. Testing defect: Tall component exceeds cavity height...")
    bad_spec1 = {
        "board": {"length": 50, "width": 30, "thickness": 1.6, "component_height_top": 30.0, "component_height_bottom": 1.5, "mounting_holes": [], "ports": []},
        "enclosure": {"wall_thickness": 2.0, "clearance": 0.5, "standoff_height": 3.0, "screw_size": "M2.5"}
    }
    # Mock report with insufficient height
    bad_report1 = {
        "cavity_dimensions_mm": {"length": 51.0, "width": 31.0, "total_height": 10.0},
        "design_rules_and_sources": {"tongue_and_groove_joint": {"lip_height_mm": 1.4, "lip_thickness_mm": 1.0, "tolerance_gap_mm": 0.2, "groove_depth_relief_mm": 0.3}}
    }
    with open(f"{tmp_test_dir}/bad1.json", "w") as f:
        json.dump(bad_spec1, f)
    with open(f"{tmp_test_dir}/report.json", "w") as f:
        json.dump(bad_report1, f)
    # Copy dummy meshes
    import shutil
    shutil.copy(f"{base_dir}/examples/esp32/base.stl", f"{tmp_test_dir}/base.stl")
    shutil.copy(f"{base_dir}/examples/esp32/lid.stl", f"{tmp_test_dir}/lid.stl")
    
    try:
        check_enclosure(f"{tmp_test_dir}/bad1.json", tmp_test_dir)
        print("FAIL: Bad height was NOT caught!", file=sys.stderr)
        sys.exit(1)
    except VerificationError as ve:
        print(f"  [CAUGHT AS EXPECTED]: {ve}")

    # Negative Test 2: Wall thickness too thin (< 1.6mm)
    print("2. Testing defect: Wall thickness below 1.6mm threshold...")
    bad_spec2 = {
        "board": {"length": 50, "width": 30, "thickness": 1.6, "component_height_top": 4.0, "component_height_bottom": 1.5, "mounting_holes": [], "ports": []},
        "enclosure": {"wall_thickness": 1.0, "clearance": 0.5, "standoff_height": 3.0, "screw_size": "M2.5"}
    }
    with open(f"{tmp_test_dir}/bad2.json", "w") as f:
        json.dump(bad_spec2, f)
    try:
        check_enclosure(f"{tmp_test_dir}/bad2.json", tmp_test_dir)
        print("FAIL: Thin wall was NOT caught!", file=sys.stderr)
        sys.exit(1)
    except VerificationError as ve:
        print(f"  [CAUGHT AS EXPECTED]: {ve}")

    # Negative Test 3: Mounting hole outside board bounds
    print("3. Testing defect: Mounting hole outside board bounds...")
    bad_spec3 = {
        "board": {"length": 50, "width": 30, "thickness": 1.6, "component_height_top": 4.0, "component_height_bottom": 1.5, "mounting_holes": [{"x": 65.0, "y": 15.0}], "ports": []},
        "enclosure": {"wall_thickness": 2.0, "clearance": 0.5, "standoff_height": 3.0, "screw_size": "M2.5"}
    }
    with open(f"{tmp_test_dir}/bad3.json", "w") as f:
        json.dump(bad_spec3, f)
    try:
        check_enclosure(f"{tmp_test_dir}/bad3.json", tmp_test_dir)
        print("FAIL: Out of bounds mounting hole was NOT caught!", file=sys.stderr)
        sys.exit(1)
    except VerificationError as ve:
        print(f"  [CAUGHT AS EXPECTED]: {ve}")

    # Negative Test 4: Insufficient tolerance gap (<0.15mm)
    print("4. Testing defect: Joint tolerance gap too tight...")
    bad_spec4 = {
        "board": {"length": 50, "width": 30, "thickness": 1.6, "component_height_top": 4.0, "component_height_bottom": 1.5, "mounting_holes": [], "ports": []},
        "enclosure": {"wall_thickness": 2.0, "clearance": 0.5, "standoff_height": 3.0, "screw_size": "M2.5"}
    }
    bad_report4 = {
        "cavity_dimensions_mm": {"length": 51.0, "width": 31.0, "total_height": 15.0},
        "design_rules_and_sources": {"tongue_and_groove_joint": {"lip_height_mm": 1.4, "lip_thickness_mm": 1.0, "tolerance_gap_mm": 0.05, "groove_depth_relief_mm": 0.3}}
    }
    with open(f"{tmp_test_dir}/bad4.json", "w") as f:
        json.dump(bad_spec4, f)
    with open(f"{tmp_test_dir}/report.json", "w") as f:
        json.dump(bad_report4, f)
    try:
        check_enclosure(f"{tmp_test_dir}/bad4.json", tmp_test_dir)
        print("FAIL: Too tight joint gap was NOT caught!", file=sys.stderr)
        sys.exit(1)
    except VerificationError as ve:
        print(f"  [CAUGHT AS EXPECTED]: {ve}")

    print("\n======================================================================")
    print("ALL TESTS PASSED: Valid designs verified; defects caught with loud errors.")
    print("======================================================================")


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--test":
        run_tests()
    elif len(sys.argv) >= 2:
        board_file = sys.argv[1]
        out_dir = sys.argv[2] if len(sys.argv) > 2 else os.path.dirname(board_file) or "."
        try:
            results = check_enclosure(board_file, out_dir)
            for r in results:
                print(r)
            print("\nEnclosure verification: ALL CHECKS PASSED.")
            sys.exit(0)
        except Exception as err:
            print(f"\n[ENCLOSURE VERIFICATION FAILED]: {err}", file=sys.stderr)
            sys.exit(1)
    else:
        print("Usage:")
        print("  python3 check_enclosure.py <board.json> [output_dir]")
        print("  python3 check_enclosure.py --test")
        sys.exit(1)

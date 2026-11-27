// Package addons lists optional tool sets the owner can turn on in Settings.
// Add-ons are independent and combine: Unire builds one image FROM the normal
// sandbox image with every add-on that is turned on (Dockerfile and Context
// below), and adds each one's guide to the agent's prompt.
//
// An add-on is a folder here with an install.sh (run as root during the image
// build, with the folder copied to /opt/unire-addons/<id>) and whatever files
// it needs, plus an entry in All. To add one: make the folder, add the entry.
package addons

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed 3d-print cad
var files embed.FS

// Addon is one optional tool set.
type Addon struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Size    string `json:"size"` // roughly what it adds to the image
	// Env is set in the image after the add-on is installed (sorted by key).
	Env   map[string]string `json:"-"`
	Guide string            `json:"-"` // what the agent is told
}

// Print3D is 3D modelling, mesh checks and slicing for 3D printing.
var Print3D = Addon{
	ID:      "3d-print",
	Name:    "3D modelling and printing",
	Summary: "build123d, OpenSCAD, trimesh, CuraEngine and ADMesh, plus the unire-3d, unire-enclosure and unire-slice commands. For printable parts, enclosures, gadgets, drone frames and lithophanes.",
	Size:    "adds about 1.5 GB",
	Env: map[string]string{
		"PATH":                       "/opt/hardware/bin:${PATH}",
		"UNIRE_HARDWARE_HOME":        "/opt/unire-addons/3d-print",
		"UNIRE_HARDWARE_PROFILE_DIR": "/opt/unire-addons/3d-print/profiles",
	},
	Guide: "# 3D modelling add-on\n\nThe sandbox has 3D design tools. Work in millimetres, write models as code you can rerun, and look at every model you make.\n\n" +
		"- Python with build123d (B-Rep CAD, STEP/STL export), trimesh, numpy and pillow; OpenSCAD for CSG. Write the model as a script, export STL (and STEP for CAD).\n" +
		"- unire-3d info <model> [--bed 220x220x250] [--material pla|petg|tpu|abs|cf-nylon|cf] - size, watertight, bed fit, thin parts, overhang share, weight. Run it on every part.\n" +
		"- unire-3d views <model> <out.png> - front/side/top/iso sheet. Open the PNG and look at it, then unire share it with the owner.\n" +
		"- unire-3d fix|scale|convert - repair a mesh, scale it (--max-dim, --factor, --x/--y/--z), turn STEP/OBJ/3MF into STL.\n" +
		"- unire-3d relief <image> <out.stl> [--mode lithophane] - a picture as a relief or lithophane (a heightmap, not a full 3D model).\n" +
		"- unire-3d assemble <assembly.json> <out.png> and unire-3d dxf <model> <out.dxf> - multi-part fit and collision check; 2D cut profile for laser or CNC.\n" +
		"- unire-enclosure <board.json> <outdir> - two-part electronics enclosure; unire-mesh-check <stl> - mesh validity.\n" +
		"- unire-slice <model.stl> <out.gcode> [--material pla|petg|tpu] [--infill N] - CuraEngine slice with a generic printer: use it for time and filament estimates only.\n\n" +
		"Limits to tell the owner: organic or sculpted shapes from a photo are not possible here; a photo plus real measurements works for simple geometric parts. The G-code is not for their printer: they should slice the STL in their own slicer. Nothing is checked physically, so say a first print is a test fit. Never send anything to a printer.\n",
}

// CAD is electronics (KiCad) and CAD/CAM (FreeCAD). It does not need Print3D;
// with both on, the image has both.
var CAD = Addon{
	ID:      "cad",
	Name:    "Electronics and CAD/CAM",
	Summary: "KiCad (kicad-cli: DRC, Gerbers, STEP) and FreeCAD (headless CAD and CAM). For custom PCBs and milled parts. Turn on 3D modelling too for printable parts.",
	Size:    "adds about 3 GB",
	Guide: "# Electronics and CAD/CAM add-on\n\nThe sandbox has PCB and CAD/CAM tools.\n\n" +
		"- kicad-cli pcb drc|export gerbers|export step and kicad-cli sch erc - check and export KiCad boards. unire-kicad-check <board.kicad_pcb> runs DRC.\n" +
		"- freecadcmd -c \"<python>\" or freecadcmd script.py - headless FreeCAD (Python API) for STEP work and Path CAM toolpaths. Do not run freecadcmd --version: it crashes; use FreeCAD.Version() in Python.\n\n" +
		"Limits: no circuit simulation, no flight or physics simulation, no FEA. Gerbers and toolpaths need a check by the owner (fab house preview, a test cut) before they are used.\n",
}

// All is every add-on, in the order Settings shows them and the image installs
// them (a stable order keeps Docker's layer cache when one more is turned on).
var All = []Addon{Print3D, CAD}

// Lookup finds an add-on by id.
func Lookup(id string) (Addon, bool) {
	for _, a := range All {
		if a.ID == id {
			return a, true
		}
	}
	return Addon{}, false
}

// Clean keeps the known ids, once each, in the order of All.
func Clean(ids []string) []string {
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	var out []string
	for _, a := range All {
		if want[a.ID] {
			out = append(out, a.ID)
		}
	}
	return out
}

// Guide is the prompt text for the add-ons that are on: each one's guide.
func Guide(ids []string) string {
	var guides []string
	for _, id := range Clean(ids) {
		a, _ := Lookup(id)
		guides = append(guides, a.Guide)
	}
	return strings.Join(guides, "\n")
}

// Dockerfile is the image that adds ids to the sandbox image given as the
// BASE build argument. It uses only what the classic builder supports, since
// the docker CLI on the host may not have BuildKit.
func Dockerfile(ids []string) string {
	var b strings.Builder
	b.WriteString("# Generated by Unire (package addons).\nARG BASE\nFROM ${BASE}\nUSER root\n")
	for _, id := range Clean(ids) {
		a, _ := Lookup(id)
		fmt.Fprintf(&b, "\n# add-on %s\nCOPY %s /opt/unire-addons/%s\nRUN sh /opt/unire-addons/%s/install.sh\n", a.ID, a.ID, a.ID, a.ID)
		keys := make([]string, 0, len(a.Env))
		for k := range a.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "ENV %s=%q\n", k, a.Env[k])
		}
	}
	fmt.Fprintf(&b, "\nUSER node\nLABEL unire.addons=%q\n", strings.Join(Clean(ids), ","))
	return b.String()
}

// Context is the build context for ids, as a tar stream for `docker build -`:
// the Dockerfile and each add-on's folder. Hash identifies its content, so an
// image built from it can be tagged and found again.
func Context(ids []string) (tarball []byte, hash string, err error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	sum := sha256.New()
	add := func(name string, mode int64, data []byte) error {
		fmt.Fprintf(sum, "%s %o %d\n", name, mode, len(data))
		sum.Write(data)
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := add("Dockerfile", 0o644, []byte(Dockerfile(ids))); err != nil {
		return nil, "", err
	}
	for _, id := range Clean(ids) {
		err := fs.WalkDir(files, id, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := files.ReadFile(p)
			if err != nil {
				return err
			}
			mode := int64(0o644)
			if path.Base(path.Dir(p)) == "bin" || strings.HasSuffix(p, ".sh") {
				mode = 0o755
			}
			return add(p, mode, data)
		})
		if err != nil {
			return nil, "", err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), hex.EncodeToString(sum.Sum(nil)), nil
}

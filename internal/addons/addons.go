// Package addons lists optional tool sets the owner can turn on in Settings.
// An add-on brings two things to every new run: a sandbox image that carries
// its tools (built FROM the normal sandbox image, so nothing the agent already
// has is lost), and a guide added to the agent's prompt so the agent knows the
// tools are there and how to use them.
package addons

import "strings"

// Addon is one optional tool set.
type Addon struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Size    string `json:"size"`  // download size, for the owner to judge
	Image   string `json:"image"` // the published image with the tools
	// Covers lists add-ons whose tools this one's image also carries. Turning on
	// two add-ons uses the image that covers the other.
	Covers []string `json:"covers,omitempty"`
	Guide  string   `json:"-"` // what the agent is told
}

// Print3D is 3D modelling, mesh checks and slicing for 3D printing
// (experiments/hardware-pipeline/Dockerfile.hardware, target variant-b).
var Print3D = Addon{
	ID:      "3d-print",
	Name:    "3D modelling and printing",
	Summary: "build123d, OpenSCAD, trimesh, CuraEngine and ADMesh, plus the unire-3d, unire-enclosure and unire-slice commands. For printable parts, enclosures, gadgets, drone frames and lithophanes.",
	Size:    "about 6 GB image",
	Image:   "ghcr.io/mistergregg/unire-sandbox-3d:latest",
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

// Hardware adds PCB and CAD/CAM tools on top of Print3D (target variant-c).
var Hardware = Addon{
	ID:      "hardware",
	Name:    "Electronics and CAD/CAM",
	Summary: "Everything in 3D modelling and printing, plus KiCad (kicad-cli: DRC, Gerbers, STEP) and FreeCAD (headless CAD and CAM). For custom PCBs and milled parts.",
	Size:    "about 9 GB image",
	Image:   "ghcr.io/mistergregg/unire-sandbox-hardware:latest",
	Covers:  []string{Print3D.ID},
	Guide: "# Electronics and CAD/CAM add-on\n\nThe sandbox also has PCB and CAD/CAM tools.\n\n" +
		"- kicad-cli pcb drc|export gerbers|export step and kicad-cli sch erc - check and export KiCad boards. unire-kicad-check <board.kicad_pcb> runs DRC.\n" +
		"- freecadcmd - headless FreeCAD (Python API) for STEP work and Path CAM toolpaths.\n\n" +
		"Limits: no circuit simulation, no flight or physics simulation, no FEA. Gerbers and toolpaths need a check by the owner (fab house preview, a test cut) before they are used.\n",
}

// All is every add-on, in the order Settings shows them.
var All = []Addon{Print3D, Hardware}

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

// Pick returns the add-ons that apply when ids are turned on: the image to run
// (empty: keep the normal sandbox image) and the guides for every add-on that
// image carries. images overrides an add-on's published image (for an owner
// who builds their own); a missing or empty entry uses the published one.
func Pick(ids []string, images map[string]string) (image, guide string) {
	on := Clean(ids)
	if len(on) == 0 {
		return "", ""
	}
	imageOf := func(a Addon) string {
		if v := strings.TrimSpace(images[a.ID]); v != "" {
			return v
		}
		return a.Image
	}
	// The chosen add-on is the one whose image covers the most of the others.
	best, bestCover := Addon{}, -1
	for _, id := range on {
		a, _ := Lookup(id)
		n := 0
		for _, other := range on {
			if other == a.ID || contains(a.Covers, other) {
				n++
			}
		}
		if n > bestCover {
			best, bestCover = a, n
		}
	}
	image = imageOf(best)
	// Guides: only what the chosen image carries, so the agent is never told
	// about tools it does not have.
	var guides []string
	for _, a := range All {
		if a.ID == best.ID || contains(best.Covers, a.ID) {
			guides = append(guides, a.Guide)
		}
	}
	return image, strings.Join(guides, "\n")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

package addons

import (
	"strings"
	"testing"
)

func TestNothingOnKeepsTheSandboxImageAndAddsNoGuide(t *testing.T) {
	if img, g := Pick(nil, nil); img != "" || g != "" {
		t.Errorf("Pick(nil) = %q, %q", img, g)
	}
	if img, _ := Pick([]string{"nope", ""}, nil); img != "" {
		t.Errorf("unknown ids are ignored, got image %q", img)
	}
}

func TestOneAddonUsesItsImageAndGuide(t *testing.T) {
	img, g := Pick([]string{Print3D.ID}, nil)
	if img != Print3D.Image || !strings.Contains(g, "unire-3d info") || strings.Contains(g, "kicad-cli") {
		t.Errorf("3d-print: %q\n%s", img, g)
	}
}

func TestTheImageThatCoversTheOthersWins(t *testing.T) {
	for _, on := range [][]string{{Hardware.ID}, {Print3D.ID, Hardware.ID}, {Hardware.ID, Print3D.ID}} {
		img, g := Pick(on, nil)
		if img != Hardware.Image {
			t.Errorf("%v: image %q, want %q", on, img, Hardware.Image)
		}
		// The hardware image carries the 3D tools too, so the agent is told about both.
		if !strings.Contains(g, "unire-3d info") || !strings.Contains(g, "kicad-cli") {
			t.Errorf("%v: guide misses a tool set:\n%s", on, g)
		}
	}
}

func TestAnOwnersImageReplacesThePublishedOne(t *testing.T) {
	if img, _ := Pick([]string{Print3D.ID}, map[string]string{Print3D.ID: " my-3d:dev "}); img != "my-3d:dev" {
		t.Errorf("image = %q", img)
	}
	if img, _ := Pick([]string{Print3D.ID}, map[string]string{Print3D.ID: ""}); img != Print3D.Image {
		t.Errorf("an empty override uses the published image, got %q", img)
	}
}

func TestCleanKeepsKnownIdsOnceInOrder(t *testing.T) {
	got := strings.Join(Clean([]string{"hardware", " 3d-print", "x", "hardware"}), ",")
	if got != "3d-print,hardware" {
		t.Errorf("Clean = %q", got)
	}
}

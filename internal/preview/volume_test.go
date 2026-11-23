package preview

import (
	"strings"
	"testing"
)

func TestFoundFromListing(t *testing.T) {
	out := strings.Join([]string{
		"./notes.md", // not a repo file
		"./recipes/pubspec.yaml",
		"./recipes/web/index.html",
		"./site/index.html",
		"./site/unire.preview.json", // content comes from the block below
		"./.hidden/index.html",
		"./api/main.go",
		marker + "./site/unire.preview.json",
		`{"previews":[{"name":"main","recipe":"static","dir":"."}]}`,
		endTag,
		"",
	}, "\n")
	got := foundFromListing(out)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Repo != "recipes" || got[0].Previews[0].Recipe.Name != "flutter" || got[0].Previews[0].Dir != "." {
		t.Fatalf("recipes: %+v", got[0])
	}
	if got[1].Repo != "site" || got[1].Previews[0].Name != "main" {
		t.Fatalf("site must use its config: %+v", got[1])
	}
}

func TestFoundFromListingReportsABrokenConfig(t *testing.T) {
	out := strings.Join([]string{"./site/unire.preview.json", marker + "./site/unire.preview.json", `{"previews":[{"recipe":"static","image":"evil"}]}`, endTag}, "\n")
	got := foundFromListing(out)
	if len(got) != 1 || got[0].Repo != "site" || !strings.Contains(got[0].Err, "cannot choose a container image") {
		t.Fatalf("got %+v", got)
	}
}

func TestFoundFromListingEmpty(t *testing.T) {
	if got := foundFromListing(""); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	if got := foundFromListing("./a.txt\n./b/c.go\n"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestListArgsAreLockedDown(t *testing.T) {
	joined := strings.Join(listArgs("img", "unire-ws-x"), " ")
	for _, want := range []string{"--network none", "--cap-drop ALL", "no-new-privileges", "-v unire-ws-x:/ws:ro", "--rm"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(joined, "docker.sock") {
		t.Error("the listing container must not get the docker socket")
	}
}

func TestDetectInVolumeRefusesUnsafeNames(t *testing.T) {
	for _, vol := range []string{"", "a b", "x;rm", "-v"} {
		if _, err := DetectInVolume(t.Context(), "img", vol); err == nil {
			t.Errorf("volume %q must be refused", vol)
		}
	}
	if _, err := DetectInVolume(t.Context(), "--privileged", "unire-ws-x"); err == nil {
		t.Error("an image that looks like a flag must be refused")
	}
}

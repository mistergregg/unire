package addons

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestCleanKeepsKnownIdsOnceInOrder(t *testing.T) {
	if got := strings.Join(Clean([]string{"cad", " 3d-print", "x", "cad"}), ","); got != "3d-print,cad" {
		t.Errorf("Clean = %q", got)
	}
}

func TestGuideHasEveryAddonThatIsOn(t *testing.T) {
	if g := Guide(nil); g != "" {
		t.Errorf("no add-ons, guide %q", g)
	}
	if g := Guide([]string{"3d-print"}); !strings.Contains(g, "unire-3d info") || strings.Contains(g, "kicad-cli") {
		t.Errorf("3d only:\n%s", g)
	}
	if g := Guide([]string{"cad"}); strings.Contains(g, "unire-3d") || !strings.Contains(g, "kicad-cli") {
		t.Errorf("cad only:\n%s", g)
	}
	if g := Guide([]string{"cad", "3d-print"}); !strings.Contains(g, "unire-3d info") || !strings.Contains(g, "kicad-cli") {
		t.Errorf("both:\n%s", g)
	}
}

func TestDockerfileInstallsEachAddonOnTheBase(t *testing.T) {
	d := Dockerfile([]string{"cad", "3d-print"})
	i3, ic := strings.Index(d, "RUN sh /opt/unire-addons/3d-print/install.sh"), strings.Index(d, "RUN sh /opt/unire-addons/cad/install.sh")
	if !strings.Contains(d, "FROM ${BASE}") || i3 < 0 || ic < i3 || !strings.HasSuffix(strings.TrimSpace(d), `LABEL unire.addons="3d-print,cad"`) {
		t.Errorf("Dockerfile:\n%s", d)
	}
	if !strings.Contains(d, `ENV UNIRE_HARDWARE_HOME="/opt/unire-addons/3d-print"`) || !strings.Contains(d, "USER node") {
		t.Errorf("env or user missing:\n%s", d)
	}
	if strings.Contains(Dockerfile([]string{"cad"}), "3d-print") {
		t.Errorf("cad alone must not install 3d-print")
	}
}

func TestContextCarriesEachAddonsFilesAndHashesThem(t *testing.T) {
	tarball, hash, err := Context([]string{"3d-print", "cad"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int64{}
	tr := tar.NewReader(bytes.NewReader(tarball))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = h.Mode
	}
	for _, want := range []string{"Dockerfile", "3d-print/install.sh", "3d-print/mesh_tools.py", "3d-print/profiles/NOTICE.md", "cad/install.sh"} {
		if _, ok := names[want]; !ok {
			t.Errorf("context lacks %s", want)
		}
	}
	if names["3d-print/bin/unire-3d"] != 0o755 || names["cad/bin/unire-kicad-check"] != 0o755 {
		t.Errorf("commands must be executable: %o %o", names["3d-print/bin/unire-3d"], names["cad/bin/unire-kicad-check"])
	}
	_, h1, _ := Context([]string{"3d-print"})
	_, h2, _ := Context([]string{"3d-print"})
	if h1 != h2 || h1 == hash {
		t.Errorf("hash must be stable per set and differ between sets: %s %s %s", h1, h2, hash)
	}
}

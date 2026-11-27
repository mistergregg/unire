package server

import (
	"strings"
	"testing"

	"github.com/mistergregg/unire/internal/addons"
)

func TestAddonsChangeTheSessionImageAndPrompt(t *testing.T) {
	e := newEnv(t)
	e.setup()
	base := e.srv.sandbox().Image
	if img, g := e.srv.sessionImage(e.srv.sandbox()); img != base || g != "" || e.srv.sessionConfig().Guide != "" {
		t.Fatalf("no add-ons: image %q guide %q", img, g)
	}
	code, body := e.do("GET", "/api/addons", nil)
	if code != 200 || !strings.Contains(body, `"id":"3d-print"`) || strings.Contains(body, `"enabled":true`) {
		t.Fatalf("list = %d %s", code, body)
	}

	code, body = e.do("PUT", "/api/addons", map[string]any{"enabled": []string{"3d-print"}})
	if code != 200 || !strings.Contains(body, `"image":"`+addons.Print3D.Image+`"`) {
		t.Fatalf("enable = %d %s", code, body)
	}
	if img, _ := e.srv.sessionImage(e.srv.sandbox()); img != addons.Print3D.Image {
		t.Errorf("session image = %q", img)
	}
	if g := e.srv.sessionConfig().Guide; !strings.Contains(g, "unire-3d") {
		t.Errorf("guide = %q", g)
	}
	if e.srv.sandbox().Image != base {
		t.Errorf("the sandbox setting itself must not change (sign-in still uses it)")
	}

	// An owner's own build, kept across saves; then everything off again.
	if code, _ := e.do("PUT", "/api/addons", map[string]any{"enabled": []string{"3d-print"}, "images": map[string]string{"3d-print": "local/unire-3d:dev"}}); code != 200 {
		t.Fatal(code)
	}
	if img, _ := e.srv.sessionImage(e.srv.sandbox()); img != "local/unire-3d:dev" {
		t.Errorf("custom image = %q", img)
	}
	e.do("PUT", "/api/addons", map[string]any{"enabled": []string{}})
	if img, g := e.srv.sessionImage(e.srv.sandbox()); img != base || g != "" {
		t.Errorf("turned off: %q %q", img, g)
	}

	for _, bad := range []map[string]any{
		{"enabled": []string{"rootkit"}},
		{"enabled": []string{}, "images": map[string]string{"3d-print": "bad image; rm -rf"}},
		{"enabled": []string{}, "images": map[string]string{"nope": "x"}},
	} {
		if code, _ := e.do("PUT", "/api/addons", bad); code != 400 {
			t.Errorf("%v = %d, want 400", bad, code)
		}
	}
}

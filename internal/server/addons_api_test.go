package server

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDocker stands in for the docker CLI calls of addons_build.go.
type fakeDocker struct {
	mu      sync.Mutex
	baseID  string
	images  map[string]bool
	builds  []string // the Dockerfile of each build
	fail    bool
	release chan struct{} // when set, builds wait for it
}

func useFakeDocker(t *testing.T) *fakeDocker {
	f := &fakeDocker{baseID: "sha256:base1", images: map[string]bool{}}
	oldID, oldHas, oldBuild, oldUntag := dockerImageID, dockerHasImage, dockerBuild, dockerUntagOld
	t.Cleanup(func() { dockerImageID, dockerHasImage, dockerBuild, dockerUntagOld = oldID, oldHas, oldBuild, oldUntag })
	dockerImageID = func(context.Context, string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.baseID, nil
	}
	dockerHasImage = func(_ context.Context, image string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.images[image]
	}
	dockerBuild = func(ctx context.Context, tags []string, base string, tarball []byte, log io.Writer) error {
		if f.release != nil {
			<-f.release
		}
		tr := tar.NewReader(bytes.NewReader(tarball))
		var dockerfile bytes.Buffer
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			if h.Name == "Dockerfile" {
				_, _ = io.Copy(&dockerfile, tr)
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.builds = append(f.builds, dockerfile.String())
		if f.fail {
			_, _ = io.WriteString(log, "E: Unable to locate package kicad\n")
			return errors.New("exit status 100")
		}
		for _, t := range tags {
			f.images[t] = true
		}
		return nil
	}
	dockerUntagOld = func(context.Context, string, string) {}
	return f
}

func (f *fakeDocker) buildCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.builds)
}

func waitBuild(t *testing.T, e *env, want string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		_, body := e.do("GET", "/api/addons", nil)
		if strings.Contains(body, `"state":"`+want+`"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, body := e.do("GET", "/api/addons", nil)
	t.Fatalf("build never reached %s: %s", want, body)
}

func TestAddonsAreBuiltIntoOneImageAndToldToTheAgent(t *testing.T) {
	f := useFakeDocker(t)
	e := newEnv(t)
	e.setup()
	base := e.srv.sandbox().Image
	if cfg := e.srv.sessionConfig(); cfg.Guide != "" {
		t.Fatalf("no add-ons, guide %q", cfg.Guide)
	} else if _, ok := cfg.Runner.(addonRunner); ok {
		t.Fatal("no add-ons must use the plain runner")
	}

	// Both on: one image with both, built in the background when saved.
	code, body := e.do("PUT", "/api/addons", map[string]any{"enabled": []string{"cad", "3d-print"}})
	if code != 200 || strings.Count(body, `"enabled":true`) != 2 {
		t.Fatalf("enable = %d %s", code, body)
	}
	waitBuild(t, e, "ready")
	if f.buildCount() != 1 || !strings.Contains(f.builds[0], "3d-print/install.sh") || !strings.Contains(f.builds[0], "cad/install.sh") {
		t.Fatalf("builds: %q", f.builds)
	}
	cfg := e.srv.sessionConfig()
	if _, ok := cfg.Runner.(addonRunner); !ok || !strings.Contains(cfg.Guide, "unire-3d") || !strings.Contains(cfg.Guide, "kicad-cli") {
		t.Fatalf("runner %T, guide %q", cfg.Runner, cfg.Guide)
	}
	ctx := context.Background()
	img, err := e.srv.addonBuilds.image(ctx, ctx, &e.srv.bgWG, base, []string{"3d-print", "cad"}, true, nil)
	if err != nil || !strings.HasPrefix(img, addonRepo+":") {
		t.Fatalf("image = %q, %v", img, err)
	}
	if e.srv.sandbox().Image != base {
		t.Errorf("the sandbox setting must not change (sign-in still uses it)")
	}

	// The base image is updated: the previous build is used at once, a new one is made.
	f.mu.Lock()
	f.baseID = "sha256:base2"
	f.release = make(chan struct{})
	f.mu.Unlock()
	img2, err := e.srv.addonBuilds.image(ctx, ctx, &e.srv.bgWG, base, []string{"3d-print", "cad"}, true, nil)
	if err != nil || !strings.HasPrefix(img, img2+"-") { // img is <set>-<build>, img2 must be <set>
		t.Errorf("while rebuilding, the set tag is used: %q (was %q), %v", img2, img, err)
	}
	close(f.release)
	f.mu.Lock()
	f.release = nil
	f.mu.Unlock()
	waitBuild(t, e, "ready")

	// Off again: plain runner, no guide.
	e.do("PUT", "/api/addons", map[string]any{"enabled": []string{}})
	if cfg := e.srv.sessionConfig(); cfg.Guide != "" {
		t.Errorf("turned off, guide %q", cfg.Guide)
	}
	if code, _ := e.do("PUT", "/api/addons", map[string]any{"enabled": []string{"rootkit"}}); code != 400 {
		t.Errorf("unknown add-on = %d", code)
	}
}

func TestAFailedAddonBuildIsReportedAndStopsTheChat(t *testing.T) {
	f := useFakeDocker(t)
	f.fail = true
	e := newEnv(t)
	e.setup()
	var log bytes.Buffer
	ctx := context.Background()
	_, err := e.srv.addonBuilds.image(ctx, ctx, &e.srv.bgWG, e.srv.sandbox().Image, []string{"cad"}, true, &log)
	if err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(log.String(), "several minutes") {
		t.Fatalf("err = %v, log %q", err, log.String())
	}
	e.do("PUT", "/api/addons", map[string]any{"enabled": []string{"cad"}})
	waitBuild(t, e, "failed")
	if _, body := e.do("GET", "/api/addons", nil); !strings.Contains(body, "Unable to locate package") {
		t.Errorf("the build log is shown: %s", body)
	}
	if strings.Contains(f.builds[0], "3d-print") {
		t.Errorf("cad alone must not install 3d-print:\n%s", f.builds[0])
	}
}

func TestAddonsWithoutTheSandboxImageSayWhy(t *testing.T) {
	useFakeDocker(t)
	dockerImageID = func(context.Context, string) (string, error) { return "", errors.New("no such image") }
	e := newEnv(t)
	e.setup()
	e.do("PUT", "/api/addons", map[string]any{"enabled": []string{"3d-print"}})
	waitBuild(t, e, "failed")
	if _, body := e.do("GET", "/api/addons", nil); !strings.Contains(body, "is not on this host") {
		t.Errorf("status = %s", body)
	}
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeProbe struct {
	dockerErr  error
	imageFound bool
	imageAge   time.Duration
	volume     bool
	containers int
}

func (f fakeProbe) DockerVersion(context.Context) (string, error) { return "27.1.0", f.dockerErr }
func (f fakeProbe) ImageCreated(context.Context, string) (time.Time, bool, error) {
	return time.Now().Add(-f.imageAge), f.imageFound, nil
}
func (f fakeProbe) VolumeExists(context.Context, string) (bool, error) { return f.volume, nil }
func (f fakeProbe) SandboxContainers(context.Context) (int, error)     { return f.containers, nil }

type healthReply struct {
	Status string `json:"status"`
	Checks []struct {
		ID, Name, Status, Summary, Fix string
	} `json:"checks"`
}

func health(t *testing.T, e *env) (healthReply, map[string]string) {
	t.Helper()
	code, body := e.do("GET", "/api/health", nil)
	if code != 200 {
		t.Fatalf("health = %d %s", code, body)
	}
	var r healthReply
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, c := range r.Checks {
		status[c.ID] = c.Status
	}
	return r, status
}

func TestHealthSaysWhatIsFineAndWhatIsNot(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.srv.probe = fakeProbe{imageFound: true, imageAge: time.Hour, volume: true}
	r, st := health(t, e)
	for _, id := range []string{"unire", "docker", "image", "volume", "containers", "chats", "egress", "database", "limits", "cleanup"} {
		if st[id] != "ok" {
			t.Errorf("%s = %q, want ok (%+v)", id, st[id], r.Checks)
		}
	}
	if r.Checks[0].ID != "unire" {
		t.Errorf("checks keep a fixed order, unire first: %v", r.Checks[0])
	}

	// Docker is down: that is broken, and it says what to do.
	e.srv.probe = fakeProbe{dockerErr: errors.New("Cannot connect to the Docker daemon"), imageFound: true, volume: true}
	r, st = health(t, e)
	if st["docker"] != "bad" || r.Status != "bad" {
		t.Errorf("docker down: %v overall %q", st, r.Status)
	}
	for _, c := range r.Checks {
		if c.ID == "docker" && (!strings.Contains(c.Summary, "Cannot connect") || !strings.Contains(c.Fix, "docker.sock")) {
			t.Errorf("the docker check should say why and what to do: %+v", c)
		}
	}

	// An image that is missing or old, a login volume not made yet, leftover containers.
	e.srv.probe = fakeProbe{imageFound: false, volume: false, containers: 3}
	_, st = health(t, e)
	if st["image"] != "warn" || st["volume"] != "warn" || st["containers"] != "warn" || st["docker"] != "ok" {
		t.Errorf("warnings = %v", st)
	}
	e.srv.probe = fakeProbe{imageFound: true, imageAge: 90 * 24 * time.Hour, volume: true}
	if _, st = health(t, e); st["image"] != "warn" {
		t.Errorf("a three-month-old image: %v", st)
	}
	// No time limit is worth a look.
	e.do("PUT", "/api/limits", map[string]any{"maxRunMinutes": 0, "keepLogsDays": 90, "keepWorkspaceDays": 14})
	if _, st = health(t, e); st["limits"] != "warn" {
		t.Errorf("no run limit: %v", st)
	}
	if code, _ := e.do("GET", "/api/health", nil, true); code == http.StatusInternalServerError {
		t.Error("health must not fail without the CSRF header on a GET")
	}
}

func TestHealthNeedsALogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.do("POST", "/api/logout", nil)
	if code, _ := e.do("GET", "/api/health", nil); code != http.StatusUnauthorized {
		t.Errorf("health without login = %d, want 401", code)
	}
}

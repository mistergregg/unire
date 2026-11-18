package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mistergregg/unire/internal/gitprovider"
	"github.com/mistergregg/unire/internal/store"
)

func TestAPushedBranchIsOfferedAndOpenedAfterTheOwnerSaysYes(t *testing.T) {
	e := newEnv(t)
	e.setup()

	var opened, closedWith string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		pr := `{"number":9,"title":"Fix it","html_url":"http://git/pr/9","state":"open","head":{"ref":"agent/b","sha":"abc"},"base":{"ref":"main"}}`
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/repos/greg/api":
			w.Write([]byte(`{"default_branch":"main"}`))
		case "POST /api/v1/repos/greg/api/pulls":
			opened = string(b)
			w.Write([]byte(pr))
		case "PATCH /api/v1/repos/greg/api/pulls/9":
			closedWith = string(b)
			w.Write([]byte(`{}`))
		case "GET /api/v1/repos/greg/api/pulls/9":
			w.Write([]byte(pr))
		case "GET /api/v1/repos/greg/api/commits/abc/status":
			w.Write([]byte(`{"statuses":[{"context":"build","status":"success","target_url":""}]}`))
		case "GET /api/v1/repos/greg/api/issues/9/comments", "GET /api/v1/repos/greg/api/pulls/9/reviews":
			w.Write([]byte(`[]`))
		default:
			http.Error(w, "no route", 404)
		}
	}))
	defer api.Close()

	st := e.srv.store
	g, err := st.AddGitServer(store.GitServer{Name: "home", Type: "gitea", BaseURL: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.pulls.Provider = func(g store.GitServer) (gitprovider.Provider, error) {
		return gitprovider.New(gitprovider.Config{Type: g.Type, BaseURL: g.BaseURL, Token: "t"})
	}
	ref := store.RepoRef{ServerID: g.ID, FullName: "greg/api"}
	if err := st.CreateSession(store.Session{ID: "c1", Task: "Fix the bug", Agent: "claude", Mode: "hands-off", Prompt: "p", Branch: "agent/b", Repos: []store.RepoRef{ref}}); err != nil {
		t.Fatal(err)
	}
	st.AddEvent("c1", "log", "api: pushed agent/b")

	code, body := e.do("GET", "/api/sessions/c1/pulls", nil)
	if code != 200 {
		t.Fatalf("GET = %d %s", code, body)
	}
	var got struct {
		Proposals []map[string]any `json:"proposals"`
		Pulls     []map[string]any `json:"pulls"`
	}
	json.Unmarshal([]byte(body), &got)
	if len(got.Proposals) != 1 || len(got.Pulls) != 0 || got.Proposals[0]["title"] != "Fix the bug" {
		t.Fatalf("before saying yes: %s", body)
	}
	if opened != "" {
		t.Fatal("nothing may be opened before the owner agrees")
	}

	code, body = e.do("POST", "/api/sessions/c1/pulls", map[string]any{"serverId": g.ID, "repo": "greg/api", "branch": "agent/b", "title": "Fix the bug"})
	if code != 200 || !strings.Contains(body, `"number":9`) || !strings.Contains(body, `"summary":"success"`) {
		t.Fatalf("POST = %d %s", code, body)
	}
	if !strings.Contains(opened, "Fix the bug") {
		t.Errorf("the pull request was not opened with the title: %q", opened)
	}

	code, body = e.do("GET", "/api/sessions/c1/pulls", nil)
	json.Unmarshal([]byte(body), &got)
	if code != 200 || len(got.Proposals) != 0 || len(got.Pulls) != 1 {
		t.Fatalf("after: %d %s", code, body)
	}
	if code, _ := e.do("POST", "/api/sessions/c1/pulls", map[string]any{"serverId": g.ID, "repo": "greg/api", "branch": "agent/b"}); code != 400 {
		t.Errorf("opening it again = %d, want 400", code)
	}
	if code, _ := e.do("POST", "/api/sessions/c1/pulls", map[string]any{"serverId": g.ID, "repo": "greg/api", "branch": "main"}); code != 400 {
		t.Errorf("a branch that was not pushed = %d, want 400", code)
	}
	code, body = e.do("POST", "/api/sessions/c1/pulls/state", map[string]any{"serverId": g.ID, "repo": "greg/api", "number": 9, "open": false})
	if code != 200 || !strings.Contains(closedWith, `"state":"closed"`) {
		t.Errorf("close = %d %s (sent %q)", code, body, closedWith)
	}
	if code, _ := e.do("POST", "/api/sessions/c1/pulls/state", map[string]any{"serverId": g.ID, "repo": "greg/api", "number": 77, "open": false}); code != 400 {
		t.Errorf("closing a pull request the chat did not open = %d, want 400", code)
	}
	if code, _ := e.do("GET", "/api/sessions/nope/pulls", nil); code != 404 {
		t.Errorf("unknown chat = %d, want 404", code)
	}
}

func TestTheTokenCanBeCheckedForPullRequestAccess(t *testing.T) {
	e := newEnv(t)
	e.setup()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/user":
			w.Write([]byte(`{"login":"greg"}`))
		case "/api/v1/repos/search":
			w.Write([]byte(`{"data":[{"full_name":"greg/api","clone_url":"https://g/greg/api.git","default_branch":"main"}]}`))
		case "/api/v1/repos/greg/api":
			w.Write([]byte(`{"permissions":{"push":true}}`))
		case "/api/v1/repos/greg/api/issues":
			http.Error(w, `{"message":"token does not have issue scope"}`, http.StatusForbidden) // too few permissions
		case "/api/v1/repos/greg/api/commits":
			w.Write([]byte(`[{"sha":"abc"}]`))
		default:
			w.Write([]byte(`[]`))
		}
	}))
	defer api.Close()
	if code, body := e.do("POST", "/api/servers", map[string]any{"name": "home", "type": "gitea", "baseUrl": api.URL, "token": "t"}); code != http.StatusCreated {
		t.Fatalf("add = %d %s", code, body)
	}
	code, body := e.do("POST", "/api/servers/home/test-pulls", nil)
	if code != 200 {
		t.Fatalf("test = %d %s", code, body)
	}
	var got struct {
		Repo         string                   `json:"repo"`
		Capabilities []gitprovider.Capability `json:"capabilities"`
	}
	json.Unmarshal([]byte(body), &got)
	if got.Repo != "greg/api" || len(got.Capabilities) != 4 {
		t.Fatalf("result = %s", body)
	}
	for _, c := range got.Capabilities {
		wantOK := c.Name != gitprovider.CapComments // the one the fake refuses
		if c.OK != wantOK {
			t.Errorf("%s: ok = %v, want %v (%s)", c.Name, c.OK, wantOK, c.Note)
		}
	}
}

func TestLimitsHaveSaneDefaultsAndAreValidated(t *testing.T) {
	e := newEnv(t)
	e.setup()
	code, body := e.do("GET", "/api/limits", nil)
	if code != 200 || !strings.Contains(body, `"maxRunMinutes":240`) || !strings.Contains(body, `"keepLogsDays":90`) || !strings.Contains(body, `"keepWorkspaceDays":14`) {
		t.Fatalf("defaults = %d %s", code, body)
	}
	if code, body := e.do("PUT", "/api/limits", map[string]any{"maxRunMinutes": 30, "keepLogsDays": 0, "keepWorkspaceDays": 7}); code != 200 || !strings.Contains(body, `"maxRunMinutes":30`) || !strings.Contains(body, `"keepLogsDays":0`) {
		t.Errorf("save = %d %s", code, body)
	}
	if code, _ := e.do("PUT", "/api/limits", map[string]any{"maxRunMinutes": -5}); code != http.StatusBadRequest {
		t.Errorf("a negative limit = %d, want 400", code)
	}
	if code, _ := e.do("PUT", "/api/limits", map[string]any{"keepLogsDays": 99999}); code != http.StatusBadRequest {
		t.Errorf("too many days = %d, want 400", code)
	}
}

func TestMemoryStatsEndpoint(t *testing.T) {
	e := newEnv(t)
	e.setup()
	code, body := e.do("GET", "/api/memory/stats", nil)
	if code != 200 || !strings.Contains(body, `"days":90`) || strings.Count(body, `"memory":`) != 2 {
		t.Errorf("stats = %d %s", code, body)
	}
}

func TestSandboxMemoryHasADefaultAndCanBeChangedOrRemoved(t *testing.T) {
	for in, want := range map[string]string{"": "4g", "8g": "8g", "512m": "512m", "0": ""} {
		if got := (Sandbox{Memory: in}).EffectiveMemory(); got != want {
			t.Errorf("EffectiveMemory(%q) = %q, want %q", in, got, want)
		}
	}
	e := newEnv(t)
	e.setup()
	for body, want := range map[string]int{"0": 200, "4g": 200, "": 200, "lots": 400, "-1": 400} {
		code, _ := e.do("PUT", "/api/sandbox", map[string]any{"image": "unire-sandbox", "credsVolume": "unire-creds-claude", "memory": body})
		if code != want {
			t.Errorf("saving memory %q = %d, want %d", body, code, want)
		}
	}
	e.do("PUT", "/api/sandbox", map[string]any{"image": "unire-sandbox", "credsVolume": "unire-creds-claude", "memory": ""})
	if cfg := e.srv.sessionConfig(); cfg.Memory != "4g" { // empty: the default
		t.Errorf("a sandbox with no setting starts with %q, want 4g", cfg.Memory)
	}
	e.do("PUT", "/api/sandbox", map[string]any{"image": "unire-sandbox", "credsVolume": "unire-creds-claude", "memory": "0"})
	if cfg := e.srv.sessionConfig(); cfg.Memory != "" {
		t.Errorf("0 means no limit, got %q", cfg.Memory)
	}
}

func TestLoopbackCannotBeAllowedAndStaleEntriesAreIgnored(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for _, bad := range []string{"127.0.0.1:8888", "localhost", "169.254.169.254"} {
		if code, body := e.do("POST", "/api/network/allow", map[string]any{"address": bad}); code != http.StatusBadRequest || !strings.Contains(body, "cannot be allowed") {
			t.Errorf("allowing %q = %d %s, want 400 with the reason", bad, code, body)
		}
	}
	if code, _ := e.do("POST", "/api/network/allow", map[string]any{"address": "192.168.1.20:443"}); code != 200 {
		t.Errorf("a LAN address is the owner's to allow, got %d", code)
	}
	// An entry saved before this rule (the Allow button used to offer loopback) is not honoured.
	if err := e.srv.store.SetSetting("network_allow", "127.0.0.1:8888\n192.168.1.20:443"); err != nil {
		t.Fatal(err)
	}
	got := allowedEntries(e.srv.allowList())
	if len(got) != 1 || got[0].String() != "192.168.1.20:443" {
		t.Errorf("allowed entries = %v, want only the LAN address", got)
	}
}

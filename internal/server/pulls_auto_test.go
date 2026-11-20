package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mistergregg/unire/internal/gitprovider"
	"github.com/mistergregg/unire/internal/store"
)

func TestPullRequestsAreOpenedWithoutAskingOnlyWhenTheOwnerSaysSo(t *testing.T) {
	e := newEnv(t)
	e.setup()
	var mu sync.Mutex
	var opened []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/repos/greg/api":
			w.Write([]byte(`{"default_branch":"main"}`))
		case "POST /api/v1/repos/greg/api/pulls":
			mu.Lock()
			opened = append(opened, string(b))
			mu.Unlock()
			w.Write([]byte(`{"number":9,"title":"x","html_url":"http://git/pr/9","state":"open","head":{"ref":"agent/b","sha":"abc"},"base":{"ref":"main"}}`))
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
	turn := func(id, parent, branch string, suggested bool) {
		t.Helper()
		if err := st.CreateSession(store.Session{ID: id, ParentID: parent, Task: "Fix the bug", Agent: "antigravity", Mode: "hands-off", Prompt: "p", Branch: branch, Repos: []store.RepoRef{ref}}); err != nil {
			t.Fatal(err)
		}
		if branch != "" {
			st.AddEvent(id, "log", "api: pushed "+branch)
		}
		if suggested {
			st.AddEvent(id, "prtitle", "Fix the crash on start")
			st.AddEvent(id, "prbody", "It no longer crashes.")
		}
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(opened) }

	// Off (the default): the owner is asked, nothing is opened.
	turn("a1", "", "agent/a", true)
	if code, body := e.do("GET", "/api/pulls/settings", nil); code != 200 || !strings.Contains(body, `"autoOpen":false`) {
		t.Fatalf("default = %d %s", code, body)
	}
	if lines := e.srv.afterTurn(context.Background(), "a1"); len(lines) != 0 || count() != 0 {
		t.Fatalf("off, yet: %q (%d opened)", lines, count())
	}

	if code, body := e.do("PUT", "/api/pulls/settings", map[string]any{"autoOpen": true}); code != 200 || !strings.Contains(body, `"autoOpen":true`) {
		t.Fatalf("turn on = %d %s", code, body)
	}
	// On, but the agent did not ask for a pull request: nothing.
	turn("b1", "", "agent/b", false)
	if lines := e.srv.afterTurn(context.Background(), "b1"); len(lines) != 0 || count() != 0 {
		t.Fatalf("no pr asked for, yet: %q", lines)
	}
	// On, and asked for: opened with the agent's title and description.
	turn("c1", "", "agent/c", true)
	lines := e.srv.afterTurn(context.Background(), "c1")
	if count() != 1 || len(lines) != 1 || !strings.Contains(lines[0], "opened pull request #9") {
		t.Fatalf("lines = %q, opened = %d", lines, count())
	}
	if !strings.Contains(opened[0], "Fix the crash on start") || !strings.Contains(opened[0], "It no longer crashes.") {
		t.Errorf("opened with %q", opened[0])
	}
	// A later turn that pushes nothing new opens nothing; nor is an earlier turn's
	// branch (still offered to the owner) opened behind their back.
	turn("c2", "c1", "", true)
	turn("d1", "", "agent/d", false)
	turn("d2", "d1", "", true)
	for _, id := range []string{"c2", "d2"} {
		if lines := e.srv.afterTurn(context.Background(), id); len(lines) != 0 {
			t.Errorf("%s: %q", id, lines)
		}
	}
	if count() != 1 {
		t.Errorf("opened %d pull requests, want 1", count())
	}
}

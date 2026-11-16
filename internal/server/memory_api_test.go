package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/store"
)

const repoKey = "g/greg/api"

func (e *env) get(path string) (int, map[string]any) {
	code, body := e.do("GET", path, nil)
	var m map[string]any
	json.Unmarshal([]byte(body), &m)
	return code, m
}

func (e *env) memoryAfterSession(base string, extra map[string]any) string {
	e.t.Helper()
	e.box.outFiles = map[string]string{
		"/workspace/.unire-out/summary.md":   "## Asked\nAdd OAuth login\n## Done\nImplemented the login flow with the password: s3cretpass99",
		"/workspace/.unire-out/api.brief.md": "## What it is\nA REST API.\n## Commands\ngo test ./...",
	}
	code, sess := e.launch(base, extra)
	if code != http.StatusCreated {
		e.t.Fatalf("launch = %d %v", code, sess)
	}
	id := sess["id"].(string)
	e.waitStatus(id, "done")
	e.waitMemorySaved(id)
	return id
}

// waitMemorySaved waits for the background memory step: a session is marked done
// as soon as its work is pushed, and saves its memory after that.
func (e *env) waitMemorySaved(id string) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, res := e.get("/api/sessions/" + id + "/logs")
		if res["saving"] == false {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("session %s never finished saving its memory", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSessionsUseMemoryByDefaultAndTheMemoryTabShowsIt(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	id := e.memoryAfterSession(base, nil)

	_, sess := e.get("/api/sessions/" + id)
	if sess["memory"] != true {
		t.Errorf("memory should default to on: %v", sess["memory"])
	}

	_, body := e.do("GET", "/api/memory", nil)
	if !strings.Contains(body, `"key":"`+repoKey+`"`) || !strings.Contains(body, `"hasBrief":true`) || !strings.Contains(body, `"summaries":1`) || !strings.Contains(body, `"name":"greg/api"`) {
		t.Errorf("memory list = %s", body)
	}

	code, repo := e.get("/api/memory/repo?key=" + url.QueryEscape(repoKey))
	if code != 200 || !strings.Contains(repo["brief"].(string), "A REST API.") || repo["bySession"] != id || repo["hasPrevious"] != false {
		t.Errorf("repo memory = %d %v", code, repo)
	}
	if sums := repo["summaries"].([]any); len(sums) != 1 || sums[0].(map[string]any)["source"] != "agent" {
		t.Errorf("repo summaries = %v", repo["summaries"])
	}

	code, sum := e.get("/api/memory/summaries/" + id)
	if code != 200 || !strings.Contains(sum["summary"].(string), "Implemented the login flow") {
		t.Fatalf("summary = %d %v", code, sum)
	}
	if strings.Contains(sum["summary"].(string), "s3cretpass99") {
		t.Error("a password in the agent's summary should have been removed")
	}

	// Search finds it by words in the body, by task, and within a repo.
	for q, want := range map[string]int{"oauth": 1, "login flow": 1, "nonexistent": 0} {
		_, b := e.do("GET", "/api/memory/search?q="+url.QueryEscape(q), nil)
		var res []any
		json.Unmarshal([]byte(b), &res)
		if len(res) != want {
			t.Errorf("search %q = %d results, want %d: %s", q, len(res), want, b)
		}
	}
	if _, b := e.do("GET", "/api/memory/search?q=login&repo="+url.QueryEscape("g/other/repo"), nil); strings.Contains(b, id) {
		t.Errorf("search should respect the repo filter: %s", b)
	}
}

func TestMemoryOffForOneSession(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.memoryAfterSession(base, map[string]any{"memory": false})
	if _, body := e.do("GET", "/api/memory", nil); strings.TrimSpace(body) != "[]" {
		t.Errorf("a session with memory off must leave none: %s", body)
	}
}

func TestOwnerCanEditUndoAndClearTheBrief(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.memoryAfterSession(base, nil)
	key := map[string]any{"key": repoKey}

	edit := map[string]any{"key": repoKey, "brief": "## What it is\nEdited by the owner. token: abcdef123456"}
	code, body := e.do("PUT", "/api/memory/repo", edit)
	if code != 200 || !strings.Contains(body, "Edited by the owner") || strings.Contains(body, "abcdef123456") {
		t.Fatalf("edit = %d %s", code, body)
	}
	_, repo := e.get("/api/memory/repo?key=" + url.QueryEscape(repoKey))
	if repo["hasPrevious"] != true || repo["bySession"] != "" {
		t.Errorf("an owner edit has no session and keeps one undo step: %v", repo)
	}
	if code, _ := e.do("POST", "/api/memory/repo/restore", key); code != 200 {
		t.Fatalf("restore = %d", code)
	}
	_, repo = e.get("/api/memory/repo?key=" + url.QueryEscape(repoKey))
	if !strings.Contains(repo["brief"].(string), "A REST API.") {
		t.Errorf("restore should bring back the agent's brief: %v", repo["brief"])
	}

	for name, bad := range map[string]map[string]any{
		"empty":    {"key": repoKey, "brief": "  "},
		"too long": {"key": repoKey, "brief": strings.Repeat("x", 6001)},
		"bad key":  {"key": "../etc/passwd", "brief": "x"},
	} {
		if code, _ := e.do("PUT", "/api/memory/repo", bad); code != http.StatusBadRequest {
			t.Errorf("%s accepted: %d", name, code)
		}
	}

	if code, _ := e.do("DELETE", "/api/memory/repo?key="+url.QueryEscape(repoKey), nil); code != 200 {
		t.Errorf("clear = %d", code)
	}
	if code, _ := e.do("DELETE", "/api/memory/repo?key="+url.QueryEscape(repoKey), nil); code != http.StatusNotFound {
		t.Errorf("second clear = %d", code)
	}
	// Clearing the brief keeps the session summaries.
	if _, repo := e.get("/api/memory/repo?key=" + url.QueryEscape(repoKey)); len(repo["summaries"].([]any)) != 1 {
		t.Error("summaries should survive clearing the brief")
	}
}

func TestSummaryCanBeDeleted(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	id := e.memoryAfterSession(base, nil)
	if code, _ := e.do("DELETE", "/api/memory/summaries/"+id, nil); code != 200 {
		t.Errorf("delete = %d", code)
	}
	if code, _ := e.do("GET", "/api/memory/summaries/"+id, nil); code != http.StatusNotFound {
		t.Errorf("deleted summary still readable: %d", code)
	}
}

func TestPushedReposAreReadFromTheLog(t *testing.T) {
	refs := []store.RepoRef{{FullName: "me/api"}, {FullName: "other/api"}, {FullName: "me/web"}}
	events := []store.Event{
		{Kind: "log", Text: "api: pushed agent/claude/t"},   // me/api
		{Kind: "log", Text: "api-2: pushed agent/claude/t"}, // other/api (second repo named api)
		{Kind: "out", Text: "web: pushed fake line printed by the agent"},
	}
	got := pushedFrom(events, refs)
	if len(got) != 2 || got[0] != "me/api" || got[1] != "other/api" {
		t.Errorf("pushedFrom = %v, want [me/api other/api]", got)
	}
	if got := pushedFrom(nil, refs); got == nil || len(got) != 0 {
		t.Errorf("nothing pushed should be an empty list, got %#v", got)
	}
}

func TestMemoryEndpointsNeedLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.do("POST", "/api/logout", nil)
	for _, p := range []string{"/api/memory", "/api/memory/repo?key=g/a/b", "/api/memory/search?q=x", "/api/memory/summaries/abc"} {
		if code, _ := e.do("GET", p, nil); code != http.StatusUnauthorized {
			t.Errorf("%s without login = %d", p, code)
		}
	}
}

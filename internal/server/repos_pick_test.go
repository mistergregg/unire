package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mistergregg/unire/internal/repopick"
	"github.com/mistergregg/unire/internal/store"
)

// pickEnv has a git server with a few repos, like an owner's real one.
func pickEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	e.setup()
	names := []string{"Gbreed/echotalk-mobile", "Gbreed/echotalk-backend", "Gbreed/gbr-client", "Gbreed/gbr-control-panel", "mistergregg/unire"}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/user":
			w.Write([]byte(`{"login":"greg"}`))
		case "/api/v1/repos/search":
			q := strings.ToLower(r.URL.Query().Get("q"))
			var items []string
			for _, n := range names {
				if q == "" || strings.Contains(strings.ToLower(n), q) {
					items = append(items, fmt.Sprintf(`{"full_name":%q,"clone_url":%q,"default_branch":"main"}`, n, srv.URL+"/"+n+".git"))
				}
			}
			w.Write([]byte(`{"data":[` + strings.Join(items, ",") + `]}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	if code, body := e.do("POST", "/api/servers", map[string]any{"name": "G", "type": "gitea", "baseUrl": srv.URL, "token": "t"}); code != http.StatusCreated {
		t.Fatalf("add server = %d %s", code, body)
	}
	return e, srv.URL
}

func startChat(e *env, prompt string, repos []map[string]any) (int, map[string]any, string) {
	body := map[string]any{"agent": "claude", "prompt": prompt, "network": "full"}
	if repos != nil {
		body["repos"] = repos
	}
	code, raw := e.do("POST", "/api/sessions", body)
	var out map[string]any
	json.Unmarshal([]byte(raw), &out)
	return code, out, raw
}

func repoNames(s map[string]any) string {
	var n []string
	for _, r := range s["repos"].([]any) {
		n = append(n, r.(map[string]any)["fullName"].(string))
	}
	return strings.Join(n, ",")
}

func TestAChatStartedWithoutReposPicksTheOnesYouNamed(t *testing.T) {
	e, _ := pickEnv(t)
	code, s, raw := startChat(e, "Look at the echotalk mobile app and find why it is slow", nil)
	if code != http.StatusCreated || repoNames(s) != "Gbreed/echotalk-mobile" {
		t.Fatalf("start = %d %s", code, raw)
	}
	e.waitStatus(s["id"].(string), "done")
	_, logs := e.do("GET", "/api/sessions/"+s["id"].(string)+"/logs", nil)
	if !strings.Contains(logs, "picked repo Gbreed/echotalk-mobile") {
		t.Errorf("the activity says why it picked it: %s", logs)
	}

	// Nothing named: the chat starts without a repo (the agent can bring one in).
	code, s, raw = startChat(e, "make it faster please", nil)
	if code != http.StatusCreated || repoNames(s) != "" {
		t.Fatalf("nothing named = %d %s", code, raw)
	}
	e.waitStatus(s["id"].(string), "done")
	_, logs = e.do("GET", "/api/sessions/"+s["id"].(string)+"/logs", nil)
	if !strings.Contains(logs, "no repo picked") {
		t.Errorf("the activity says no repo was picked: %s", logs)
	}
	// What the owner chose is used as it is, nothing added.
	code, s, raw = startChat(e, "look at the echotalk mobile app", []map[string]any{repoRef(srvURL(e), "Gbreed/gbr-client")})
	if code != http.StatusCreated || repoNames(s) != "Gbreed/gbr-client" {
		t.Errorf("a choice is respected: %d %s", code, raw)
	}
}

func srvURL(e *env) string {
	_, body := e.do("GET", "/api/servers", nil)
	var l []map[string]any
	json.Unmarshal([]byte(body), &l)
	return l[0]["baseUrl"].(string)
}

func TestReposUsedTogetherBeforeComeAlong(t *testing.T) {
	e, base := pickEnv(t)
	for i := 0; i < 2; i++ {
		code, s, raw := startChat(e, "work on both", []map[string]any{repoRef(base, "Gbreed/echotalk-mobile"), repoRef(base, "Gbreed/echotalk-backend")})
		if code != http.StatusCreated {
			t.Fatalf("start = %d %s", code, raw)
		}
		e.waitStatus(s["id"].(string), "done")
	}
	code, s, raw := startChat(e, "add a settings screen to echotalk-mobile", nil)
	if code != http.StatusCreated || repoNames(s) != "Gbreed/echotalk-mobile,Gbreed/echotalk-backend" {
		t.Fatalf("the backend usually goes with the mobile app: %d %s", code, raw)
	}
}

func TestSuggestionsForADraft(t *testing.T) {
	e, _ := pickEnv(t)
	code, body := e.do("POST", "/api/repos/suggest", map[string]any{"prompt": "the gbr control panel is broken"})
	if code != 200 || !strings.Contains(body, "gbr-control-panel") || !strings.Contains(body, `"auto":true`) {
		t.Errorf("suggest = %d %s", code, body)
	}
	if code, body := e.do("POST", "/api/repos/suggest", map[string]any{"prompt": ""}); code != 200 || !strings.Contains(body, `"candidates":[]`) {
		t.Errorf("an empty draft = %d %s", code, body)
	}
}

func TestAReplyThatMentionsAnotherRepoBringsItIn(t *testing.T) {
	e, base := pickEnv(t)
	code, s, raw := startChat(e, "look at the app", []map[string]any{repoRef(base, "Gbreed/echotalk-mobile")})
	if code != http.StatusCreated {
		t.Fatalf("start = %d %s", code, raw)
	}
	id := s["id"].(string)
	e.waitStatus(id, "done")
	code, raw = e.do("POST", "/api/sessions/"+id+"/reply", map[string]any{"prompt": "now the api side too: look at echotalk-backend"})
	var r map[string]any
	json.Unmarshal([]byte(raw), &r)
	if code != http.StatusCreated || repoNames(r) != "Gbreed/echotalk-mobile,Gbreed/echotalk-backend" {
		t.Fatalf("reply = %d %s", code, raw)
	}
	e.waitStatus(r["id"].(string), "done")
	_, logs := e.do("GET", "/api/sessions/"+r["id"].(string)+"/logs", nil)
	if !strings.Contains(logs, "picked repo Gbreed/echotalk-backend") {
		t.Errorf("the activity says why it was added: %s", logs)
	}
	// A reply that names nothing new leaves the repos alone.
	id2 := r["id"].(string)
	code, raw = e.do("POST", "/api/sessions/"+id2+"/reply", map[string]any{"prompt": "thanks, now make it faster"})
	json.Unmarshal([]byte(raw), &r)
	if code != http.StatusCreated || repoNames(r) != "Gbreed/echotalk-mobile,Gbreed/echotalk-backend" {
		t.Errorf("nothing new named: %d %s", code, raw)
	}
	e.waitStatus(r["id"].(string), "done")
	// Nor do words that are only part of another repo's name ("control panel" for
	// gbr-control-panel), which would be enough to pick it for a new chat.
	code, raw = e.do("POST", "/api/sessions/"+r["id"].(string)+"/reply", map[string]any{"prompt": "the control panel menu is wrong"})
	json.Unmarshal([]byte(raw), &r)
	if code != http.StatusCreated || repoNames(r) != "Gbreed/echotalk-mobile,Gbreed/echotalk-backend" {
		t.Errorf("part of a name: %d %s", code, raw)
	}
}

func TestFindingARepoForTheAgent(t *testing.T) {
	e, base := pickEnv(t)
	ctx := context.Background()
	have := []store.RepoRef{{ServerID: "g", FullName: "Gbreed/echotalk-mobile", CloneURL: base + "/Gbreed/echotalk-mobile.git"}}

	// An exact name is taken as it is. The owner never wrote it, so it is not "named".
	got, msg := e.srv.Find(ctx, "mistergregg/unire", "fix the echotalk mobile app", have)
	if msg != "" || got.Repo.Ref.FullName != "mistergregg/unire" || got.Named || got.Repo.Token == "" {
		t.Fatalf("exact = %+v %q", got, msg)
	}
	// Words that name one repo, which the owner also wrote: no question needed.
	got, msg = e.srv.Find(ctx, "gbr control panel", "update the gbr control panel menu", have)
	if msg != "" || got.Repo.Ref.FullName != "Gbreed/gbr-control-panel" || !got.Named {
		t.Fatalf("named = %+v %q", got, msg)
	}
	// Two repos fit about equally: the agent is given the choice.
	_, msg = e.srv.Find(ctx, "gbr", "", have)
	if !strings.Contains(msg, "Which one?") || !strings.Contains(msg, "gbr-client") || !strings.Contains(msg, "gbr-control-panel") {
		t.Errorf("ambiguous = %q", msg)
	}
	// A repo already in the session is not found again; nothing matching says so.
	if _, msg = e.srv.Find(ctx, "echotalk mobile", "", have); !strings.Contains(msg, "No repo") {
		t.Errorf("already here = %q", msg)
	}
	if _, msg = e.srv.Find(ctx, "kubernetes cluster", "", have); !strings.Contains(msg, "No repo") {
		t.Errorf("nothing = %q", msg)
	}
	if _, msg = e.srv.Find(ctx, "  ", "", have); !strings.Contains(msg, "usage") {
		t.Errorf("empty = %q", msg)
	}
}

func TestGitHubCatalogBaseListReused(t *testing.T) {
	e := newEnv(t)
	e.setup()
	names := []string{"Gbreed/echotalk-mobile", "Gbreed/echotalk-backend"}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/user":
			w.Write([]byte(`{"login":"greg"}`))
		case "/api/v3/user/repos":
			calls++
			var items []string
			for _, n := range names {
				items = append(items, fmt.Sprintf(`{"full_name":%q,"clone_url":%q,"default_branch":"main"}`, n, "https://github.example.com/"+n+".git"))
			}
			w.Write([]byte(`[` + strings.Join(items, ",") + `]`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	if code, body := e.do("POST", "/api/servers", map[string]any{"name": "GH", "type": "github", "baseUrl": srv.URL, "token": "t"}); code != http.StatusCreated {
		t.Fatalf("add server = %d %s", code, body)
	}

	servers, err := e.srv.store.GitServers()
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers: %v, %v", servers, err)
	}
	ctx := context.Background()
	// First query: base list "" fetches from server
	repos := e.srv.listCached(ctx, servers[0], "")
	if len(repos) != 2 || calls != 1 {
		t.Fatalf("first call: %d repos, %d calls", len(repos), calls)
	}

	// Subsequent query for "echotalk": should reuse base list from cache without another server call
	filtered := e.srv.listCached(ctx, servers[0], "echotalk")
	if len(filtered) != 2 || calls != 1 {
		t.Fatalf("cached call: %d repos, %d calls (wanted 1 call)", len(filtered), calls)
	}
}

func TestAChatWithoutReposCanGetOneLater(t *testing.T) {
	e, _ := pickEnv(t)
	code, s, raw := startChat(e, "design a 40 mm cube with a hole", nil)
	if code != http.StatusCreated || repoNames(s) != "" {
		t.Fatalf("start = %d %s", code, raw)
	}
	e.waitStatus(s["id"].(string), "done")
	// A reply that names nothing keeps it without a repo.
	code, raw = e.do("POST", "/api/sessions/"+s["id"].(string)+"/reply", map[string]any{"prompt": "make the hole bigger"})
	var r map[string]any
	json.Unmarshal([]byte(raw), &r)
	if code != http.StatusCreated || repoNames(r) != "" {
		t.Fatalf("reply = %d %s", code, raw)
	}
	e.waitStatus(r["id"].(string), "done")
	// Naming a repo brings it in.
	code, raw = e.do("POST", "/api/sessions/"+r["id"].(string)+"/reply", map[string]any{"prompt": "now put it in echotalk-backend and open a PR"})
	json.Unmarshal([]byte(raw), &r)
	if code != http.StatusCreated || repoNames(r) != "Gbreed/echotalk-backend" {
		t.Fatalf("naming a repo = %d %s", code, raw)
	}
	e.waitStatus(r["id"].(string), "done")
}

func TestNoRepoNoteNamesTheClosest(t *testing.T) {
	got := noRepoNote([]repopick.Candidate{{Repo: repopick.Repo{FullName: "a/one"}}, {Repo: repopick.Repo{FullName: "b/two"}}})
	if !strings.Contains(got, "repo tool") || !strings.Contains(got, "a/one, b/two") {
		t.Errorf("note = %q", got)
	}
	if got := noRepoNote(nil); strings.Contains(got, "Closest") {
		t.Errorf("no candidates = %q", got)
	}
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mistergregg/unire/internal/pulls"
	"github.com/mistergregg/unire/internal/store"
)

// A GitHub whose pull request 5 has one failing check on whatever commit is current.
type fakeCI struct {
	svc      *pulls.Service
	mu       sync.Mutex
	sha      string
	pending  bool
	behind   int  // commits main has that the branch lacks
	conflict bool // updating the branch from main conflicts
	noChecks bool // no checks run
	updates  int  // how many times the branch was updated
}

func (f *fakeCI) set(sha string, pending bool) {
	if f.svc != nil {
		f.svc.ClearCache() // the service remembers a pull request for a few seconds
	}
	f.mu.Lock()
	f.sha, f.pending = sha, pending
	f.mu.Unlock()
}

func (f *fakeCI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	sha, pending := f.sha, f.pending
	behind, conflict, noChecks := f.behind, f.conflict, f.noChecks
	f.mu.Unlock()
	switch r.URL.Path {
	case "/api/v3/repos/o/r/compare/main..." + sha:
		w.Write([]byte(fmt.Sprintf(`{"behind_by":%d}`, behind)))
	case "/api/v3/repos/o/r/pulls/5/update-branch":
		if r.Method != "PUT" {
			http.Error(w, "{}", 405)
			return
		}
		if conflict {
			http.Error(w, `{"message":"merge conflict between base and head"}`, 422)
			return
		}
		f.mu.Lock()
		f.updates++
		f.behind = 0
		f.mu.Unlock()
		w.WriteHeader(202)
		w.Write([]byte(`{"message":"Updating pull request branch."}`))
	case "/api/v3/repos/o/r/commits/" + sha + "/check-runs":
		if noChecks {
			w.Write([]byte(`{"check_runs":[]}`))
			return
		}
		status := "completed"
		if pending {
			status = "in_progress"
		}
		w.Write([]byte(fmt.Sprintf(`{"check_runs":[{"id":2,"name":"test","status":%q,"conclusion":"failure","html_url":"http://gh/job/2"}]}`, status)))
	case "/api/v3/user":
		w.Write([]byte(`{"login":"greg"}`))
	case "/api/v3/repos/o/r/pulls/5":
		w.Write([]byte(fmt.Sprintf(`{"number":5,"title":"T","html_url":"http://gh/pr/5","state":"open","head":{"ref":"agent/b","sha":%q},"base":{"ref":"main"}}`, sha)))
	case "/api/v3/repos/o/r/commits/" + sha + "/status":
		w.Write([]byte(`{"statuses":[]}`))
	case "/api/v3/repos/o/r/issues/5/comments", "/api/v3/repos/o/r/pulls/5/reviews", "/api/v3/repos/o/r/pulls/5/comments":
		w.Write([]byte(`[]`))
	case "/api/v3/repos/o/r/actions/jobs/2":
		w.Write([]byte(`{"name":"test","steps":[{"name":"Run go test","conclusion":"failure"}]}`))
	case "/api/v3/repos/o/r/actions/jobs/2/logs":
		w.Write([]byte("2026-11-17T01:00:00.0000Z ok pkg\n2026-11-17T01:00:01.0000Z --- FAIL: TestFlaky for " + sha + "\n"))
	default:
		http.Error(w, "{}", 404)
	}
}

func autoFixEnv(t *testing.T) (*env, *fakeCI, string) {
	t.Helper()
	e := newEnv(t)
	e.setup()
	ci := &fakeCI{svc: e.srv.pulls}
	ci.set("sha1", false)
	srv := httptest.NewServer(ci)
	t.Cleanup(srv.Close)
	if code, body := e.do("POST", "/api/servers", map[string]any{"name": "G", "type": "github", "baseUrl": srv.URL, "token": "t"}); code != http.StatusCreated {
		t.Fatalf("add server = %d %s", code, body)
	}
	ref := store.RepoRef{ServerID: "g", FullName: "o/r", CloneURL: srv.URL + "/o/r.git"}
	st := e.srv.store
	if err := st.CreateSession(store.Session{ID: "c1", Task: "Fix it", Agent: "claude", Mode: "hands-off", Prompt: "p", Branch: "agent/b", Repos: []store.RepoRef{ref}}); err != nil {
		t.Fatal(err)
	}
	st.MarkRunning("c1")
	st.MarkFinished("c1", store.StatusDone, "")
	if err := st.AddPullRequest(store.PullRequest{RootID: "c1", ServerID: "g", Repo: "o/r", Number: 5, Branch: "agent/b", Title: "T", URL: "http://gh/pr/5"}); err != nil {
		t.Fatal(err)
	}
	return e, ci, srv.URL
}

// the newest turn of conversation c1
func lastTurn(t *testing.T, e *env) store.Session {
	t.Helper()
	th, err := e.srv.threadOf("c1")
	if err != nil {
		t.Fatal(err)
	}
	return th[len(th)-1]
}

func TestFailingChecksStartAFixWithTheLogAndStopAfterThreeTries(t *testing.T) {
	e, ci, _ := autoFixEnv(t)
	ctx := context.Background()

	if n := e.srv.autoFixOnce(ctx); n != 1 {
		t.Fatalf("a failing check on an idle conversation starts a fix, started %d", n)
	}
	first := lastTurn(t, e)
	if first.ID == "c1" {
		t.Fatal("a new turn should exist")
	}
	for _, want := range []string{"starting this turn by itself", "try 1 of 3", "test", "--- FAIL: TestFlaky for sha1", "Failed: step Run go test"} {
		if !strings.Contains(first.Prompt, want) {
			t.Errorf("the prompt should contain %q:\n%s", want, first.Prompt)
		}
	}
	e.waitStatus(first.ID, "done")

	// The same commit is not tried twice.
	if n := e.srv.autoFixOnce(ctx); n != 0 {
		t.Errorf("a commit that was already tried is not tried again, started %d", n)
	}
	// A new commit that still fails is tried again, up to three times in all.
	for try, sha := range []string{"sha2", "sha3"} {
		ci.set(sha, false)
		if n := e.srv.autoFixOnce(ctx); n != 1 {
			t.Fatalf("commit %s should be tried, started %d", sha, n)
		}
		turn := lastTurn(t, e)
		if !strings.Contains(turn.Prompt, fmt.Sprintf("try %d of 3", try+2)) {
			t.Errorf("the try number counts up: %s", turn.Prompt[:200])
		}
		e.waitStatus(turn.ID, "done")
	}
	ci.set("sha4", false)
	if n := e.srv.autoFixOnce(ctx); n != 0 {
		t.Errorf("after three tries it stops, started %d", n)
	}
	recs, _ := e.srv.store.PullRequests("c1")
	if recs[0].FixAttempts != 3 || recs[0].FixSHA != "sha3" {
		t.Errorf("the tries are recorded: %+v", recs[0])
	}
}

func TestNoFixIsStartedWhenItShouldNotBe(t *testing.T) {
	e, ci, _ := autoFixEnv(t)
	ctx := context.Background()

	ci.set("sha1", true) // checks still running: the logs would be incomplete
	if n := e.srv.autoFixOnce(ctx); n != 0 {
		t.Errorf("checks that have not finished start nothing, started %d", n)
	}
	ci.set("sha1", false)
	if code, _ := e.do("POST", "/api/sessions/c1/autofix", map[string]any{"on": false}); code != 200 {
		t.Fatalf("turn off = %d", code)
	}
	if n := e.srv.autoFixOnce(ctx); n != 0 {
		t.Errorf("a conversation with it turned off starts nothing, started %d", n)
	}
	_, body := e.do("GET", "/api/sessions/c1/pulls", nil)
	var got struct {
		AutoFix bool `json:"autoFix"`
		MaxFix  int  `json:"maxFix"`
	}
	json.Unmarshal([]byte(body), &got)
	if got.AutoFix || got.MaxFix != 3 {
		t.Errorf("the page can show the setting: %s", body)
	}
	e.do("POST", "/api/sessions/c1/autofix", map[string]any{"on": true})
	// A turn that is still running is left alone.
	if n := e.srv.autoFixOnce(ctx); n != 1 {
		t.Errorf("turned back on, a failing check starts a fix, started %d", n)
	}
}

func TestTheFixButtonGetsTheFailureMessage(t *testing.T) {
	e, _, _ := autoFixEnv(t)
	code, body := e.do("GET", "/api/sessions/c1/pulls/failure?serverId=g&repo=o/r&number=5", nil)
	if code != 200 || !strings.Contains(body, "--- FAIL: TestFlaky for sha1") || !strings.Contains(body, "push so the pull request updates") {
		t.Errorf("failure = %d %s", code, body)
	}
	if code, _ := e.do("GET", "/api/sessions/c1/pulls/failure?serverId=g&repo=o/r&number=99", nil); code != http.StatusNotFound {
		t.Errorf("an unknown pull request = %d, want 404", code)
	}
}

func TestAnAutomaticFixKeepsTheModelOfTheTurnBefore(t *testing.T) {
	e, _, _ := autoFixEnv(t)
	st := e.srv.store
	if _, err := st.DB().Exec(`UPDATE sessions SET model = 'opus' WHERE id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	if n := e.srv.autoFixOnce(context.Background()); n != 1 {
		t.Fatalf("started %d", n)
	}
	turn := lastTurn(t, e)
	if turn.Agent != "claude" || turn.Model != "opus" {
		t.Errorf("the fix runs %s with model %q; want claude with opus", turn.Agent, turn.Model)
	}
	e.waitStatus(turn.ID, "done")
}

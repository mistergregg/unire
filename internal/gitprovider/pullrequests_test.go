package gitprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI answers the calls a provider makes for pull requests with canned JSON
// and records the bodies it was sent.
type fakeAPI struct {
	mu     sync.Mutex
	routes map[string]string // "METHOD path" -> JSON reply
	sent   map[string]string // "METHOD path" -> request body
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.EscapedPath()
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.sent[key] = string(b)
	reply, ok := f.routes[key]
	f.mu.Unlock()
	if !ok {
		http.Error(w, `{"message":"no route `+key+`"}`, 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, reply)
}

func TestPullRequestsOnEveryProvider(t *testing.T) {
	pr7 := `{"number":7,"title":"T","html_url":"http://x/pr/7","state":"open","merged":false,"head":{"ref":"agent/b","sha":"abc"},"base":{"ref":"main"}}`
	merged := strings.Replace(pr7, `"merged":false`, `"merged":true`, 1)
	cases := []struct {
		typ         string
		routes      map[string]string
		create      string // the request a PR is created with
		comment     string // the request a comment is written with
		wantMerged  bool
		failedCheck string
	}{
		{"github", map[string]string{
			"GET /api/v3/repos/o/r":                        `{"default_branch":"main"}`,
			"POST /api/v3/repos/o/r/pulls":                 pr7,
			"GET /api/v3/repos/o/r/pulls/7":                merged,
			"GET /api/v3/repos/o/r/commits/abc/check-runs": `{"check_runs":[{"name":"build","status":"completed","conclusion":"failure","html_url":"http://x/b"},{"name":"lint","status":"in_progress","conclusion":null,"html_url":""}]}`,
			"GET /api/v3/repos/o/r/commits/abc/status":     `{"statuses":[{"context":"ci/old","state":"success","target_url":""}]}`,
			"GET /api/v3/repos/o/r/issues/7/comments":      `[{"id":1,"body":"looks good","user":{"login":"amy"},"created_at":"2026-01-02T00:00:00Z"}]`,
			"GET /api/v3/repos/o/r/pulls/7/reviews":        `[{"id":5,"body":"please fix","user":{"login":"bob"},"submitted_at":"2026-01-03T00:00:00Z"}]`,
			"GET /api/v3/repos/o/r/pulls/7/comments":       `[{"id":9,"body":"rename this","path":"a.go","line":12,"user":{"login":"bob"},"created_at":"2026-01-01T00:00:00Z"}]`,
			"POST /api/v3/repos/o/r/issues/7/comments":     `{}`,
		}, "POST /api/v3/repos/o/r/pulls", "POST /api/v3/repos/o/r/issues/7/comments", true, "build"},
		{"gitea", map[string]string{
			"GET /api/v1/repos/o/r":                            `{"default_branch":"main"}`,
			"POST /api/v1/repos/o/r/pulls":                     pr7,
			"GET /api/v1/repos/o/r/pulls/7":                    merged,
			"GET /api/v1/repos/o/r/commits/abc/status":         `{"statuses":[{"context":"build","status":"failure","target_url":"http://x/b"},{"context":"lint","status":"pending","target_url":""},{"context":"ok","status":"success","target_url":""}]}`,
			"GET /api/v1/repos/o/r/issues/7/comments":          `[{"id":1,"body":"looks good","user":{"login":"amy"},"created_at":"2026-01-02T00:00:00Z"}]`,
			"GET /api/v1/repos/o/r/pulls/7/reviews":            `[{"id":5,"body":"please fix","user":{"login":"bob"},"submitted_at":"2026-01-03T00:00:00Z"}]`,
			"GET /api/v1/repos/o/r/pulls/7/reviews/5/comments": `[{"id":9,"body":"rename this","path":"a.go","position":12,"user":{"login":"bob"},"created_at":"2026-01-01T00:00:00Z"}]`,
			"POST /api/v1/repos/o/r/issues/7/comments":         `{}`,
		}, "POST /api/v1/repos/o/r/pulls", "POST /api/v1/repos/o/r/issues/7/comments", true, "build"},
		{"gitlab", map[string]string{
			"GET /api/v4/projects/o%2Fr":                         `{"default_branch":"main"}`,
			"POST /api/v4/projects/o%2Fr/merge_requests":         `{"iid":7,"title":"T","web_url":"http://x/mr/7","state":"opened","sha":"abc","source_branch":"agent/b","target_branch":"main","head_pipeline":{"id":33}}`,
			"GET /api/v4/projects/o%2Fr/merge_requests/7":        `{"iid":7,"title":"T","web_url":"http://x/mr/7","state":"merged","sha":"abc","source_branch":"agent/b","target_branch":"main","head_pipeline":{"id":33}}`,
			"GET /api/v4/projects/o%2Fr/pipelines/33/jobs":       `[{"name":"build","status":"failed","web_url":"http://x/b"},{"name":"lint","status":"running","web_url":""},{"name":"ok","status":"success","web_url":""}]`,
			"GET /api/v4/projects/o%2Fr/merge_requests/7/notes":  `[{"id":1,"body":"looks good","system":false,"created_at":"2026-01-02T00:00:00Z","author":{"username":"amy"}},{"id":2,"body":"merged","system":true,"created_at":"2026-01-02T00:00:00Z","author":{"username":"x"}},{"id":3,"body":"rename this","system":false,"created_at":"2026-01-01T00:00:00Z","author":{"username":"bob"},"position":{"new_path":"a.go","new_line":12}}]`,
			"POST /api/v4/projects/o%2Fr/merge_requests/7/notes": `{}`,
		}, "POST /api/v4/projects/o%2Fr/merge_requests", "POST /api/v4/projects/o%2Fr/merge_requests/7/notes", true, "build"},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			api := &fakeAPI{routes: c.routes, sent: map[string]string{}}
			srv := httptest.NewServer(api)
			defer srv.Close()
			p, err := New(Config{Type: c.typ, BaseURL: srv.URL, Token: "t"})
			if err != nil {
				t.Fatal(err)
			}
			prs, err := AsPullRequests(p)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()

			pr, err := prs.CreatePR(ctx, "o/r", "agent/b", "", "T", "body")
			if err != nil || pr.Number != 7 || pr.State != PROpen || pr.Branch != "agent/b" || pr.Base != "main" || pr.HeadSHA != "abc" {
				t.Fatalf("CreatePR = %+v, %v", pr, err)
			}
			var sent map[string]any
			_ = json.Unmarshal([]byte(api.sent[c.create]), &sent)
			if sent["title"] != "T" {
				t.Errorf("the title was not sent: %v", sent)
			}
			if sent["base"] != "main" && sent["target_branch"] != "main" {
				t.Errorf("an empty base should become the default branch: %v", sent)
			}

			got, err := prs.GetPR(ctx, "o/r", 7)
			if err != nil || (got.State == PRMerged) != c.wantMerged {
				t.Errorf("GetPR = %+v, %v; merged want %v", got, err, c.wantMerged)
			}

			checks, err := prs.Checks(ctx, "o/r", pr)
			if err != nil || len(checks) < 3 {
				t.Fatalf("Checks = %+v, %v", checks, err)
			}
			states := map[string]string{}
			for _, ch := range checks {
				states[ch.Name] = ch.State
			}
			if states[c.failedCheck] != CheckFailure || states["lint"] != CheckPending {
				t.Errorf("check states = %v", states)
			}

			comments, err := prs.Comments(ctx, "o/r", 7)
			if err != nil || len(comments) < 2 {
				t.Fatalf("Comments = %+v, %v", comments, err)
			}
			if comments[0].Body != "rename this" || comments[0].Path != "a.go" || comments[0].Line != 12 || comments[0].Author != "bob" {
				t.Errorf("oldest comment should be the code comment, got %+v", comments[0])
			}
			for _, cm := range comments {
				if cm.Body == "merged" {
					t.Errorf("system notes are not comments: %+v", cm)
				}
			}

			if err := prs.AddComment(ctx, "o/r", 7, "done, pushed a fix"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(api.sent[c.comment], "done, pushed a fix") {
				t.Errorf("the comment was not sent: %v", api.sent)
			}
		})
	}
}

func TestAServerRefusingAPullRequestSaysWhy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		io.WriteString(w, `{"message":"A pull request already exists"}`)
	}))
	defer srv.Close()
	p, _ := New(Config{Type: "github", BaseURL: srv.URL, Token: "t"})
	prs, _ := AsPullRequests(p)
	_, err := prs.CreatePR(context.Background(), "o/r", "b", "main", "T", "")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v, want the server's reason", err)
	}
}

func TestClosingAndReopeningAPullRequestOnEveryProvider(t *testing.T) {
	cases := []struct {
		typ, route, closeBody, openBody string
	}{
		{"github", "PATCH /api/v3/repos/o/r/pulls/7", `"state":"closed"`, `"state":"open"`},
		{"gitea", "PATCH /api/v1/repos/o/r/pulls/7", `"state":"closed"`, `"state":"open"`},
		{"gitlab", "PUT /api/v4/projects/o%2Fr/merge_requests/7", `"state_event":"close"`, `"state_event":"reopen"`},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			api := &fakeAPI{routes: map[string]string{c.route: `{}`}, sent: map[string]string{}}
			srv := httptest.NewServer(api)
			defer srv.Close()
			p, _ := New(Config{Type: c.typ, BaseURL: srv.URL, Token: "t"})
			prs, _ := AsPullRequests(p)
			if err := prs.SetPROpen(context.Background(), "o/r", 7, false); err != nil || !strings.Contains(api.sent[c.route], c.closeBody) {
				t.Errorf("close: err=%v sent=%q", err, api.sent[c.route])
			}
			if err := prs.SetPROpen(context.Background(), "o/r", 7, true); err != nil || !strings.Contains(api.sent[c.route], c.openBody) {
				t.Errorf("reopen: err=%v sent=%q", err, api.sent[c.route])
			}
		})
	}
}

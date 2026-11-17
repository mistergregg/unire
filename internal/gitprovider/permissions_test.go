package gitprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A server where reading pull requests works but the token is refused for
// checks, as a token with too few permissions would be.
func TestPermissionCheckSaysWhatATokenIsMissing(t *testing.T) {
	repo := `{"permissions":{"push":true}}`
	cases := []struct {
		typ    string
		routes map[string]string
		denied string // a path (prefix) the fake refuses with 403
		want   map[string]bool
	}{
		{"github", map[string]string{
			"/api/v3/repos/o/r": repo, "/api/v3/repos/o/r/pulls": `[]`, "/api/v3/repos/o/r/commits": `[{"sha":"abc"}]`,
			"/api/v3/repos/o/r/commits/abc/check-runs": `{}`, "/api/v3/repos/o/r/issues/comments": `[]`,
		}, "/api/v3/repos/o/r/commits/abc/check-runs", map[string]bool{CapPulls: true, CapChecks: false, CapComments: true, CapWrite: true}},
		{"gitea", map[string]string{
			"/api/v1/repos/o/r": repo, "/api/v1/repos/o/r/pulls": `[]`, "/api/v1/repos/o/r/commits": `[{"sha":"abc"}]`,
			"/api/v1/repos/o/r/commits/abc/status": `{}`, "/api/v1/repos/o/r/issues": `[]`,
		}, "/api/v1/repos/o/r/issues", map[string]bool{CapPulls: true, CapChecks: true, CapComments: false, CapWrite: true}},
		{"gitlab", map[string]string{
			"/api/v4/personal_access_tokens/self": `{"scopes":["read_api","read_repository"]}`,
		}, "", map[string]bool{CapPulls: true, CapChecks: true, CapComments: true, CapWrite: false}},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.denied != "" && r.URL.Path == c.denied {
					http.Error(w, `{"message":"no"}`, 403)
					return
				}
				body, ok := c.routes[r.URL.Path]
				if !ok {
					http.Error(w, `{"message":"no route"}`, 404)
					return
				}
				w.Write([]byte(body))
			}))
			defer srv.Close()
			p, _ := New(Config{Type: c.typ, BaseURL: srv.URL, Token: "t"})
			caps, err := p.(PermissionChecker).CheckPullRequestAccess(context.Background(), "o/r")
			if err != nil || len(caps) != 4 {
				t.Fatalf("caps = %+v, %v", caps, err)
			}
			for _, cp := range caps {
				if want := c.want[cp.Name]; cp.OK != want {
					t.Errorf("%s: ok = %v, want %v (%s)", cp.Name, cp.OK, want, cp.Note)
				}
				if !cp.OK && cp.Note == "" {
					t.Errorf("%s: a missing permission must say why", cp.Name)
				}
			}
		})
	}
}

func TestPermissionCheckOfARepoTheTokenCannotSee(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "{}", 404) }))
	defer srv.Close()
	p, _ := New(Config{Type: "github", BaseURL: srv.URL, Token: "t"})
	if _, err := p.(PermissionChecker).CheckPullRequestAccess(context.Background(), "o/r"); err == nil || !strings.Contains(err.Error(), "o/r") {
		t.Errorf("err = %v, want it to name the repo", err)
	}
}

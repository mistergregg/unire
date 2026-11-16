package gitprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// conformCase describes a fake server for one provider type. Every provider
// must add one (see docs/adding-a-git-provider.md); the tests below run the
// same contract against all of them.
type conformCase struct {
	typ     string
	handler http.Handler // fake API; must accept only `token`
	token   string
	user    string // user name the fake returns
	query   string // a search that matches at least one repo
}

var conformCases []conformCase

func TestConformance(t *testing.T) {
	for _, c := range conformCases {
		t.Run(c.typ, func(t *testing.T) {
			srv := httptest.NewServer(c.handler)
			defer srv.Close()
			ctx := context.Background()

			p, err := New(Config{Type: c.typ, BaseURL: srv.URL, Token: c.token})
			if err != nil {
				t.Fatal(err)
			}
			if u, err := p.TestConnection(ctx); err != nil || u != c.user {
				t.Fatalf("TestConnection = %q, %v; want %q", u, err, c.user)
			}

			repos, err := p.ListRepos(ctx, c.query)
			if err != nil || len(repos) == 0 {
				t.Fatalf("ListRepos(%q) = %v, %v; want at least one repo", c.query, repos, err)
			}
			for _, r := range repos {
				if r.FullName == "" || r.CloneURL == "" {
					t.Errorf("repo missing FullName or CloneURL: %+v", r)
				}
			}
			if _, err := p.ListRepos(ctx, ""); err != nil {
				t.Errorf("ListRepos with empty query: %v", err)
			}

			bad, err := New(Config{Type: c.typ, BaseURL: srv.URL, Token: "wrong"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bad.TestConnection(ctx); err == nil || !strings.Contains(err.Error(), "rejected the token") {
				t.Errorf("a bad token should give a 'rejected the token' error, got %v", err)
			}
		})
	}
}

func TestEveryProviderHasConformanceCase(t *testing.T) {
	have := map[string]bool{}
	for _, c := range conformCases {
		have[c.typ] = true
	}
	for _, typ := range Types() {
		if !have[typ] {
			t.Errorf("provider %q has no conformance case; add one in its _test.go file", typ)
		}
	}
}

func TestInfosAreComplete(t *testing.T) {
	for _, i := range Infos() {
		if i.Type == "" || i.Label == "" || i.TokenHelp == "" {
			t.Errorf("incomplete Info: %+v", i)
		}
	}
}

func TestUnknownTypeAndMissingURL(t *testing.T) {
	if _, err := New(Config{Type: "nope"}); err == nil {
		t.Error("unknown type should fail")
	}
	if _, err := New(Config{Type: "gitea"}); err == nil {
		t.Error("gitea without a server URL should fail")
	}
}

// ---- helpers for fake servers ----

func requireHeader(name, want string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(name) != want {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jsonRoutes(routes map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	})
}

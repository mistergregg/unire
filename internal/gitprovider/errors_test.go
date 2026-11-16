package gitprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func giteaWith(t *testing.T, h http.HandlerFunc) Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(Config{Type: "gitea", BaseURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGiteaTokenWithoutUserScopeStillConnects(t *testing.T) {
	p := giteaWith(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/user":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message":"token does not have at least one of required scope(s): [read:user]"}`))
		case "/api/v1/repos/search":
			w.Write([]byte(`{"data":[]}`))
		}
	})
	user, err := p.TestConnection(context.Background())
	if err != nil || !strings.Contains(user, "without user permission") {
		t.Errorf("a repository-only token should connect: %q, %v", user, err)
	}
}

func TestRejectedTokenExplainsWhy(t *testing.T) {
	p := giteaWith(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"token does not have at least one of required scope(s): [read:repository]"}`))
	})
	_, err := p.TestConnection(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rejected the token (HTTP 403)") || !strings.Contains(err.Error(), "required scope(s)") {
		t.Errorf("the error should include the server's reason, got %v", err)
	}
}

func TestWebPageInsteadOfAPIAnswerPointsAtAProxy(t *testing.T) {
	p := giteaWith(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`<html><head><title>Attention Required! | Cloudflare</title></head><body>Sorry, you have been blocked</body></html>`))
	})
	_, err := p.TestConnection(context.Background())
	if err == nil || !strings.Contains(err.Error(), "proxy or firewall") {
		t.Errorf("an HTML 403 should point at a proxy or firewall, got %v", err)
	}
	if strings.Contains(err.Error(), "Cloudflare") || strings.Contains(err.Error(), "<html") {
		t.Errorf("the page itself must not be echoed: %v", err)
	}
}

func TestServerReasonIsShortenedAndCleaned(t *testing.T) {
	long := strings.Repeat("x", 500)
	p := giteaWith(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"line1\nline2 ` + long + `"}`))
	})
	_, err := p.TestConnection(context.Background())
	if err == nil || len(err.Error()) > 300 || strings.Contains(err.Error(), "\n") {
		t.Errorf("reason should be one short line, got %d chars: %v", len(err.Error()), err)
	}
}

func TestBothDeniedStillFailsEvenWithTheFallback(t *testing.T) {
	// A 403 that is not about scopes (for example a firewall) must not be
	// reported as a successful connection.
	p := giteaWith(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	if user, err := p.TestConnection(context.Background()); err == nil {
		t.Errorf("a blanket 403 connected as %q", user)
	}
}

package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type env struct {
	t    *testing.T
	base string
	c    *http.Client
	dir  string
	log  *bytes.Buffer // every response body seen, to check for leaks
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &env{t: t, base: ts.URL, c: &http.Client{Jar: jar}, dir: dir, log: &bytes.Buffer{}}
}

func (e *env) do(method, path string, body any, headers map[string]string) (int, string) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if headers == nil {
		req.Header.Set("X-Requested-With", "unire")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	e.log.Write(b)
	return resp.StatusCode, string(b)
}

func fakeGitea(t *testing.T, token string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token "+token {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/user":
			w.Write([]byte(`{"login":"greg"}`))
		case "/api/v1/repos/search":
			w.Write([]byte(`{"data":[{"full_name":"greg/api","clone_url":"https://g/greg/api.git","default_branch":"main"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFlow(t *testing.T) {
	e := newEnv(t)
	const secret = "super-secret-token-xyz"
	gitea := fakeGitea(t, secret)

	if code, _ := e.do("GET", "/api/servers", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list = %d, want 401", code)
	}
	if code, _ := e.do("POST", "/api/setup", map[string]string{"password": "short"}, nil); code != http.StatusBadRequest {
		t.Errorf("short password = %d, want 400", code)
	}
	if code, _ := e.do("POST", "/api/setup", map[string]string{"password": "correct horse battery"}, nil); code != http.StatusOK {
		t.Fatalf("setup = %d", code)
	}
	if code, _ := e.do("POST", "/api/setup", map[string]string{"password": "another password!"}, nil); code != http.StatusConflict {
		t.Errorf("second setup = %d, want 409", code)
	}

	// Add a server with a wrong token: refused, nothing saved.
	add := map[string]any{"name": "Home Gitea", "type": "gitea", "baseUrl": gitea.URL, "token": "wrong"}
	if code, _ := e.do("POST", "/api/servers", add, nil); code != http.StatusBadRequest {
		t.Errorf("wrong token add = %d, want 400", code)
	}
	add["token"] = secret
	code, body := e.do("POST", "/api/servers", add, nil)
	if code != http.StatusCreated {
		t.Fatalf("add = %d %s", code, body)
	}

	code, body = e.do("GET", "/api/servers", nil, nil)
	if code != 200 || !strings.Contains(body, `"tokenSet":true`) || !strings.Contains(body, `"id":"home-gitea"`) {
		t.Errorf("list = %d %s", code, body)
	}
	if code, body = e.do("GET", "/api/servers/home-gitea/repos?q=api", nil, nil); code != 200 || !strings.Contains(body, "greg/api") {
		t.Errorf("repos = %d %s", code, body)
	}
	if code, body = e.do("POST", "/api/servers/home-gitea/test", nil, nil); code != 200 || !strings.Contains(body, "greg") {
		t.Errorf("test = %d %s", code, body)
	}
	if code, _ = e.do("PUT", "/api/servers/home-gitea/token", map[string]string{"token": "also-wrong"}, nil); code != http.StatusBadRequest {
		t.Errorf("replace with bad token = %d, want 400", code)
	}

	// The token must never appear in any response or in a file as plain text.
	if strings.Contains(e.log.String(), secret) {
		t.Error("a response contained the token")
	}
	for _, f := range []string{"vault.json", "state.json"} {
		b, _ := os.ReadFile(filepath.Join(e.dir, f))
		if strings.Contains(string(b), secret) {
			t.Errorf("%s contains the token in clear text", f)
		}
	}

	if code, _ = e.do("DELETE", "/api/servers/home-gitea", nil, nil); code != 200 {
		t.Errorf("delete = %d", code)
	}
	if v, _ := New(e.dir); v != nil && v.vault.Has("git-server-home-gitea") {
		t.Error("token should be deleted with the server")
	}
}

func TestCSRFHeaderRequired(t *testing.T) {
	e := newEnv(t)
	code, _ := e.do("POST", "/api/setup", map[string]string{"password": "correct horse battery"}, map[string]string{})
	if code != http.StatusForbidden {
		t.Errorf("POST without X-Requested-With = %d, want 403", code)
	}
}

func TestLoginLockout(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/setup", map[string]string{"password": "correct horse battery"}, nil)
	e.do("POST", "/api/logout", nil, nil)
	for i := 0; i < maxFailures; i++ {
		if code, _ := e.do("POST", "/api/login", map[string]string{"password": "nope"}, nil); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, code)
		}
	}
	if code, _ := e.do("POST", "/api/login", map[string]string{"password": "correct horse battery"}, nil); code != http.StatusTooManyRequests {
		t.Errorf("after lockout = %d, want 429", code)
	}
}

func TestTypesListedForUI(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/setup", map[string]string{"password": "correct horse battery"}, nil)
	code, body := e.do("GET", "/api/types", nil, nil)
	if code != 200 || !strings.Contains(body, `"gitlab"`) || !strings.Contains(body, `"tokenHelp"`) {
		t.Errorf("types = %d %s", code, body)
	}
}

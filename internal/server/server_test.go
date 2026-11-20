package server

import (
	"archive/zip"
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

	"github.com/mistergregg/unire/internal/egress"
	"github.com/mistergregg/unire/internal/runner"
)

const goodPassword = "correct horse battery"

type env struct {
	t    *testing.T
	srv  *Server
	ts   *httptest.Server
	c    *http.Client
	dir  string
	seen *bytes.Buffer // every response body, to check for leaks
	box  *fakeBox      // the fake sandbox every session runs in
	info egress.Info   // network protection as the server sees it
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	jar, _ := cookiejar.New(nil)
	e := &env{t: t, dir: dir, c: &http.Client{Jar: jar}, seen: &bytes.Buffer{}, box: &fakeBox{},
		info: egress.Info{Available: true, Network: "unire-sandbox", ProxyURL: "http://unire-egress:3128"}}
	e.start()
	return e
}

// start (re)opens the server on the same data directory, like a restart.
func (e *env) start() {
	s, err := NewWithEgress(e.dir, func(Sandbox) runner.Runner { return fakeRunner{e.box} }, egress.NewProxy(), e.info)
	if err != nil {
		e.t.Fatal(err)
	}
	e.srv = s
	e.ts = httptest.NewServer(s.Handler())
	e.t.Cleanup(func() { e.ts.Close(); s.Close() })
}

func (e *env) restart() {
	e.ts.Close()
	e.srv.Close()
	e.start()
}

func (e *env) do(method, path string, body any, noCSRF ...bool) (int, string) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if len(noCSRF) == 0 {
		req.Header.Set("X-Requested-With", "unire")
	}
	resp, err := e.c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	e.seen.Write(b)
	return resp.StatusCode, string(b)
}

func (e *env) setup() {
	e.t.Helper()
	if code, body := e.do("POST", "/api/setup", map[string]string{"password": goodPassword}); code != 200 {
		e.t.Fatalf("setup = %d %s", code, body)
	}
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

	if code, _ := e.do("GET", "/api/servers", nil); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list = %d, want 401", code)
	}
	if code, _ := e.do("POST", "/api/setup", map[string]string{"password": "short"}); code != http.StatusBadRequest {
		t.Errorf("short password = %d, want 400", code)
	}
	e.setup()

	add := map[string]any{"name": "Home Gitea", "type": "gitea", "baseUrl": gitea.URL, "token": "wrong"}
	if code, _ := e.do("POST", "/api/servers", add); code != http.StatusBadRequest {
		t.Errorf("wrong token add = %d, want 400", code)
	}
	add["token"] = secret
	if code, body := e.do("POST", "/api/servers", add); code != http.StatusCreated {
		t.Fatalf("add = %d %s", code, body)
	}

	code, body := e.do("GET", "/api/servers", nil)
	if code != 200 || !strings.Contains(body, `"tokenSet":true`) || !strings.Contains(body, `"id":"home-gitea"`) {
		t.Errorf("list = %d %s", code, body)
	}
	if code, body = e.do("GET", "/api/servers/home-gitea/repos?q=api", nil); code != 200 || !strings.Contains(body, "greg/api") {
		t.Errorf("repos = %d %s", code, body)
	}
	if code, body = e.do("POST", "/api/servers/home-gitea/test", nil); code != 200 || !strings.Contains(body, "greg") {
		t.Errorf("test = %d %s", code, body)
	}
	if code, _ = e.do("PUT", "/api/servers/home-gitea/token", map[string]string{"token": "also-wrong"}); code != http.StatusBadRequest {
		t.Errorf("replace with bad token = %d, want 400", code)
	}

	// The token must never appear in a response, or in clear text on disk.
	if strings.Contains(e.seen.String(), secret) {
		t.Error("a response contained the token")
	}
	files, _ := filepath.Glob(filepath.Join(e.dir, "*"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), secret) {
			t.Errorf("%s contains the token in clear text", filepath.Base(f))
		}
	}

	if code, _ = e.do("DELETE", "/api/servers/home-gitea", nil); code != 200 {
		t.Errorf("delete = %d", code)
	}
	if e.srv.vault.Has("git-server-home-gitea") {
		t.Error("token should be deleted with the server")
	}
}

func TestEverythingSurvivesARestart(t *testing.T) {
	e := newEnv(t)
	gitea := fakeGitea(t, "tok")
	e.setup()
	e.do("POST", "/api/servers", map[string]any{"name": "G", "type": "gitea", "baseUrl": gitea.URL, "token": "tok"})

	e.restart() // same cookie jar: the browser keeps its cookie

	if code, body := e.do("GET", "/api/status", nil); code != 200 || !strings.Contains(body, `"loggedIn":true`) {
		t.Errorf("login lost on restart: %d %s", code, body)
	}
	if code, body := e.do("POST", "/api/servers/g/test", nil); code != 200 {
		t.Errorf("server or token lost on restart: %d %s", code, body)
	}
}

func TestOnlyOneOwner(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if code, _ := e.do("POST", "/api/setup", map[string]string{"password": "another password!"}); code != http.StatusConflict {
		t.Errorf("second setup = %d, want 409", code)
	}
	// Even after a restart, and even when logged out.
	e.restart()
	e.do("POST", "/api/logout", nil)
	if code, _ := e.do("POST", "/api/setup", map[string]string{"password": "another password!"}); code != http.StatusConflict {
		t.Errorf("setup after restart = %d, want 409", code)
	}
}

func TestDevicesAndRevoke(t *testing.T) {
	e := newEnv(t)
	e.setup()

	// A second device logs in.
	jar2, _ := cookiejar.New(nil)
	phone := &http.Client{Jar: jar2}
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/login", strings.NewReader(`{"password":"`+goodPassword+`"}`))
	req.Header.Set("X-Requested-With", "unire")
	req.Header.Set("User-Agent", "Phone Browser")
	if resp, err := phone.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("second login: %v %v", resp, err)
	}

	_, body := e.do("GET", "/api/logins", nil)
	var list []loginView
	json.Unmarshal([]byte(body), &list)
	if len(list) != 2 {
		t.Fatalf("want 2 logins, got %s", body)
	}
	var phoneID string
	currents := 0
	for _, l := range list {
		if l.Current {
			currents++
		}
		if l.UserAgent == "Phone Browser" {
			phoneID = l.ID
		}
	}
	if currents != 1 || phoneID == "" {
		t.Fatalf("expected one current login and a phone login, got %s", body)
	}

	if code, _ := e.do("DELETE", "/api/logins/"+phoneID, nil); code != 200 {
		t.Fatalf("revoke = %d", code)
	}
	areq, _ := http.NewRequest("GET", e.ts.URL+"/api/servers", nil)
	if resp, _ := phone.Do(areq); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked phone still logged in: %d", resp.StatusCode)
	}

	if code, _ := e.do("POST", "/api/logins/revoke-all", nil); code != 200 {
		t.Fatalf("revoke-all = %d", code)
	}
	if code, _ := e.do("GET", "/api/servers", nil); code != http.StatusUnauthorized {
		t.Errorf("after revoke-all = %d, want 401", code)
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	e.setup()
	jar2, _ := cookiejar.New(nil)
	other := &http.Client{Jar: jar2}
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/login", strings.NewReader(`{"password":"`+goodPassword+`"}`))
	req.Header.Set("X-Requested-With", "unire")
	other.Do(req)

	if code, _ := e.do("POST", "/api/password", map[string]string{"current": "wrong", "new": "brand new password"}); code != http.StatusUnauthorized {
		t.Errorf("wrong current = %d, want 401", code)
	}
	if code, _ := e.do("POST", "/api/password", map[string]string{"current": goodPassword, "new": "short"}); code != http.StatusBadRequest {
		t.Errorf("short new = %d, want 400", code)
	}
	if code, _ := e.do("POST", "/api/password", map[string]string{"current": goodPassword, "new": "brand new password"}); code != 200 {
		t.Fatalf("change = %d", code)
	}
	if code, _ := e.do("GET", "/api/servers", nil); code != 200 {
		t.Errorf("this device should stay logged in, got %d", code)
	}
	areq, _ := http.NewRequest("GET", e.ts.URL+"/api/servers", nil)
	if resp, _ := other.Do(areq); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("other device should be logged out, got %d", resp.StatusCode)
	}
	e.do("POST", "/api/logout", nil)
	if code, _ := e.do("POST", "/api/login", map[string]string{"password": goodPassword}); code != http.StatusUnauthorized {
		t.Errorf("old password still works: %d", code)
	}
	if code, _ := e.do("POST", "/api/login", map[string]string{"password": "brand new password"}); code != 200 {
		t.Errorf("new password rejected: %d", code)
	}
}

func TestBackupContainsDatabaseAndKey(t *testing.T) {
	e := newEnv(t)
	t.Setenv("UNIRE_MASTER_KEY", "") // key is in a file in this test
	e.setup()
	if code, _ := func() (int, string) {
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar}
		resp, _ := c.Get(e.ts.URL + "/api/backup")
		return resp.StatusCode, ""
	}(); code != http.StatusUnauthorized {
		t.Errorf("backup without login = %d, want 401", code)
	}

	resp, err := e.c.Get(e.ts.URL + "/api/backup")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("backup: %v %v", resp, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range zr.File {
		got[f.Name] = true
	}
	if !got["unire.db"] || !got["master.key"] || len(got) != 2 {
		t.Errorf("zip contents = %v", got)
	}
	left, _ := filepath.Glob(filepath.Join(e.dir, "backup-*"))
	if len(left) != 0 {
		t.Errorf("temporary backup files left behind: %v", left)
	}
}

func TestBackupContainsAttachments(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	code, sess := e.launch(base, map[string]any{
		"prompt": "inspect attached image",
		"images": []any{image("screen.png", pngData)},
	})
	if code != http.StatusCreated {
		t.Fatalf("launch = %d %v", code, sess)
	}
	id := sess["id"].(string)
	e.waitStatus(id, "done")

	resp, err := e.c.Get(e.ts.URL + "/api/backup")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("backup: %v %v", resp, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}

	var hasDB bool
	var attBytes []byte
	var attName string
	for _, f := range zr.File {
		if f.Name == "unire.db" {
			hasDB = true
		}
		if strings.HasPrefix(f.Name, "attachments/"+id+"/") {
			attName = f.Name
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open attachment in zip: %v", err)
			}
			attBytes, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("read attachment in zip: %v", err)
			}
		}
	}
	if !hasDB {
		t.Error("zip is missing unire.db")
	}
	if attName == "" {
		t.Fatalf("zip has no attachment for session %s", id)
	}
	if !bytes.Equal(attBytes, pngData) {
		t.Errorf("attachment %s has %d bytes, want %d", attName, len(attBytes), len(pngData))
	}
}

func TestBackupSkipsSymlinks(t *testing.T) {
	e := newEnv(t)
	e.setup()

	// Create a regular attachment, a symlink to a file, and a symlink to a directory.
	attDir := filepath.Join(e.dir, "attachments", "sess1")
	if err := os.MkdirAll(attDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attDir, "photo.png"), pngData, 0o600); err != nil {
		t.Fatal(err)
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.key")
	if err := os.WriteFile(outsideFile, []byte("sensitive-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(outsideDir, "nested")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("nested-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Symlink to a file inside the session folder
	if err := os.Symlink(outsideFile, filepath.Join(attDir, "link.png")); err != nil {
		t.Fatal(err)
	}
	// Symlink to a directory inside the session folder
	if err := os.Symlink(subDir, filepath.Join(attDir, "symlink_dir")); err != nil {
		t.Fatal(err)
	}
	// Symlink to a directory directly under attachments folder
	if err := os.Symlink(outsideDir, filepath.Join(e.dir, "attachments", "outside")); err != nil {
		t.Fatal(err)
	}

	resp, err := e.c.Get(e.ts.URL + "/api/backup")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("backup: %v %v", resp, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}

	var hasRegular bool
	for _, f := range zr.File {
		if f.Name == "attachments/sess1/photo.png" {
			hasRegular = true
		}
		if strings.Contains(f.Name, "link.png") || strings.Contains(f.Name, "symlink_dir") ||
			strings.Contains(f.Name, "outside") || strings.Contains(f.Name, "secret.key") ||
			strings.Contains(f.Name, "nested") {
			t.Errorf("backup must not follow or include symlinks: found %s", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(rc)
		rc.Close()
		if bytes.Contains(content, []byte("sensitive-secret")) || bytes.Contains(content, []byte("nested-secret")) {
			t.Errorf("backup must not contain content from symlinked files: found in %s", f.Name)
		}
	}
	if !hasRegular {
		t.Error("backup missing attachments/sess1/photo.png")
	}
}

func TestBackupWithoutAttachmentsDir(t *testing.T) {
	e := newEnv(t)
	e.setup()

	// Ensure the attachments folder does not exist.
	attDir := filepath.Join(e.dir, "attachments")
	_ = os.RemoveAll(attDir)
	if _, err := os.Stat(attDir); !os.IsNotExist(err) {
		t.Fatalf("expected attachments directory not to exist: %v", err)
	}

	resp, err := e.c.Get(e.ts.URL + "/api/backup")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("backup without attachments dir: %v %v", resp, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}

	var hasDB bool
	for _, f := range zr.File {
		if f.Name == "unire.db" {
			hasDB = true
		}
		if strings.HasPrefix(f.Name, "attachments/") {
			t.Errorf("unexpected attachment in backup: %s", f.Name)
		}
	}
	if !hasDB {
		t.Error("backup is missing unire.db")
	}
}

func TestCSRFHeaderRequired(t *testing.T) {
	e := newEnv(t)
	code, _ := e.do("POST", "/api/setup", map[string]string{"password": goodPassword}, true)
	if code != http.StatusForbidden {
		t.Errorf("POST without X-Requested-With = %d, want 403", code)
	}
}

func TestLoginLockout(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.do("POST", "/api/logout", nil)
	for i := 0; i < maxFailures; i++ {
		if code, _ := e.do("POST", "/api/login", map[string]string{"password": "nope"}); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, code)
		}
	}
	if code, _ := e.do("POST", "/api/login", map[string]string{"password": goodPassword}); code != http.StatusTooManyRequests {
		t.Errorf("after lockout = %d, want 429", code)
	}
}

func TestTypesListedForUI(t *testing.T) {
	e := newEnv(t)
	e.setup()
	code, body := e.do("GET", "/api/types", nil)
	if code != 200 || !strings.Contains(body, `"gitlab"`) || !strings.Contains(body, `"tokenHelp"`) {
		t.Errorf("types = %d %s", code, body)
	}
}

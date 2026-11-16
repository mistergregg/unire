package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func TestMigrationsAreIdempotent(t *testing.T) {
	s, dir := open(t)
	if err := s.SetSetting("k", "v"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(dir) // runs migrate again on an existing database
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, ok, _ := s2.Setting("k"); !ok || v != "v" {
		t.Errorf("data lost after reopen: %q %v", v, ok)
	}
	var n int
	s2.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	if n != len(migrations) {
		t.Errorf("schema_migrations has %d rows, want %d", n, len(migrations))
	}
}

func TestNewerDatabaseIsRefused(t *testing.T) {
	s, dir := open(t)
	s.db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (999, 0)`)
	s.Close()
	if _, err := Open(dir); err == nil {
		t.Error("a database from a newer version should be refused, not downgraded")
	}
}

func TestSetSettingIfUnsetIsOnce(t *testing.T) {
	s, _ := open(t)
	if ok, err := s.SetSettingIfUnset("admin", "a"); !ok || err != nil {
		t.Fatalf("first = %v, %v", ok, err)
	}
	if ok, _ := s.SetSettingIfUnset("admin", "b"); ok {
		t.Error("second call must not overwrite")
	}
	if v, _, _ := s.Setting("admin"); v != "a" {
		t.Errorf("value = %q", v)
	}
}

func TestGitServers(t *testing.T) {
	s, _ := open(t)
	a, err := s.AddGitServer(GitServer{Name: "Home Gitea", Type: "gitea", BaseURL: "https://g", InsecureSkipVerify: true})
	if err != nil || a.ID != "home-gitea" || a.TokenSecret != "git-server-home-gitea" {
		t.Fatalf("add = %+v, %v", a, err)
	}
	b, _ := s.AddGitServer(GitServer{Name: "Home Gitea", Type: "gitea"})
	if b.ID != "home-gitea-2" {
		t.Errorf("duplicate name got ID %q", b.ID)
	}
	list, _ := s.GitServers()
	if len(list) != 2 || !list[0].InsecureSkipVerify || list[1].InsecureSkipVerify {
		t.Errorf("list = %+v", list)
	}
	if _, ok, _ := s.RemoveGitServer("home-gitea"); !ok {
		t.Error("remove failed")
	}
	if _, ok, _ := s.GitServer("home-gitea"); ok {
		t.Error("server still there after remove")
	}
	if _, ok, _ := s.RemoveGitServer("missing"); ok {
		t.Error("removing a missing server should report false")
	}
}

func TestLoginLifecycle(t *testing.T) {
	s, _ := open(t)
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	tok, err := s.CreateLogin("Firefox", "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	// Only a hash is stored.
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM login_sessions WHERE id = ?`, tok).Scan(&n)
	if n != 0 {
		t.Error("the raw token is stored in the database")
	}
	id, ok, _ := s.CheckLogin(tok)
	if !ok || id != hashToken(tok) {
		t.Fatalf("CheckLogin = %q, %v", id, ok)
	}
	if _, ok, _ := s.CheckLogin("not-a-token"); ok {
		t.Error("unknown token accepted")
	}

	// Sliding expiry: use it 20 days later, it lasts until 30 days after that.
	clock = clock.Add(20 * 24 * time.Hour)
	if _, ok, _ := s.CheckLogin(tok); !ok {
		t.Fatal("login should still be valid at 20 days")
	}
	clock = clock.Add(25 * 24 * time.Hour) // 45 days after creation, 25 after last use
	if _, ok, _ := s.CheckLogin(tok); !ok {
		t.Error("login should have slid forward")
	}
	clock = clock.Add(31 * 24 * time.Hour)
	if _, ok, _ := s.CheckLogin(tok); ok {
		t.Error("login should expire after 30 unused days")
	}
}

func TestRevoke(t *testing.T) {
	s, _ := open(t)
	t1, _ := s.CreateLogin("a", "1")
	t2, _ := s.CreateLogin("b", "2")
	id1, _, _ := s.CheckLogin(t1)
	if list, _ := s.Logins(); len(list) != 2 {
		t.Fatalf("Logins = %d", len(list))
	}
	if ok, _ := s.RevokeLogin(id1); !ok {
		t.Error("revoke failed")
	}
	if _, ok, _ := s.CheckLogin(t1); ok {
		t.Error("revoked login still works")
	}
	id2, _, _ := s.CheckLogin(t2)
	_ = s.RevokeAllLogins(id2)
	if _, ok, _ := s.CheckLogin(t2); !ok {
		t.Error("the kept login should survive")
	}
	_ = s.RevokeAllLogins("")
	if _, ok, _ := s.CheckLogin(t2); ok {
		t.Error("revoke all should end every login")
	}
}

func TestSnapshot(t *testing.T) {
	s, dir := open(t)
	s.SetSetting("k", "v")
	out := filepath.Join(dir, "copy.db")
	if err := s.Snapshot(out); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		t.Fatalf("snapshot missing or empty: %v", err)
	}
}

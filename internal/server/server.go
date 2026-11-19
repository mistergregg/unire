// Package server is the Unire controller: the owner's login, the vault, git
// server settings and the web UI. No API response ever contains a secret.
//
// Unire has exactly one account, the owner's. There is no way to add users:
// agent logins and tokens belong to one person, and a second person runs
// their own instance.
package server

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mistergregg/unire/internal/egress"
	"github.com/mistergregg/unire/internal/gitprovider"
	"github.com/mistergregg/unire/internal/pulls"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/safe"
	"github.com/mistergregg/unire/internal/sessions"
	"github.com/mistergregg/unire/internal/store"
	"github.com/mistergregg/unire/internal/vault"
)

//go:embed web
var webFS embed.FS

const (
	cookieName   = "unire_session"
	minPassword  = 10
	maxFailures  = 5
	lockoutTime  = time.Minute
	adminHashKey = "admin_hash"
)

// Server holds the controller's state.
type Server struct {
	dataDir string
	store   *store.Store
	vault   *vault.Vault

	sessions      *sessions.Manager
	pulls         *pulls.Service
	catalog       catalogCache
	stopCleanup   chan struct{}
	stopOnce      sync.Once
	probe         healthProbe // how the health page asks Docker about the host
	cleanup       lastCleanup
	runnerFactory RunnerFactory
	egressProxy   *egress.Proxy
	egressInfo    egress.Info
	terminalFor   TerminalFactory
	termBusy      atomic.Bool // one sign-in terminal at a time
	signins       statusCache
	transcripts   transcripts

	mu       sync.Mutex
	failures map[string]failure
}

type failure struct {
	count int
	until time.Time
}

// New opens the data directory (creating the database and key on first run)
// and runs sessions in Docker.
func New(dataDir string) (*Server, error) {
	// Containers from a previous run (a crash, a kill, an update) belong to
	// sessions that no longer exist.
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if n, err := runner.CleanupOrphans(cctx); n > 0 {
		fmt.Fprintf(os.Stderr, "removed %d container(s) left behind by an earlier run\n", n)
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "could not check for leftover containers (is Docker reachable?): %v\n", err)
	}
	cancel()
	proxy := egress.NewProxy()
	s, err := NewWithEgress(dataDir, dockerRunner, proxy, egress.Setup(context.Background(), proxy))
	if err == nil {
		// Fetch a newer sandbox image now, so the first session or sign-in after
		// an update does not wait for the download.
		go runner.Refresh(context.Background(), s.sandbox().Image) // its own goroutine, so it may wait for the download
	}
	return s, err
}

// NewWith is New with a custom way to build sandboxes and no network
// protection (used by tests).
func NewWith(dataDir string, rf RunnerFactory) (*Server, error) {
	return NewWithEgress(dataDir, rf, egress.NewProxy(), egress.Info{Reason: "not configured"})
}

// NewWithEgress is NewWith with network protection supplied by the caller.
func NewWithEgress(dataDir string, rf RunnerFactory, proxy *egress.Proxy, info egress.Info) (*Server, error) {
	st, err := store.Open(dataDir)
	if err != nil {
		return nil, err
	}
	v, err := vault.Open(st.DB(), dataDir)
	if err != nil {
		st.Close()
		return nil, err
	}
	s := &Server{dataDir: dataDir, store: st, vault: v, runnerFactory: rf, egressProxy: proxy, egressInfo: info,
		terminalFor: dockerTerminal, signins: statusCache{m: map[string]statusEntry{}}, failures: map[string]failure{}}
	s.pulls = &pulls.Service{Store: st, Provider: s.provider}
	s.stopCleanup = make(chan struct{})
	s.probe = dockerProbe{}
	safe.Go("cleanup", func() { s.runCleanup(s.stopCleanup) })
	s.sessions, err = sessions.New(st, s.sessionConfig)
	if err != nil {
		st.Close()
		return nil, err
	}
	safe.Go("automatic fix", func() { s.runAutoFix(s.stopCleanup) })
	return s, nil
}

// Close stops running sessions and releases the database.
func (s *Server) Close() error {
	s.stopOnce.Do(func() { close(s.stopCleanup) })
	s.sessions.Shutdown()
	return s.store.Close()
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)

	mux.HandleFunc("GET /api/logins", s.auth(s.listLogins))
	mux.HandleFunc("DELETE /api/logins/{id}", s.auth(s.revokeLogin))
	mux.HandleFunc("POST /api/logins/revoke-all", s.auth(s.revokeAll))
	mux.HandleFunc("POST /api/password", s.auth(s.changePassword))
	mux.HandleFunc("GET /api/backup", s.auth(s.backup))

	mux.HandleFunc("GET /api/agents", s.auth(s.listAgents))
	mux.HandleFunc("POST /api/agents/recheck", s.auth(s.recheckAgents))
	mux.HandleFunc("GET /api/agents/{id}/last-signin", s.auth(s.lastSignin))
	mux.HandleFunc("POST /api/agents/{id}/inspect", s.auth(s.agentTool(false)))
	mux.HandleFunc("POST /api/agents/{id}/reset", s.auth(s.agentTool(true)))
	mux.HandleFunc("GET /api/agents/{id}/login", s.auth(s.loginTerminal))
	mux.HandleFunc("GET /api/sandbox", s.auth(s.getSandbox))
	mux.HandleFunc("PUT /api/sandbox", s.auth(s.putSandbox))
	mux.HandleFunc("GET /api/memory", s.auth(s.listMemory))
	mux.HandleFunc("GET /api/memory/repo", s.auth(s.getRepoMemory))
	mux.HandleFunc("PUT /api/memory/repo", s.auth(s.putRepoMemory))
	mux.HandleFunc("POST /api/memory/repo/restore", s.auth(s.restoreRepoMemory))
	mux.HandleFunc("DELETE /api/memory/repo", s.auth(s.deleteRepoMemory))
	mux.HandleFunc("GET /api/memory/search", s.auth(s.searchMemory))
	mux.HandleFunc("GET /api/memory/summaries/{id}", s.auth(s.getSummary))
	mux.HandleFunc("DELETE /api/memory/summaries/{id}", s.auth(s.deleteSummary))
	mux.HandleFunc("GET /api/network", s.auth(s.getNetwork))
	mux.HandleFunc("PUT /api/network", s.auth(s.putNetwork))
	mux.HandleFunc("POST /api/network/allow", s.auth(s.allowAddress))
	mux.HandleFunc("GET /api/workspaces", s.auth(s.listWorkspaces))
	mux.HandleFunc("POST /api/workspaces", s.auth(s.saveWorkspace))
	mux.HandleFunc("DELETE /api/workspaces/{id}", s.auth(s.deleteWorkspace))
	mux.HandleFunc("POST /api/repos/suggest", s.auth(s.suggestRepos))
	mux.HandleFunc("GET /api/memory/stats", s.auth(s.memoryStats))
	mux.HandleFunc("GET /api/health", s.auth(s.getHealth))
	mux.HandleFunc("GET /api/limits", s.auth(s.getLimits))
	mux.HandleFunc("PUT /api/limits", s.auth(s.putLimits))
	mux.HandleFunc("GET /api/sessions", s.auth(s.listSessions))
	mux.HandleFunc("POST /api/sessions", s.auth(s.startSession))
	mux.HandleFunc("GET /api/sessions/{id}", s.auth(s.getSession))
	mux.HandleFunc("GET /api/sessions/{id}/logs", s.auth(s.sessionLogs))
	mux.HandleFunc("GET /api/sessions/{id}/thread", s.auth(s.getThread))
	mux.HandleFunc("POST /api/sessions/{id}/reply", s.auth(s.replyToSession))
	mux.HandleFunc("GET /api/sessions/{id}/fork", s.auth(s.getFork))
	mux.HandleFunc("GET /api/sessions/{id}/links", s.auth(s.getLinks))
	mux.HandleFunc("POST /api/sessions/{id}/parent", s.auth(s.setParent))
	mux.HandleFunc("GET /api/sessions/{id}/pulls", s.auth(s.getPulls))
	mux.HandleFunc("POST /api/sessions/{id}/pulls", s.auth(s.openPull))
	mux.HandleFunc("POST /api/sessions/{id}/pulls/state", s.auth(s.setPullOpen))
	mux.HandleFunc("POST /api/sessions/{id}/pulls/update", s.auth(s.updatePull))
	mux.HandleFunc("POST /api/sessions/{id}/autofix", s.auth(s.setAutoFix))
	mux.HandleFunc("GET /api/sessions/{id}/pulls/failure", s.auth(s.getPullFailure))
	mux.HandleFunc("POST /api/sessions/{id}/answer", s.auth(s.answerQuestion))
	mux.HandleFunc("POST /api/sessions/{id}/steer", s.auth(s.steerSession))
	mux.HandleFunc("POST /api/sessions/{id}/steer/{msg}/now", s.auth(s.promoteSteer))
	mux.HandleFunc("POST /api/sessions/{id}/stop", s.auth(s.stopSession))
	mux.HandleFunc("DELETE /api/sessions/{id}", s.auth(s.deleteSession))

	mux.HandleFunc("GET /api/types", s.auth(s.types))
	mux.HandleFunc("GET /api/servers", s.auth(s.listServers))
	mux.HandleFunc("POST /api/servers", s.auth(s.addServer))
	mux.HandleFunc("POST /api/servers/{id}/test", s.auth(s.testServer))
	mux.HandleFunc("POST /api/servers/{id}/test-pulls", s.auth(s.testPullRequestAccess))
	mux.HandleFunc("PUT /api/servers/{id}/token", s.auth(s.replaceToken))
	mux.HandleFunc("DELETE /api/servers/{id}", s.auth(s.deleteServer))
	mux.HandleFunc("GET /api/servers/{id}/repos", s.auth(s.listRepos))

	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return securityHeaders(s.csrf(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// csrf rejects state-changing requests that lack our custom header. A
// cross-site form or fetch cannot set it without a CORS preflight we never
// answer; the session cookie is also SameSite=Strict.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Requested-With") != "unire" {
			writeErr(w, http.StatusForbidden, "missing X-Requested-With header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- logins ----

func (s *Server) currentLogin(r *http.Request) (id string, token string, ok bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", "", false
	}
	id, ok, err = s.store.CheckLogin(c.Value)
	if err != nil || !ok {
		return "", "", false
	}
	return id, c.Value, true
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := s.currentLogin(r); !ok {
			writeErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		h(w, r)
	}
}

func setCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge: int(store.LoginTTL.Seconds()),
	})
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
}

func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) error {
	tok, err := s.store.CreateLogin(r.UserAgent(), clientIP(r))
	if err != nil {
		return err
	}
	setCookie(w, r, tok)
	return nil
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// checkPassword verifies the owner's password with a per-IP lockout.
func (s *Server) checkPassword(r *http.Request, pw string) (ok, lockedOut bool) {
	ip := clientIP(r)
	s.mu.Lock()
	f := s.failures[ip]
	if time.Now().Before(f.until) {
		s.mu.Unlock()
		return false, true
	}
	s.mu.Unlock()

	hash, set, err := s.store.Setting(adminHashKey)
	ok = err == nil && set && bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil

	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		delete(s.failures, ip)
		return true, false
	}
	f.count++
	if f.count >= maxFailures {
		f = failure{until: time.Now().Add(lockoutTime)}
	}
	s.failures[ip] = f
	return false, false
}

// ---- JSON helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func serverError(w http.ResponseWriter) {
	writeErr(w, http.StatusInternalServerError, "something went wrong saving that")
}

// ---- auth endpoints ----

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	_, set, _ := s.store.Setting(adminHashKey)
	_, tok, in := s.currentLogin(r)
	if in {
		setCookie(w, r, tok) // keeps the cookie's lifetime sliding with the login
	}
	writeJSON(w, http.StatusOK, map[string]bool{"setupDone": set, "loggedIn": in})
}

func hashPassword(pw string) (string, error) {
	if len(pw) < minPassword {
		return "", errors.New("password must be at least 10 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", errors.New("password is too long")
	}
	return string(h), nil
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var req struct{ Password string }
	if !readJSON(w, r, &req) {
		return
	}
	h, err := hashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Atomic: only the first caller can ever set the owner's password.
	first, err := s.store.SetSettingIfUnset(adminHashKey, h)
	if err != nil {
		serverError(w)
		return
	}
	if !first {
		writeErr(w, http.StatusConflict, "already set up")
		return
	}
	if err := s.startLogin(w, r); err != nil {
		serverError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct{ Password string }
	if !readJSON(w, r, &req) {
		return
	}
	ok, locked := s.checkPassword(r, req.Password)
	switch {
	case locked:
		writeErr(w, http.StatusTooManyRequests, "too many attempts, wait a minute")
	case !ok:
		writeErr(w, http.StatusUnauthorized, "wrong password")
	default:
		if err := s.startLogin(w, r); err != nil {
			serverError(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if id, _, ok := s.currentLogin(r); ok {
		_, _ = s.store.RevokeLogin(id)
	}
	clearCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type loginView struct {
	ID        string `json:"id"`
	UserAgent string `json:"userAgent"`
	IP        string `json:"ip"`
	CreatedAt int64  `json:"createdAt"`
	LastSeen  int64  `json:"lastSeen"`
	Current   bool   `json:"current"`
}

func (s *Server) listLogins(w http.ResponseWriter, r *http.Request) {
	cur, _, _ := s.currentLogin(r)
	logins, err := s.store.Logins()
	if err != nil {
		serverError(w)
		return
	}
	out := []loginView{}
	for _, l := range logins {
		out = append(out, loginView{l.ID, l.UserAgent, l.IP, l.CreatedAt.Unix(), l.LastSeen.Unix(), l.ID == cur})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) revokeLogin(w http.ResponseWriter, r *http.Request) {
	cur, _, _ := s.currentLogin(r)
	id := r.PathValue("id")
	ok, err := s.store.RevokeLogin(id)
	switch {
	case err != nil:
		serverError(w)
	case !ok:
		writeErr(w, http.StatusNotFound, "no such login")
	default:
		if id == cur {
			clearCookie(w)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) revokeAll(w http.ResponseWriter, _ *http.Request) {
	if err := s.store.RevokeAllLogins(""); err != nil {
		serverError(w)
		return
	}
	clearCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct{ Current, New string }
	if !readJSON(w, r, &req) {
		return
	}
	ok, locked := s.checkPassword(r, req.Current)
	if locked {
		writeErr(w, http.StatusTooManyRequests, "too many attempts, wait a minute")
		return
	}
	if !ok {
		writeErr(w, http.StatusUnauthorized, "current password is wrong")
		return
	}
	h, err := hashPassword(req.New)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetSetting(adminHashKey, h); err != nil {
		serverError(w)
		return
	}
	// A new password ends every other login, in case one of them was not yours.
	cur, _, _ := s.currentLogin(r)
	if err := s.store.RevokeAllLogins(cur); err != nil {
		serverError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// backup streams a zip with a consistent copy of the database and, when the
// master key is stored in a file, the key. The key is what decrypts the
// vault, so the download is as sensitive as the vault itself.
func (s *Server) backup(w http.ResponseWriter, _ *http.Request) {
	rb := make([]byte, 6)
	_, _ = rand.Read(rb)
	tmp := filepath.Join(s.dataDir, "backup-"+hex.EncodeToString(rb)+".tmp")
	defer os.Remove(tmp)
	if err := s.store.Snapshot(tmp); err != nil {
		serverError(w)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="unire-backup-`+time.Now().Format("2006-01-02")+`.zip"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	add := func(name, path string) {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		if fw, err := zw.Create(name); err == nil {
			_, _ = io.Copy(fw, f)
		}
	}
	add(store.DBFileName, tmp)
	if vault.KeyInFile() {
		add(vault.KeyFileName, filepath.Join(s.dataDir, vault.KeyFileName))
	}
}

// ---- git servers ----

type serverView struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Type               string `json:"type"`
	BaseURL            string `json:"baseUrl"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
	TokenSet           bool   `json:"tokenSet"` // never the token itself
}

func (s *Server) view(g store.GitServer) serverView {
	return serverView{g.ID, g.Name, g.Type, g.BaseURL, g.InsecureSkipVerify, s.vault.Has(g.TokenSecret)}
}

func (s *Server) types(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, gitprovider.Infos())
}

func (s *Server) listServers(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.GitServers()
	if err != nil {
		serverError(w)
		return
	}
	out := []serverView{}
	for _, g := range list {
		out = append(out, s.view(g))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) provider(g store.GitServer) (gitprovider.Provider, error) {
	tok, err := s.vault.Get(g.TokenSecret)
	if err != nil {
		return nil, errors.New("no token saved for this server")
	}
	return gitprovider.New(gitprovider.Config{
		Type: g.Type, BaseURL: g.BaseURL, Token: tok, InsecureSkipVerify: g.InsecureSkipVerify,
	})
}

func testWith(ctx context.Context, p gitprovider.Provider) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	return p.TestConnection(ctx)
}

func validBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (s *Server) addServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name, Type, BaseURL, Token string
		InsecureSkipVerify         bool
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	switch {
	case req.Name == "" || len(req.Name) > 60:
		writeErr(w, http.StatusBadRequest, "name is required (60 characters max)")
		return
	case req.Token == "":
		writeErr(w, http.StatusBadRequest, "token is required")
		return
	case req.BaseURL != "" && !validBaseURL(req.BaseURL):
		writeErr(w, http.StatusBadRequest, "base URL must start with http:// or https://")
		return
	}
	cfg := gitprovider.Config{Type: req.Type, BaseURL: req.BaseURL, Token: req.Token, InsecureSkipVerify: req.InsecureSkipVerify}
	p, err := gitprovider.New(cfg)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := testWith(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "connection test failed: "+err.Error())
		return
	}
	g, err := s.store.AddGitServer(store.GitServer{Name: req.Name, Type: req.Type, BaseURL: req.BaseURL, InsecureSkipVerify: req.InsecureSkipVerify})
	if err != nil {
		serverError(w)
		return
	}
	if err := s.vault.Set(g.TokenSecret, req.Token); err != nil {
		_, _, _ = s.store.RemoveGitServer(g.ID)
		serverError(w)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"server": s.view(g), "user": user})
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (store.GitServer, bool) {
	g, ok, err := s.store.GitServer(r.PathValue("id"))
	if err != nil {
		serverError(w)
		return g, false
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such server")
	}
	return g, ok
}

func (s *Server) testServer(w http.ResponseWriter, r *http.Request) {
	g, ok := s.lookup(w, r)
	if !ok {
		return
	}
	p, err := s.provider(g)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := testWith(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user": user})
}

func (s *Server) replaceToken(w http.ResponseWriter, r *http.Request) {
	g, ok := s.lookup(w, r)
	if !ok {
		return
	}
	var req struct{ Token string }
	if !readJSON(w, r, &req) {
		return
	}
	if req.Token == "" {
		writeErr(w, http.StatusBadRequest, "token is required")
		return
	}
	p, err := gitprovider.New(gitprovider.Config{Type: g.Type, BaseURL: g.BaseURL, Token: req.Token, InsecureSkipVerify: g.InsecureSkipVerify})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := testWith(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "connection test failed: "+err.Error())
		return
	}
	if err := s.vault.Set(g.TokenSecret, req.Token); err != nil {
		serverError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user": user})
}

func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request) {
	g, removed, err := s.store.RemoveGitServer(r.PathValue("id"))
	if err != nil {
		serverError(w)
		return
	}
	if !removed {
		writeErr(w, http.StatusNotFound, "no such server")
		return
	}
	_ = s.vault.Delete(g.TokenSecret)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	g, ok := s.lookup(w, r)
	if !ok {
		return
	}
	p, err := s.provider(g)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	repos, err := p.ListRepos(ctx, r.URL.Query().Get("q"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if repos == nil {
		repos = []gitprovider.Repo{}
	}
	writeJSON(w, http.StatusOK, repos)
}

// Package server is the Unire controller: admin login, the vault, git server
// settings and the web UI. No API response ever contains a secret value.
package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mistergregg/unire/internal/gitprovider"
	"github.com/mistergregg/unire/internal/vault"
)

//go:embed web
var webFS embed.FS

const (
	cookieName  = "unire_session"
	sessionTTL  = 12 * time.Hour
	minPassword = 10
	maxFailures = 5
	lockoutTime = time.Minute
)

// Server holds the controller's state.
type Server struct {
	vault *vault.Vault
	state *state

	setupMu  sync.Mutex // makes first-time setup happen once
	mu       sync.Mutex
	sessions map[string]time.Time
	failures map[string]failure
}

type failure struct {
	count int
	until time.Time
}

// New opens the data directory and returns a Server.
func New(dataDir string) (*Server, error) {
	v, err := vault.Open(dataDir)
	if err != nil {
		return nil, err
	}
	st, err := openState(dataDir)
	if err != nil {
		return nil, err
	}
	return &Server{vault: v, state: st, sessions: map[string]time.Time{}, failures: map[string]failure{}}, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/types", s.auth(s.types))
	mux.HandleFunc("GET /api/servers", s.auth(s.listServers))
	mux.HandleFunc("POST /api/servers", s.auth(s.addServer))
	mux.HandleFunc("POST /api/servers/{id}/test", s.auth(s.testServer))
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
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
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

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.loggedIn(r) {
			writeErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		h(w, r)
	}
}

func (s *Server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[c.Value]
	if !ok || time.Now().After(exp) {
		delete(s.sessions, c.Value)
		return false
	}
	return true
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[tok] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge: int(sessionTTL.Seconds()),
	})
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
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

// ---- auth endpoints ----

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"setupDone": s.state.adminHash() != "",
		"loggedIn":  s.loggedIn(r),
	})
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var req struct{ Password string }
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Password) < minPassword {
		writeErr(w, http.StatusBadRequest, "password must be at least 10 characters")
		return
	}
	h, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "password is too long")
		return
	}
	// Setting the first password is allowed once; a second attempt is refused.
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.state.adminHash() != "" {
		writeErr(w, http.StatusConflict, "already set up")
		return
	}
	if err := s.state.setAdminHash(string(h)); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save")
		return
	}
	s.startSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct{ Password string }
	if !readJSON(w, r, &req) {
		return
	}
	ip := clientIP(r)
	s.mu.Lock()
	f := s.failures[ip]
	locked := time.Now().Before(f.until)
	s.mu.Unlock()
	if locked {
		writeErr(w, http.StatusTooManyRequests, "too many attempts, wait a minute")
		return
	}
	hash := s.state.adminHash()
	ok := hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) == nil
	if !ok {
		s.mu.Lock()
		f.count++
		if f.count >= maxFailures {
			f = failure{until: time.Now().Add(lockoutTime)}
		}
		s.failures[ip] = f
		s.mu.Unlock()
		writeErr(w, http.StatusUnauthorized, "wrong password")
		return
	}
	s.mu.Lock()
	delete(s.failures, ip)
	s.mu.Unlock()
	s.startSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

func (s *Server) view(g GitServer) serverView {
	return serverView{g.ID, g.Name, g.Type, g.BaseURL, g.InsecureSkipVerify, s.vault.Has(g.TokenSecret)}
}

func (s *Server) types(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, gitprovider.Infos())
}

func (s *Server) listServers(w http.ResponseWriter, _ *http.Request) {
	out := []serverView{}
	for _, g := range s.state.servers() {
		out = append(out, s.view(g))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) provider(g GitServer) (gitprovider.Provider, error) {
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
	g, err := s.state.addServer(GitServer{Name: req.Name, Type: req.Type, BaseURL: req.BaseURL, InsecureSkipVerify: req.InsecureSkipVerify})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save server")
		return
	}
	if err := s.vault.Set(g.TokenSecret, req.Token); err != nil {
		_, _, _ = s.state.removeServer(g.ID)
		writeErr(w, http.StatusInternalServerError, "could not save token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"server": s.view(g), "user": user})
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (GitServer, bool) {
	g, ok := s.state.server(r.PathValue("id"))
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
		writeErr(w, http.StatusInternalServerError, "could not save token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user": user})
}

func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request) {
	g, removed, err := s.state.removeServer(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save")
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

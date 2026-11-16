package server

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/sessions"
	"github.com/mistergregg/unire/internal/store"
)

// ---- sandbox settings ----

// Sandbox is how sessions are run. These values end up as arguments to
// `docker run`, so they are validated before they are saved.
type Sandbox struct {
	Image       string `json:"image"`
	CredsVolume string `json:"credsVolume"`
	Memory      string `json:"memory"`
	CPUs        string `json:"cpus"`
}

// RunnerFactory builds the runner for a sandbox setting. Tests replace it.
type RunnerFactory func(Sandbox) runner.Runner

func dockerRunner(sb Sandbox) runner.Runner {
	return runner.Docker{Image: sb.Image, CredsVolume: sb.CredsVolume}
}

var (
	dockerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,127}$`)
	memoryRe   = regexp.MustCompile(`^[1-9][0-9]*[kKmMgG]?$`)
	cpusRe     = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	branchRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
)

func (sb Sandbox) validate() error {
	switch {
	case !dockerName.MatchString(sb.Image):
		return fmt.Errorf("image name looks invalid")
	case !dockerName.MatchString(sb.CredsVolume):
		return fmt.Errorf("credentials volume name looks invalid")
	case sb.Memory != "" && !memoryRe.MatchString(sb.Memory):
		return fmt.Errorf("memory limit should look like 4g or 512m")
	case sb.CPUs != "" && !cpusRe.MatchString(sb.CPUs):
		return fmt.Errorf("CPU limit should be a number like 2 or 1.5")
	}
	return nil
}

func (s *Server) sandbox() Sandbox {
	get := func(key, def string) string {
		if v, ok, _ := s.store.Setting(key); ok {
			return v
		}
		return def
	}
	return Sandbox{
		Image:       get("sandbox_image", "unire-sandbox"),
		CredsVolume: get("sandbox_creds_volume", "unire-creds-claude"),
		Memory:      get("sandbox_memory", ""),
		CPUs:        get("sandbox_cpus", ""),
	}
}

func (s *Server) sessionConfig() sessions.Config {
	sb := s.sandbox()
	return sessions.Config{Runner: s.runnerFactory(sb), Memory: sb.Memory, CPUs: sb.CPUs}
}

func (s *Server) getSandbox(w http.ResponseWriter, _ *http.Request) {
	sb := s.sandbox()
	writeJSON(w, http.StatusOK, map[string]any{
		"sandbox": sb,
		// Run once to log the agent in. The login is saved in the volume.
		"loginCommand": "docker run -it --rm -v " + sb.CredsVolume + ":/home/node " + sb.Image + " claude",
	})
}

func (s *Server) putSandbox(w http.ResponseWriter, r *http.Request) {
	var sb Sandbox
	if !readJSON(w, r, &sb) {
		return
	}
	sb.Image, sb.CredsVolume = strings.TrimSpace(sb.Image), strings.TrimSpace(sb.CredsVolume)
	sb.Memory, sb.CPUs = strings.TrimSpace(sb.Memory), strings.TrimSpace(sb.CPUs)
	if err := sb.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	for k, v := range map[string]string{
		"sandbox_image": sb.Image, "sandbox_creds_volume": sb.CredsVolume,
		"sandbox_memory": sb.Memory, "sandbox_cpus": sb.CPUs,
	} {
		if err := s.store.SetSetting(k, v); err != nil {
			serverError(w)
			return
		}
	}
	s.getSandbox(w, r)
}

func (s *Server) agentNames(w http.ResponseWriter, _ *http.Request) {
	names := agent.Names()
	sort.Strings(names)
	writeJSON(w, http.StatusOK, names)
}

// ---- repo references ----

var defaultHosts = map[string]string{"github": "github.com", "gitlab": "gitlab.com"}

// serverHost is the host a git server's repos must be cloned from.
func serverHost(g store.GitServer) string {
	if g.BaseURL != "" {
		if u, err := url.Parse(g.BaseURL); err == nil {
			return strings.ToLower(u.Host)
		}
	}
	return defaultHosts[g.Type]
}

// resolveRepos checks repo references from the browser and attaches each
// server's token. The clone URL must be on the server's own host: the token
// is sent wherever git is told to clone from, so it must never be possible to
// point it at another host.
func (s *Server) resolveRepos(refs []store.RepoRef) ([]sessions.Repo, error) {
	if len(refs) == 0 || len(refs) > 10 {
		return nil, fmt.Errorf("choose between 1 and 10 repos")
	}
	out := make([]sessions.Repo, 0, len(refs))
	for _, ref := range refs {
		g, ok, err := s.store.GitServer(ref.ServerID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("unknown git server %q", ref.ServerID)
		}
		ref.FullName = strings.TrimSpace(ref.FullName)
		if ref.FullName == "" || len(ref.FullName) > 200 {
			return nil, fmt.Errorf("a repo has no name")
		}
		u, err := url.Parse(ref.CloneURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("%s: the clone URL must be a plain http(s) URL", ref.FullName)
		}
		if h := serverHost(g); h == "" || strings.ToLower(u.Host) != h {
			return nil, fmt.Errorf("%s: the clone URL is not on %s's host", ref.FullName, g.Name)
		}
		if ref.Base != "" && (!branchRe.MatchString(ref.Base) || strings.Contains(ref.Base, "..")) {
			return nil, fmt.Errorf("%s: the base branch name looks invalid", ref.FullName)
		}
		tok, err := s.vault.Get(g.TokenSecret)
		if err != nil {
			return nil, fmt.Errorf("%s: no token saved for %s", ref.FullName, g.Name)
		}
		out = append(out, sessions.Repo{Ref: ref, Token: tok})
	}
	return out, nil
}

// ---- workspaces ----

func (s *Server) listWorkspaces(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.Workspaces()
	if err != nil {
		serverError(w)
		return
	}
	type view struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Repos []store.RepoRef `json:"repos"`
	}
	out := []view{}
	for _, x := range list {
		out = append(out, view{x.ID, x.Name, x.Repos})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string          `json:"name"`
		Repos []store.RepoRef `json:"repos"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 60 {
		writeErr(w, http.StatusBadRequest, "name is required (60 characters max)")
		return
	}
	// Same checks as launching, so a saved workspace can always be launched.
	if _, err := s.resolveRepos(req.Repos); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	x, err := s.store.SaveWorkspace(req.Name, req.Repos)
	if err != nil {
		serverError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": x.ID})
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	ok, err := s.store.DeleteWorkspace(r.PathValue("id"))
	switch {
	case err != nil:
		serverError(w)
	case !ok:
		writeErr(w, http.StatusNotFound, "no such workspace")
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// ---- sessions ----

type sessionView struct {
	ID         string          `json:"id"`
	Task       string          `json:"task"`
	Agent      string          `json:"agent"`
	Mode       string          `json:"mode"`
	Prompt     string          `json:"prompt"`
	Branch     string          `json:"branch"`
	Status     string          `json:"status"`
	Error      string          `json:"error"`
	Repos      []store.RepoRef `json:"repos"`
	CreatedAt  int64           `json:"createdAt"`
	StartedAt  int64           `json:"startedAt"`
	FinishedAt int64           `json:"finishedAt"`
}

func toView(x store.Session) sessionView {
	v := sessionView{x.ID, x.Task, x.Agent, x.Mode, x.Prompt, x.Branch, x.Status, x.Error, x.Repos, x.CreatedAt.Unix(), 0, 0}
	if !x.StartedAt.IsZero() {
		v.StartedAt = x.StartedAt.Unix()
	}
	if !x.FinishedAt.IsZero() {
		v.FinishedAt = x.FinishedAt.Unix()
	}
	return v
}

func (s *Server) listSessions(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.Sessions(50)
	if err != nil {
		serverError(w)
		return
	}
	out := []sessionView{}
	for _, x := range list {
		out = append(out, toView(x))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Task   string          `json:"task"`
		Agent  string          `json:"agent"`
		Prompt string          `json:"prompt"`
		Repos  []store.RepoRef `json:"repos"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Task, req.Prompt = strings.TrimSpace(req.Task), strings.TrimSpace(req.Prompt)
	switch {
	case req.Task == "" || len(req.Task) > 80:
		writeErr(w, http.StatusBadRequest, "task name is required (80 characters max)")
		return
	case req.Prompt == "" || len(req.Prompt) > 20000:
		writeErr(w, http.StatusBadRequest, "tell the agent what to do (20,000 characters max)")
		return
	}
	if _, err := agent.Get(req.Agent); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	repos, err := s.resolveRepos(req.Repos)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Only unattended sessions exist so far; interactive ones need a terminal.
	x, err := s.sessions.Start(sessions.Request{Task: req.Task, Agent: req.Agent, Prompt: req.Prompt, Mode: agent.HandsOff, Repos: repos})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toView(x))
}

func (s *Server) lookupSession(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	x, ok, err := s.store.Session(r.PathValue("id"))
	if err != nil {
		serverError(w)
		return x, false
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
	}
	return x, ok
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	if x, ok := s.lookupSession(w, r); ok {
		writeJSON(w, http.StatusOK, toView(x))
	}
}

func (s *Server) sessionLogs(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	events, err := s.store.Events(x.ID, after, 500)
	if err != nil {
		serverError(w)
		return
	}
	type ev struct {
		ID   int64  `json:"id"`
		TS   int64  `json:"ts"`
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	out := []ev{}
	for _, e := range events {
		out = append(out, ev{e.ID, e.TS.Unix(), e.Kind, e.Text})
	}
	// Re-read the status after the events so "done" never arrives before the last line.
	if latest, ok, _ := s.store.Session(x.ID); ok {
		x = latest
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "status": x.Status, "error": x.Error, "finished": store.Finished(x.Status)})
}

func (s *Server) stopSession(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	if store.Finished(x.Status) {
		writeErr(w, http.StatusConflict, "this session has already finished")
		return
	}
	s.sessions.Stop(x.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	ok, err := s.store.DeleteSession(r.PathValue("id"))
	switch {
	case err != nil:
		serverError(w)
	case !ok:
		writeErr(w, http.StatusConflict, "only a finished session can be deleted")
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

package server

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/egress"
	"github.com/mistergregg/unire/internal/repopick"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/safe"
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
	memoryRe   = regexp.MustCompile(`^(0|[1-9][0-9]*[kKmMgG]?)$`)
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
		return fmt.Errorf("memory limit should look like 4g or 512m (0 for no limit, empty for the default)")
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
	defaultImage := os.Getenv("UNIRE_SANDBOX_IMAGE") // set in the container image
	if defaultImage == "" {
		defaultImage = "unire-sandbox"
	}
	return Sandbox{
		Image:       get("sandbox_image", defaultImage),
		CredsVolume: get("sandbox_creds_volume", "unire-creds-claude"),
		Memory:      get("sandbox_memory", ""),
		CPUs:        get("sandbox_cpus", ""),
	}
}

func (s *Server) sessionConfig() sessions.Config {
	sb := s.sandbox()
	// Add-ons that are turned on: sandboxes start in an image that has them,
	// and the agent is told about their tools.
	rn, guide := s.sessionRunner(sb)
	allow := allowedEntries(s.allowList())
	return sessions.Config{
		Runner: rn, Memory: sb.EffectiveMemory(), CPUs: sb.CPUs,
		Guide:       guide,
		Egress:      sessions.Egress{Proxy: s.egressProxy, Info: s.egressInfo, Allow: allow},
		Pulls:       s.pulls,
		Upstream:    s,
		Repos:       s,
		MaxRun:      time.Duration(s.limits().MaxRunMinutes) * time.Minute,
		Attachments: s.attachmentsDir(),
		AutoPR:      s.autoOpenPulls(),
		AfterTurn:   s.afterTurn,
	}
}

// ---- network settings ----

func (s *Server) networkMode() egress.Mode {
	if v, ok, _ := s.store.Setting("network_mode"); ok {
		if m, ok := egress.ParseMode(v); ok {
			return m
		}
	}
	return egress.ModeInternet
}

func (s *Server) allowList() []string {
	v, _, _ := s.store.Setting("network_allow")
	var out []string
	for _, l := range strings.Split(v, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) saveAllowList(list []string) error {
	return s.store.SetSetting("network_allow", strings.Join(list, "\n"))
}

// cleanAllowList validates entries and removes blanks and duplicates.
func cleanAllowList(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		e, err := egress.ParseEntry(l)
		if err != nil {
			return nil, err
		}
		if err := e.Refusal(); err != nil {
			return nil, err
		}
		if !seen[e.String()] {
			seen[e.String()] = true
			out = append(out, e.String())
		}
	}
	return out, nil
}

func (s *Server) getNetwork(w http.ResponseWriter, _ *http.Request) {
	allow := s.allowList()
	if allow == nil {
		allow = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":       s.networkMode(),
		"allow":      allow,
		"protection": s.egressInfo,
		"modes": []map[string]string{
			{"id": string(egress.ModeInternet), "label": "Internet only", "description": "The internet, your git servers and the addresses you allow. Your private network is blocked."},
			{"id": string(egress.ModeAgentOnly), "label": "Agent only", "description": "Only the agent's own service, your git servers and the addresses you allow. Package installs and web fetches fail."},
			{"id": string(egress.ModeFull), "label": "Full access", "description": "No filtering. The agent can reach everything the server can, including your home network."},
		},
	})
}

func (s *Server) putNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode  string   `json:"mode"`
		Allow []string `json:"allow"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	mode, ok := egress.ParseMode(req.Mode)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown network mode")
		return
	}
	allow, err := cleanAllowList(req.Allow)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetSetting("network_mode", string(mode)); err != nil {
		serverError(w)
		return
	}
	if err := s.saveAllowList(allow); err != nil {
		serverError(w)
		return
	}
	s.getNetwork(w, r)
}

// allowAddress adds one address to the allowed list (the "Allow" button next
// to a blocked connection in a session log).
func (s *Server) allowAddress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	list, err := cleanAllowList(append(s.allowList(), req.Address))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveAllowList(list); err != nil {
		serverError(w)
		return
	}
	s.getNetwork(w, r)
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

type SessionPR struct {
	ServerID string `json:"serverId"`
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	State    string `json:"state"` // "open", "merged", "closed"
	Branch   string `json:"branch"`
	Base     string `json:"base,omitempty"`
}

type sessionView struct {
	ID           string          `json:"id"`
	Task         string          `json:"task"`
	Agent        string          `json:"agent"`
	Model        string          `json:"model"`
	Mode         string          `json:"mode"`
	Prompt       string          `json:"prompt"`
	Branch       string          `json:"branch"`
	Status       string          `json:"status"`
	Error        string          `json:"error"`
	Network      string          `json:"network"`
	Memory       bool            `json:"memory"`
	Parent       string          `json:"parent,omitempty"`
	CanSteer     bool            `json:"canSteer"`               // the agent takes messages while it works
	Latest       string          `json:"latest,omitempty"`       // list only: the newest turn of this conversation
	LatestStatus string          `json:"latestStatus,omitempty"` // list only
	Waiting      int             `json:"waiting,omitempty"`      // list only: questions the agent is waiting on you for
	LastActive   int64           `json:"lastActive,omitempty"`   // list only: when the owner last acted on the conversation, or its agent last finished
	Pushed       []string        `json:"pushed,omitempty"`       // repos this session pushed a branch to (session detail only)
	Pulls        []SessionPR     `json:"pulls,omitempty"`        // pull requests opened by this conversation
	Repos        []store.RepoRef `json:"repos"`
	CreatedAt    int64           `json:"createdAt"`
	StartedAt    int64           `json:"startedAt"`
	FinishedAt   int64           `json:"finishedAt"`
}

func toView(x store.Session) sessionView {
	v := sessionView{ID: x.ID, Task: x.Task, Agent: x.Agent, Model: x.Model, Mode: x.Mode, Prompt: x.Prompt, Branch: x.Branch, Status: x.Status, Error: x.Error,
		Network: x.Network, Memory: x.Memory, Parent: x.ParentID, Repos: x.Repos, CreatedAt: x.CreatedAt.Unix(),
		CanSteer: x.Mode == agent.HandsOff && agent.CanSteer(x.Agent)}
	if !x.StartedAt.IsZero() {
		v.StartedAt = x.StartedAt.Unix()
	}
	if !x.FinishedAt.IsZero() {
		v.FinishedAt = x.FinishedAt.Unix()
	}
	return v
}

func (s *Server) listSessions(w http.ResponseWriter, _ *http.Request) {
	all, err := s.store.Sessions(100000)
	if err != nil {
		serverError(w)
		return
	}
	byID := make(map[string]store.Session, len(all))
	childOf := make(map[string]store.Session, len(all))
	for _, x := range all {
		byID[x.ID] = x
		if x.ParentID != "" {
			// Sessions come back newest first and a reply is newer than its
			// parent, so the last write wins and holds the oldest child: the
			// same turn store.ChildOf would pick.
			childOf[x.ParentID] = x
		}
	}
	type conv struct {
		root   store.Session
		newest store.Session
	}
	var convs []conv
	newestIDs := make([]string, 0)
	for _, x := range all {
		if x.ParentID != "" {
			if _, ok := byID[x.ParentID]; ok {
				continue // a reply: shown inside its conversation
			}
			if _, ok, _ := s.store.Session(x.ParentID); ok {
				continue
			}
		}
		newest := x
		seen := map[string]bool{x.ID: true}
		for {
			ch, ok := childOf[newest.ID]
			if !ok || seen[ch.ID] {
				break
			}
			seen[ch.ID] = true
			newest = ch
		}
		convs = append(convs, conv{root: x, newest: newest})
		newestIDs = append(newestIDs, newest.ID)
	}
	lastOwners, err := s.store.LastOwnerEvents(newestIDs)
	if err != nil {
		lastOwners = nil
	}
	allPRs, _ := s.store.AllPullRequests()
	prsByRoot := map[string][]SessionPR{}
	for _, pr := range allPRs {
		state := pr.State
		if state == "" {
			state = "open"
		}
		prsByRoot[pr.RootID] = append(prsByRoot[pr.RootID], SessionPR{
			ServerID: pr.ServerID,
			Repo:     pr.Repo,
			Number:   pr.Number,
			Title:    pr.Title,
			URL:      pr.URL,
			State:    state,
			Branch:   pr.Branch,
			Base:     pr.Base,
		})
	}
	// One row per conversation: its first turn, with the newest turn's status.
	out := make([]sessionView, 0, len(convs))
	for _, c := range convs {
		v := toView(c.root)
		if c.newest.ID != c.root.ID {
			v.Latest, v.LatestStatus = c.newest.ID, c.newest.Status
		}
		v.Waiting = s.sessions.Waiting(c.newest.ID)
		var owner time.Time
		if lastOwners != nil {
			owner = lastOwners[c.newest.ID]
		} else {
			owner, _ = s.store.LastOwnerEvent(c.newest.ID)
		}
		v.LastActive = lastActive(c.newest, owner)
		if prs, ok := prsByRoot[c.root.ID]; ok {
			v.Pulls = prs
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b sessionView) int {
		if a.LastActive != b.LastActive {
			return cmp.Compare(b.LastActive, a.LastActive)
		}
		return cmp.Compare(b.CreatedAt, a.CreatedAt)
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Task     string          `json:"task"`
		Agent    string          `json:"agent"`
		Model    *string         `json:"model"` // omitted or empty: the agent's saved default, else the CLI's own
		Prompt   string          `json:"prompt"`
		Network  string          `json:"network"` // empty means the saved default
		Memory   *bool           `json:"memory"`  // omitted means on
		Repos    []store.RepoRef `json:"repos"`
		ForkFrom string          `json:"forkFrom"` // start from this conversation: it is told what happened there
		Parent   string          `json:"parent"`   // follow this conversation: it is told what happens there
		Images   []imageIn       `json:"images"`   // attached to the prompt
	}
	if !readMessage(w, r, &req) {
		return
	}
	images, err := decodeImages(req.Images)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var chosenModel string
	if req.Model != nil {
		chosenModel = strings.TrimSpace(*req.Model)
		if chosenModel != "" {
			if err := agent.ValidateModel(chosenModel); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}
	req.Task, req.Prompt = strings.TrimSpace(req.Task), strings.TrimSpace(req.Prompt)
	if req.Task == "" {
		req.Task = taskNameFrom(req.Prompt) // the name is made from what was asked
	}
	switch {
	case len(req.Task) > 80:
		writeErr(w, http.StatusBadRequest, "task name is too long (80 characters max)")
		return
	case req.Prompt == "" || len(req.Prompt) > 20000:
		writeErr(w, http.StatusBadRequest, "tell the agent what to do (20,000 characters max)")
		return
	}
	ad, err := agent.Get(req.Agent)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// No model named for this session: use the one saved for the agent, which is
	// empty until the Agents page sets one, leaving the CLI's own default.
	if chosenModel == "" {
		chosenModel = s.agentDefaultModel(ad.Info().ID)
	}
	// Fail fast, with a way forward, when the agent is known not to be signed in.
	// Only for agents whose sign-in check has been verified: a guess must never
	// block someone who is in fact signed in.
	if msg := s.requireSignedIn(r.Context(), ad); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	// Started from another conversation: it is told what happened there.
	var preamble string
	var forkedFrom string
	if req.ForkFrom != "" {
		src, ok, err := s.store.Session(req.ForkFrom)
		if err != nil || !ok {
			writeErr(w, http.StatusBadRequest, "the chat to start from no longer exists")
			return
		}
		thread, err := s.threadOf(src.ID)
		if err != nil || len(thread) == 0 {
			serverError(w)
			return
		}
		forkedFrom = thread[0].ID
		repos0, where := s.forkState(r.Context(), thread)
		if len(req.Repos) == 0 {
			req.Repos = repos0
		}
		preamble = handoffPreamble(thread[0].Task, thread, s.turnDigestText, where)
	}
	// Following a parent chat: it is told what the parent has done so far.
	var upstreamID string
	var upstreamSeen int
	if req.Parent != "" {
		pt, err := s.threadOf(req.Parent)
		if err != nil || len(pt) == 0 {
			writeErr(w, http.StatusBadRequest, "the parent chat no longer exists")
			return
		}
		upstreamID = pt[0].ID
		var text string
		text, upstreamSeen = s.upstreamUpdate(r.Context(), store.Session{UpstreamID: upstreamID}, true)
		if text != "" {
			if preamble == "" {
				preamble = "The owner now writes:\n\n"
			}
			preamble = text + "\n" + preamble
		}
	}
	var picked []repopick.Candidate
	if len(req.Repos) == 0 {
		// The owner chose no repos: use the ones the message is clearly about, and
		// those usually used with them.
		ranked := s.rankRepos(r.Context(), req.Prompt, nil)
		picked = repopick.Auto(ranked)
		if len(picked) == 0 {
			msg := "I could not tell which repos this needs from your message. Name one (like its repo name) or add it with the box under the message."
			if len(ranked) > 0 {
				var near []string
				for i, c := range ranked {
					if i >= 3 {
						break
					}
					near = append(near, c.FullName)
				}
				msg += " Closest: " + strings.Join(near, ", ") + "."
			}
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		for _, c := range picked {
			req.Repos = append(req.Repos, refOf(c))
		}
	}
	repos, err := s.resolveRepos(req.Repos)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := s.networkMode()
	if req.Network != "" {
		var ok bool
		if mode, ok = egress.ParseMode(req.Network); !ok {
			writeErr(w, http.StatusBadRequest, "unknown network mode")
			return
		}
	}
	// Only unattended sessions exist so far; interactive ones need a terminal.
	useMemory := req.Memory == nil || *req.Memory
	x, err := s.sessions.Start(sessions.Request{Task: req.Task, Agent: req.Agent, Model: chosenModel, Prompt: req.Prompt, Mode: agent.HandsOff, Network: mode, Memory: useMemory, Repos: repos, Notes: pickNotes(picked), Preamble: preamble, ForkedFrom: forkedFrom, UpstreamID: upstreamID, Attachments: images})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if upstreamID != "" {
		_ = s.store.SetUpstreamSeen(x.ID, upstreamSeen)
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

// pushedFrom finds which repos a session pushed, from its log lines
// ("<dir>: pushed <branch>"), as full names.
func pushedFrom(events []store.Event, refs []store.RepoRef) []string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.FullName
	}
	dirs := sessions.RepoDirNames(names)
	pushed := map[string]bool{}
	for _, e := range events {
		if e.Kind != "log" {
			continue
		}
		for i, d := range dirs {
			if strings.HasPrefix(e.Text, d+": pushed ") {
				pushed[refs[i].FullName] = true
			}
		}
	}
	out := []string{}
	for _, r := range refs {
		if pushed[r.FullName] {
			out = append(out, r.FullName)
		}
	}
	return out
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.viewWithPushed(x))
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
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "status": x.Status, "error": x.Error, "finished": store.Finished(x.Status), "saving": s.sessions.Saving(x.ID)})
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
	id := r.PathValue("id")
	x, found, _ := s.store.Session(id)
	ok, err := s.store.DeleteSession(id)
	switch {
	case err != nil:
		serverError(w)
	case !ok:
		writeErr(w, http.StatusConflict, "only a finished session can be deleted")
	default:
		sessions.RemoveAttachments(s.attachmentsDir(), id)
		if found && x.ParentID == "" {
			// The first message of a conversation owns its workspace (the cloned repos).
			safe.Go("remove a workspace", func() { s.sessions.ForgetWorkspace(id) })
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// taskNameFrom makes a short name from the first line of the prompt: the
// words that fit in 60 characters, cut at a word boundary.
func taskNameFrom(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	words := strings.Fields(line)
	name := ""
	for _, w := range words {
		next := w
		if name != "" {
			next = name + " " + w
		}
		if len([]rune(next)) > 60 {
			if name == "" { // one very long word
				name = string([]rune(w)[:57])
			}
			return name + "…"
		}
		name = next
	}
	return name
}

// DefaultSandboxMemory is the memory limit of a chat's sandbox when none is set. One
// runaway agent can otherwise use all of the host's memory and take down everything
// else running on it. Set the limit in Settings, or 0 for no limit.
const DefaultSandboxMemory = "4g"

// EffectiveMemory is the limit a sandbox is started with: the setting, the default when
// it is empty, and no limit at all when it is 0.
func (sb Sandbox) EffectiveMemory() string {
	switch sb.Memory {
	case "":
		return DefaultSandboxMemory
	case "0":
		return ""
	}
	return sb.Memory
}

// allowedEntries are the saved allowed addresses the proxy may honour. An entry that
// names loopback or a metadata address is dropped: older versions let the owner press
// "Allow" on one, which could only ever open the proxy's own services.
func allowedEntries(saved []string) []egress.Entry {
	var out []egress.Entry
	for _, l := range saved {
		e, err := egress.ParseEntry(l)
		if err != nil || e.Refusal() != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// lastActive is when a conversation was last acted on, from its newest turn: when it was
// sent, started or finished, or when the owner last said something to it while it ran.
// The chat list is ordered by it, so the chat you touched last comes first.
func lastActive(newest store.Session, ownerEvent time.Time) int64 {
	last := newest.CreatedAt
	for _, t := range []time.Time{newest.StartedAt, newest.FinishedAt, ownerEvent} {
		if t.After(last) {
			last = t
		}
	}
	return last.Unix()
}

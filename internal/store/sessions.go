package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/mistergregg/unire/internal/session"
)

// RepoRef points at a repo on a configured git server.
type RepoRef struct {
	ServerID string `json:"serverId"`
	FullName string `json:"fullName"`
	CloneURL string `json:"cloneUrl"`
	Base     string `json:"base"` // base branch; empty means the repo's default
}

// Session statuses.
const (
	StatusQueued      = "queued"
	StatusRunning     = "running"
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusStopped     = "stopped"
	StatusInterrupted = "interrupted" // Unire stopped while it was running
)

// Finished reports whether a status is final.
func Finished(status string) bool {
	return status != StatusQueued && status != StatusRunning
}

// Workspace is a named set of repos.
type Workspace struct {
	ID        string
	Name      string
	Repos     []RepoRef
	CreatedAt time.Time
}

// SaveWorkspace creates a workspace, or replaces the repos of one with the same name.
func (s *Store) SaveWorkspace(name string, repos []RepoRef) (Workspace, error) {
	b, err := json.Marshal(repos)
	if err != nil {
		return Workspace{}, err
	}
	id := session.Slug(name)
	now := s.now()
	_, err = s.db.Exec(`INSERT INTO workspaces (id, name, repos, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, repos = excluded.repos`, id, name, string(b), now.Unix())
	return Workspace{ID: id, Name: name, Repos: repos, CreatedAt: now}, err
}

// Workspaces lists saved workspaces by name.
func (s *Store) Workspaces() ([]Workspace, error) {
	rows, err := s.db.Query(`SELECT id, name, repos, created_at FROM workspaces ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		var w Workspace
		var repos string
		var created int64
		if err := rows.Scan(&w.ID, &w.Name, &repos, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(repos), &w.Repos); err != nil {
			return nil, err
		}
		w.CreatedAt = time.Unix(created, 0)
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeleteWorkspace removes a workspace.
func (s *Store) DeleteWorkspace(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Session is one coding session.
type Session struct {
	ID           string
	Task         string
	Agent        string
	Model        string // model chosen for this turn; empty means the agent's default
	Mode         string
	Prompt       string
	Branch       string
	Status       string
	Error        string
	Network      string // network mode: internet, agent-only or full
	Memory       bool   // use and update project memory
	ParentID     string // the session this one continues, or empty
	ForkedFrom   string // the conversation (its first session) this one was started from, or empty
	UpstreamID   string // the conversation (its first session) it keeps getting updates from, or empty
	UpstreamSeen int    // how many finished turns of that conversation it has been told about
	AutoFix      bool   // start a fix by itself when the conversation's pull request has failing checks (kept on the first session)
	AutoFixSet   bool   // CreateSession: AutoFix was chosen on purpose (otherwise the default, on, applies)
	AgentConv    string // the agent's own conversation id (its session or thread) for this turn, to carry it on
	Repos        []RepoRef
	CreatedAt    time.Time
	StartedAt    time.Time // zero until it starts
	FinishedAt   time.Time // zero until it ends
}

const sessionCols = `id, task, agent, mode, prompt, branch, status, error, repos, created_at, started_at, finished_at, network, memory, parent_id, forked_from, upstream_id, upstream_seen, auto_fix, agent_conv, model`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var x Session
	var repos string
	var created, started, finished int64
	var mem, autoFix int
	if err := sc.Scan(&x.ID, &x.Task, &x.Agent, &x.Mode, &x.Prompt, &x.Branch, &x.Status, &x.Error, &repos, &created, &started, &finished, &x.Network, &mem, &x.ParentID, &x.ForkedFrom, &x.UpstreamID, &x.UpstreamSeen, &autoFix, &x.AgentConv, &x.Model); err != nil {
		return x, err
	}
	x.Memory = mem != 0
	x.AutoFix = autoFix != 0
	if err := json.Unmarshal([]byte(repos), &x.Repos); err != nil {
		return x, err
	}
	x.CreatedAt = time.Unix(created, 0)
	if started > 0 {
		x.StartedAt = time.Unix(started, 0)
	}
	if finished > 0 {
		x.FinishedAt = time.Unix(finished, 0)
	}
	return x, nil
}

// CreateSession saves a new queued session.
func (s *Store) CreateSession(x Session) error {
	b, err := json.Marshal(x.Repos)
	if err != nil {
		return err
	}
	if x.Network == "" {
		x.Network = "internet"
	}
	mem := 0
	if x.Memory {
		mem = 1
	}
	_, err = s.db.Exec(`INSERT INTO sessions (`+sessionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.ID, x.Task, x.Agent, x.Mode, x.Prompt, x.Branch, StatusQueued, "", string(b), s.now().Unix(), x.Network, mem, x.ParentID, x.ForkedFrom, x.UpstreamID, x.UpstreamSeen, autoFixInt(x), x.AgentConv, x.Model)
	return err
}

// Sessions lists the most recent sessions.
func (s *Store) Sessions(limit int) ([]Session, error) {
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Session returns one session.
func (s *Store) Session(id string) (Session, bool, error) {
	x, err := scanSession(s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	return x, err == nil, err
}

// MarkRunning moves a session to running.
func (s *Store) MarkRunning(id string) error {
	_, err := s.db.Exec(`UPDATE sessions SET status = ?, started_at = ? WHERE id = ?`, StatusRunning, s.now().Unix(), id)
	return err
}

// MarkFinished records a final status.
func (s *Store) MarkFinished(id, status, errMsg string) error {
	if !Finished(status) {
		return errors.New("not a final status: " + status)
	}
	_, err := s.db.Exec(`UPDATE sessions SET status = ?, error = ?, finished_at = ? WHERE id = ?`, status, errMsg, s.now().Unix(), id)
	return err
}

// InterruptUnfinished marks sessions left running by a previous process as
// interrupted, and returns how many there were.
func (s *Store) InterruptUnfinished() (int64, error) {
	res, err := s.db.Exec(`UPDATE sessions SET status = ?, error = 'Unire stopped while this session was running', finished_at = ? WHERE status IN (?, ?)`,
		StatusInterrupted, s.now().Unix(), StatusQueued, StatusRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteSession removes a finished session and its log.
func (s *Store) DeleteSession(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE id = ? AND status NOT IN (?, ?)`, id, StatusQueued, StatusRunning)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Event is one line of a session's log.
type Event struct {
	ID   int64
	TS   time.Time
	Kind string
	Text string
}

// AddEvent appends a log line to a session.
func (s *Store) AddEvent(sessionID, kind, text string) error {
	_, err := s.db.Exec(`INSERT INTO session_events (session_id, ts, kind, text) VALUES (?, ?, ?, ?)`, sessionID, s.now().Unix(), kind, text)
	return err
}

// Events returns log lines with an ID greater than after, oldest first.
func (s *Store) Events(sessionID string, after int64, limit int) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id, ts, kind, text FROM session_events WHERE session_id = ? AND id > ? ORDER BY id LIMIT ?`, sessionID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Kind, &e.Text); err != nil {
			return nil, err
		}
		e.TS = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

const maxThread = 100 // a guard against a loop, far more than anyone replies

// Thread returns every session of the conversation that id belongs to, first to last.
func (s *Store) Thread(id string) ([]Session, error) {
	x, ok, err := s.Session(id)
	if err != nil || !ok {
		return nil, err
	}
	for i := 0; i < maxThread && x.ParentID != ""; i++ {
		p, ok, err := s.Session(x.ParentID)
		if err != nil {
			return nil, err
		}
		if !ok {
			break // the earlier turns were deleted
		}
		x = p
	}
	out := []Session{x}
	for len(out) < maxThread {
		c, ok, err := s.ChildOf(x.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		out, x = append(out, c), c
	}
	return out, nil
}

// BranchTaken reports whether any session already uses this branch name.
func (s *Store) BranchTaken(branch string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE branch = ?`, branch).Scan(&n)
	return n > 0, err
}

// ChildOf returns the session that continues id, if any.
func (s *Store) ChildOf(id string) (Session, bool, error) {
	x, err := scanSession(s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE parent_id = ? ORDER BY created_at, id LIMIT 1`, id))
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	return x, err == nil, err
}

// SetTask renames a session. The branch it already uses keeps its name.
func (s *Store) SetTask(id, task string) error {
	_, err := s.db.Exec(`UPDATE sessions SET task = ? WHERE id = ?`, task, id)
	return err
}

// SetSessionRepos replaces the repos of a session, when more were added while it ran.
func (s *Store) SetSessionRepos(id string, repos []RepoRef) error {
	b, err := json.Marshal(repos)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE sessions SET repos = ? WHERE id = ?`, string(b), id)
	return err
}

// PruneLogs deletes the activity lines (progress, network) of sessions that
// finished before the cutoff, and returns how many. The answers, questions and
// messages stay, and so do the lines saying which branch was pushed, because pull
// requests are found from those.
func (s *Store) PruneLogs(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM session_events WHERE kind IN ('log', 'net', 'blocked')
		AND text NOT LIKE '%: pushed %' AND text NOT LIKE '%: also pushed %'
		AND session_id IN (SELECT id FROM sessions WHERE finished_at > 0 AND finished_at < ?)`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// IdleConversations returns the first session of every conversation whose turns
// have all finished, the newest before the cutoff.
func (s *Store) IdleConversations(before time.Time) ([]string, error) {
	list, err := s.Sessions(100000)
	if err != nil {
		return nil, err
	}
	byID := map[string]Session{}
	for _, x := range list {
		byID[x.ID] = x
	}
	type conv struct {
		newest time.Time
		busy   bool
	}
	convs := map[string]*conv{}
	for _, x := range list {
		root := x.ID
		for i := 0; i < 1000 && byID[root].ParentID != ""; i++ {
			if _, ok := byID[byID[root].ParentID]; !ok {
				break
			}
			root = byID[root].ParentID
		}
		c := convs[root]
		if c == nil {
			c = &conv{}
			convs[root] = c
		}
		if !Finished(x.Status) {
			c.busy = true
		}
		if x.FinishedAt.After(c.newest) {
			c.newest = x.FinishedAt
		}
	}
	var out []string
	for root, c := range convs {
		if !c.busy && !c.newest.IsZero() && c.newest.Before(before) {
			out = append(out, root)
		}
	}
	return out, nil
}

// SetUpstream links a conversation to a parent it keeps getting updates from (empty
// unlinks). It is kept on the conversation's first session. The count of the parent's
// turns it has seen starts again.
func (s *Store) SetUpstream(rootID, upstreamID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET upstream_id = ?, upstream_seen = 0 WHERE id = ?`, upstreamID, rootID)
	return err
}

// SetUpstreamSeen records how many finished turns of its parent a conversation has been told about.
func (s *Store) SetUpstreamSeen(rootID string, n int) error {
	_, err := s.db.Exec(`UPDATE sessions SET upstream_seen = ? WHERE id = ?`, n, rootID)
	return err
}

// ConversationsLinkedTo lists the first sessions of conversations whose parent is upstreamID.
func (s *Store) ConversationsLinkedTo(upstreamID string) ([]Session, error) {
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE upstream_id = ? ORDER BY created_at, id`, upstreamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// autoFixInt is 1 for a new session unless the caller said otherwise. A reply or a
// session made by code that never heard of the setting gets the default (on); the setting
// that counts is the one on the conversation's first session.
func autoFixInt(x Session) int {
	if x.AutoFix || !x.AutoFixSet {
		return 1
	}
	return 0
}

// SetAutoFix turns fixing failing checks by itself on or off for a conversation.
func (s *Store) SetAutoFix(rootID string, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE sessions SET auto_fix = ? WHERE id = ?`, v, rootID)
	return err
}

// SetAgentConv records the agent's own conversation id for a turn.
func (s *Store) SetAgentConv(id, conv string) error {
	_, err := s.db.Exec(`UPDATE sessions SET agent_conv = ? WHERE id = ?`, conv, id)
	return err
}

// LastOwnerEvent is when the owner last said something to a session while it ran (a
// message to the agent, or an answer to its question), or the zero time if never.
func (s *Store) LastOwnerEvent(id string) (time.Time, error) {
	var ts int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(ts), 0) FROM session_events WHERE session_id = ? AND kind IN ('steer', 'answer')`, id).Scan(&ts)
	if err != nil || ts == 0 {
		return time.Time{}, err
	}
	return time.Unix(ts, 0), nil
}

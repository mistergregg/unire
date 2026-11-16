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
	ID         string
	Task       string
	Agent      string
	Mode       string
	Prompt     string
	Branch     string
	Status     string
	Error      string
	Network    string // network mode: internet, agent-only or full
	Memory     bool   // use and update project memory
	ParentID   string // the session this one continues, or empty
	Repos      []RepoRef
	CreatedAt  time.Time
	StartedAt  time.Time // zero until it starts
	FinishedAt time.Time // zero until it ends
}

const sessionCols = `id, task, agent, mode, prompt, branch, status, error, repos, created_at, started_at, finished_at, network, memory, parent_id`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var x Session
	var repos string
	var created, started, finished int64
	var mem int
	if err := sc.Scan(&x.ID, &x.Task, &x.Agent, &x.Mode, &x.Prompt, &x.Branch, &x.Status, &x.Error, &repos, &created, &started, &finished, &x.Network, &mem, &x.ParentID); err != nil {
		return x, err
	}
	x.Memory = mem != 0
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
	_, err = s.db.Exec(`INSERT INTO sessions (`+sessionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?)`,
		x.ID, x.Task, x.Agent, x.Mode, x.Prompt, x.Branch, StatusQueued, "", string(b), s.now().Unix(), x.Network, mem, x.ParentID)
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

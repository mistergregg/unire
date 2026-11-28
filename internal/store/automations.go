package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Automation is a task that runs by itself on a schedule. Every run is a turn in the
// automation's own chat (RootID), so its history reads like a conversation.
type Automation struct {
	ID           string
	Name         string
	Prompt       string
	Agent        string
	Model        string // empty: the agent's saved default
	Repos        []RepoRef
	EveryMinutes int    // runs every this many minutes; 0 means once a day at DailyAt
	DailyAt      string // "HH:MM" in TimeZone, when EveryMinutes is 0
	TimeZone     string // IANA name of the owner's time zone (from their browser); empty: the server's
	MaxPerDay    int    // at most this many runs a day (server's day); 0: no cap
	MaxUsage     int    // skipped while the agent's usage is at this percent or more; 0: no cap
	MayMerge     bool   // the agent may merge pull requests whose checks all passed
	Enabled      bool
	RootID       string    // the automation's chat; empty until the first run
	NextRun      time.Time // when it runs next (zero: not scheduled)
	CreatedAt    time.Time
}

// AutomationRun is one time an automation was due: the turn it started, or why not.
type AutomationRun struct {
	At        time.Time
	SessionID string // empty when skipped
	Note      string
}

const automationCols = `id, name, prompt, agent, model, repos, every_minutes, daily_at, max_per_day, max_usage, may_merge, enabled, root_id, next_run, created_at, time_zone`

func scanAutomation(sc interface{ Scan(...any) error }) (Automation, error) {
	var a Automation
	var repos string
	var merge, enabled int
	var next, created int64
	err := sc.Scan(&a.ID, &a.Name, &a.Prompt, &a.Agent, &a.Model, &repos, &a.EveryMinutes, &a.DailyAt, &a.MaxPerDay, &a.MaxUsage, &merge, &enabled, &a.RootID, &next, &created, &a.TimeZone)
	if err != nil {
		return a, err
	}
	_ = json.Unmarshal([]byte(repos), &a.Repos)
	if a.Repos == nil {
		a.Repos = []RepoRef{}
	}
	a.MayMerge, a.Enabled = merge != 0, enabled != 0
	if next > 0 {
		a.NextRun = time.Unix(next, 0)
	}
	a.CreatedAt = time.Unix(created, 0)
	return a, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// SaveAutomation adds an automation (empty ID: a new one is made) or replaces the
// settings of an existing one. Its chat is kept. It returns the automation as saved.
func (s *Store) SaveAutomation(a Automation) (Automation, error) {
	if a.Repos == nil {
		a.Repos = []RepoRef{}
	}
	repos, err := json.Marshal(a.Repos)
	if err != nil {
		return a, err
	}
	if a.ID == "" {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		a.ID = hex.EncodeToString(b)
		a.CreatedAt = s.now()
		_, err = s.db.Exec(`INSERT INTO automations (`+automationCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.Name, a.Prompt, a.Agent, a.Model, string(repos), a.EveryMinutes, a.DailyAt, a.MaxPerDay, a.MaxUsage,
			boolInt(a.MayMerge), boolInt(a.Enabled), a.RootID, unixOrZero(a.NextRun), a.CreatedAt.Unix(), a.TimeZone)
		return a, err
	}
	res, err := s.db.Exec(`UPDATE automations SET name = ?, prompt = ?, agent = ?, model = ?, repos = ?, every_minutes = ?, daily_at = ?,
		max_per_day = ?, max_usage = ?, may_merge = ?, enabled = ?, next_run = ?, time_zone = ? WHERE id = ?`,
		a.Name, a.Prompt, a.Agent, a.Model, string(repos), a.EveryMinutes, a.DailyAt, a.MaxPerDay, a.MaxUsage,
		boolInt(a.MayMerge), boolInt(a.Enabled), unixOrZero(a.NextRun), a.TimeZone, a.ID)
	if err != nil {
		return a, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return a, sql.ErrNoRows
	}
	got, _, err := s.Automation(a.ID)
	return got, err
}

// Automation returns one automation.
func (s *Store) Automation(id string) (Automation, bool, error) {
	a, err := scanAutomation(s.db.QueryRow(`SELECT `+automationCols+` FROM automations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Automation{}, false, nil
	}
	return a, err == nil, err
}

// Automations lists every automation, oldest first.
func (s *Store) Automations() ([]Automation, error) {
	rows, err := s.db.Query(`SELECT ` + automationCols + ` FROM automations ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Automation
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AutomationForChat returns the automation whose chat starts with rootID.
func (s *Store) AutomationForChat(rootID string) (Automation, bool, error) {
	if rootID == "" {
		return Automation{}, false, nil
	}
	a, err := scanAutomation(s.db.QueryRow(`SELECT `+automationCols+` FROM automations WHERE root_id = ?`, rootID))
	if errors.Is(err, sql.ErrNoRows) {
		return Automation{}, false, nil
	}
	return a, err == nil, err
}

// DeleteAutomation removes an automation and its run history. Its chat stays.
func (s *Store) DeleteAutomation(id string) (bool, error) {
	if _, err := s.db.Exec(`DELETE FROM automation_runs WHERE automation_id = ?`, id); err != nil {
		return false, err
	}
	res, err := s.db.Exec(`DELETE FROM automations WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetAutomationNextRun records when an automation runs next.
func (s *Store) SetAutomationNextRun(id string, next time.Time) error {
	_, err := s.db.Exec(`UPDATE automations SET next_run = ? WHERE id = ?`, unixOrZero(next), id)
	return err
}

// SetAutomationEnabled turns an automation on or off, and sets when it runs next.
func (s *Store) SetAutomationEnabled(id string, on bool, next time.Time) error {
	_, err := s.db.Exec(`UPDATE automations SET enabled = ?, next_run = ? WHERE id = ?`, boolInt(on), unixOrZero(next), id)
	return err
}

// SetAutomationChat records the chat an automation's runs go into.
func (s *Store) SetAutomationChat(id, rootID string) error {
	_, err := s.db.Exec(`UPDATE automations SET root_id = ? WHERE id = ?`, rootID, id)
	return err
}

// AddAutomationRun records that an automation was due: the turn it started, or why not.
func (s *Store) AddAutomationRun(id string, r AutomationRun) error {
	at := r.At
	if at.IsZero() {
		at = s.now()
	}
	_, err := s.db.Exec(`INSERT INTO automation_runs (automation_id, at, session_id, note) VALUES (?, ?, ?, ?)`, id, at.Unix(), r.SessionID, r.Note)
	return err
}

// AutomationRuns lists an automation's latest runs and skips, newest first.
func (s *Store) AutomationRuns(id string, limit int) ([]AutomationRun, error) {
	rows, err := s.db.Query(`SELECT at, session_id, note FROM automation_runs WHERE automation_id = ? ORDER BY at DESC, id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AutomationRun
	for rows.Next() {
		var r AutomationRun
		var at int64
		if err := rows.Scan(&at, &r.SessionID, &r.Note); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AutomationRunsSince counts the turns an automation started at or after since
// (skips do not count).
func (s *Store) AutomationRunsSince(id string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM automation_runs WHERE automation_id = ? AND session_id != '' AND at >= ?`, id, since.Unix()).Scan(&n)
	return n, err
}

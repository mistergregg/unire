package store

import (
	"database/sql"
	"strings"
	"time"
)

// RepoKey identifies a repo in memory: its server and full name.
func RepoKey(r RepoRef) string { return r.ServerID + "/" + r.FullName }

// RepoMemory is the rolling brief for one repo.
type RepoMemory struct {
	RepoKey   string
	Brief     string
	Previous  string // the brief before the latest change
	SessionID string // the session that wrote it; empty if the owner edited it
	UpdatedAt time.Time
}

// RepoMemory returns a repo's brief.
func (s *Store) RepoMemory(key string) (RepoMemory, bool, error) {
	var m RepoMemory
	var updated int64
	err := s.db.QueryRow(`SELECT repo_key, brief, previous, session_id, updated_at FROM repo_memory WHERE repo_key = ?`, key).
		Scan(&m.RepoKey, &m.Brief, &m.Previous, &m.SessionID, &updated)
	if err == sql.ErrNoRows {
		return RepoMemory{}, false, nil
	}
	m.UpdatedAt = time.Unix(updated, 0)
	return m, err == nil, err
}

// RepoMemories lists every repo that has a brief, newest first.
func (s *Store) RepoMemories() ([]RepoMemory, error) {
	rows, err := s.db.Query(`SELECT repo_key, brief, previous, session_id, updated_at FROM repo_memory ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoMemory
	for rows.Next() {
		var m RepoMemory
		var updated int64
		if err := rows.Scan(&m.RepoKey, &m.Brief, &m.Previous, &m.SessionID, &updated); err != nil {
			return nil, err
		}
		m.UpdatedAt = time.Unix(updated, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetRepoMemory saves a brief and keeps the old one as the undo step.
func (s *Store) SetRepoMemory(key, brief, sessionID string) error {
	_, err := s.db.Exec(`INSERT INTO repo_memory (repo_key, brief, previous, session_id, updated_at) VALUES (?, ?, '', ?, ?)
		ON CONFLICT(repo_key) DO UPDATE SET previous = repo_memory.brief, brief = excluded.brief,
		session_id = excluded.session_id, updated_at = excluded.updated_at`, key, brief, sessionID, s.now().Unix())
	return err
}

// RestoreRepoMemory swaps the brief back to the previous one. It reports
// false when there is nothing to restore.
func (s *Store) RestoreRepoMemory(key string) (bool, error) {
	res, err := s.db.Exec(`UPDATE repo_memory SET brief = previous, previous = brief, session_id = '', updated_at = ?
		WHERE repo_key = ? AND previous != ''`, s.now().Unix(), key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteRepoMemory removes a repo's brief (its session summaries stay).
func (s *Store) DeleteRepoMemory(key string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM repo_memory WHERE repo_key = ?`, key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Summary is what happened in one finished session.
type Summary struct {
	SessionID string
	Task      string
	Agent     string
	Branch    string
	Status    string
	Summary   string
	Source    string // "agent" or "fallback"
	Repos     []string
	CreatedAt time.Time
}

// SaveSummary stores a session's summary and links it to its repos.
func (s *Store) SaveSummary(x Summary) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO session_summaries (session_id, task, agent, branch, status, summary, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET summary = excluded.summary, source = excluded.source, status = excluded.status`,
		x.SessionID, x.Task, x.Agent, x.Branch, x.Status, x.Summary, x.Source, s.now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM summary_repos WHERE session_id = ?`, x.SessionID); err != nil {
		return err
	}
	for _, k := range x.Repos {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO summary_repos (session_id, repo_key) VALUES (?, ?)`, x.SessionID, k); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) scanSummaries(rows *sql.Rows) ([]Summary, error) {
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var x Summary
		var created int64
		if err := rows.Scan(&x.SessionID, &x.Task, &x.Agent, &x.Branch, &x.Status, &x.Summary, &x.Source, &created); err != nil {
			return nil, err
		}
		x.CreatedAt = time.Unix(created, 0)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Load repo links after the first result set is closed (one connection).
	for i := range out {
		r, err := s.db.Query(`SELECT repo_key FROM summary_repos WHERE session_id = ? ORDER BY repo_key`, out[i].SessionID)
		if err != nil {
			return nil, err
		}
		for r.Next() {
			var k string
			if err := r.Scan(&k); err != nil {
				r.Close()
				return nil, err
			}
			out[i].Repos = append(out[i].Repos, k)
		}
		r.Close()
	}
	return out, nil
}

const summaryCols = `s.session_id, s.task, s.agent, s.branch, s.status, s.summary, s.source, s.created_at`

// Summary returns one session's summary.
func (s *Store) Summary(sessionID string) (Summary, bool, error) {
	rows, err := s.db.Query(`SELECT `+summaryCols+` FROM session_summaries s WHERE s.session_id = ?`, sessionID)
	if err != nil {
		return Summary{}, false, err
	}
	list, err := s.scanSummaries(rows)
	if err != nil || len(list) == 0 {
		return Summary{}, false, err
	}
	return list[0], true, nil
}

// RepoSummaries returns the newest summaries that touched a repo.
func (s *Store) RepoSummaries(key string, limit int) ([]Summary, error) {
	rows, err := s.db.Query(`SELECT `+summaryCols+` FROM session_summaries s
		JOIN summary_repos r ON r.session_id = s.session_id WHERE r.repo_key = ?
		ORDER BY s.created_at DESC, s.session_id DESC LIMIT ?`, key, limit)
	if err != nil {
		return nil, err
	}
	return s.scanSummaries(rows)
}

// SearchSummaries finds summaries containing every word of the query,
// optionally limited to one repo.
func (s *Store) SearchSummaries(query, repoKey string, limit int) ([]Summary, error) {
	q := `SELECT ` + summaryCols + ` FROM session_summaries s`
	var args []any
	var where []string
	if repoKey != "" {
		q += ` JOIN summary_repos r ON r.session_id = s.session_id`
		where = append(where, `r.repo_key = ?`)
		args = append(args, repoKey)
	}
	for _, w := range strings.Fields(query) {
		where = append(where, `(s.summary LIKE ? ESCAPE '\' OR s.task LIKE ? ESCAPE '\')`)
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(w) + "%"
		args = append(args, like, like)
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY s.created_at DESC, s.session_id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return s.scanSummaries(rows)
}

// SummaryCounts returns how many summaries each repo key has.
func (s *Store) SummaryCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT repo_key, COUNT(*) FROM summary_repos GROUP BY repo_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// DeleteSummary removes a session's summary.
func (s *Store) DeleteSummary(sessionID string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM session_summaries WHERE session_id = ?`, sessionID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

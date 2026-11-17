package store

import "time"

// PullRequest is a pull request a conversation opened, found again by the
// server it is on, its repo and its number.
type PullRequest struct {
	RootID    string // the first session of the conversation
	ServerID  string
	Repo      string // full name on that server
	Number    int
	Branch    string
	Title     string
	URL       string
	CreatedAt time.Time
}

// AddPullRequest records a pull request a conversation opened.
func (s *Store) AddPullRequest(p PullRequest) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO pull_requests (root_id, server_id, repo, number, branch, title, url, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.RootID, p.ServerID, p.Repo, p.Number, p.Branch, p.Title, p.URL, s.now().Unix())
	return err
}

// PullRequests lists a conversation's pull requests, oldest first.
func (s *Store) PullRequests(rootID string) ([]PullRequest, error) {
	rows, err := s.db.Query(`SELECT root_id, server_id, repo, number, branch, title, url, created_at FROM pull_requests WHERE root_id = ? ORDER BY created_at, number`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PullRequest
	for rows.Next() {
		var p PullRequest
		var at int64
		if err := rows.Scan(&p.RootID, &p.ServerID, &p.Repo, &p.Number, &p.Branch, &p.Title, &p.URL, &at); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(at, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

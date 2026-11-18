package store

import "time"

// PullRequest is a pull request a conversation opened, found again by the
// server it is on, its repo and its number.
type PullRequest struct {
	RootID      string // the first session of the conversation
	ServerID    string
	Repo        string // full name on that server
	Number      int
	Branch      string
	Base        string // the branch it was asked to go into; empty means the repo's default
	Title       string
	URL         string
	CreatedAt   time.Time
	FixAttempts int       // times a fix was started by itself for failing checks
	FixSHA      string    // the commit that was last tried (a new commit may be tried again)
	DescribedAt time.Time // when its title and description were last set (the time it was opened, until updated)
}

// AddPullRequest records a pull request a conversation opened.
func (s *Store) AddPullRequest(p PullRequest) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO pull_requests (root_id, server_id, repo, number, branch, base, title, url, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.RootID, p.ServerID, p.Repo, p.Number, p.Branch, p.Base, p.Title, p.URL, s.now().Unix())
	return err
}

// PullRequests lists a conversation's pull requests, oldest first.
func (s *Store) PullRequests(rootID string) ([]PullRequest, error) {
	rows, err := s.db.Query(`SELECT root_id, server_id, repo, number, branch, base, title, url, created_at, fix_attempts, fix_sha, described_at FROM pull_requests WHERE root_id = ? ORDER BY created_at, number`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PullRequest
	for rows.Next() {
		var p PullRequest
		var at, described int64
		if err := rows.Scan(&p.RootID, &p.ServerID, &p.Repo, &p.Number, &p.Branch, &p.Base, &p.Title, &p.URL, &at, &p.FixAttempts, &p.FixSHA, &described); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(at, 0)
		p.DescribedAt = p.CreatedAt
		if described > 0 {
			p.DescribedAt = time.Unix(described, 0)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPullRequestState remembers the last state seen of a pull request (open,
// merged or closed), so how conversations turned out can be counted without asking
// the git servers.
func (s *Store) SetPullRequestState(serverID, repo string, number int, state string) error {
	_, err := s.db.Exec(`UPDATE pull_requests SET state = ? WHERE server_id = ? AND repo = ? AND number = ?`, state, serverID, repo, number)
	return err
}

// RecordFixAttempt notes that a fix was started by itself for a pull request's commit.
func (s *Store) RecordFixAttempt(serverID, repo string, number int, sha string) error {
	_, err := s.db.Exec(`UPDATE pull_requests SET fix_attempts = fix_attempts + 1, fix_sha = ? WHERE server_id = ? AND repo = ? AND number = ?`, sha, serverID, repo, number)
	return err
}

// ConversationsWithOpenPullRequests lists the first sessions of conversations that have a
// pull request not known to be merged or closed.
func (s *Store) ConversationsWithOpenPullRequests() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT root_id FROM pull_requests WHERE state IN ('', 'open')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetPullRequestDescribed records a new title for a pull request and that its description was just set.
func (s *Store) SetPullRequestDescribed(serverID, repo string, number int, title string) error {
	_, err := s.db.Exec(`UPDATE pull_requests SET title = ?, described_at = ? WHERE server_id = ? AND repo = ? AND number = ?`, title, s.now().Unix(), serverID, repo, number)
	return err
}

package store

import (
	"database/sql"
	"fmt"

	"github.com/mistergregg/unire/internal/session"
)

// GitServer is a configured git server. It holds no secret: the token lives
// in the vault under TokenSecret.
type GitServer struct {
	ID                 string
	Name               string
	Type               string
	BaseURL            string
	InsecureSkipVerify bool
	TokenSecret        string
}

const serverCols = `id, name, type, base_url, insecure_skip_verify, token_secret`

func scanServer(sc interface{ Scan(...any) error }) (GitServer, error) {
	var g GitServer
	var insecure int
	err := sc.Scan(&g.ID, &g.Name, &g.Type, &g.BaseURL, &insecure, &g.TokenSecret)
	g.InsecureSkipVerify = insecure != 0
	return g, err
}

// GitServers lists servers in the order they were added.
func (s *Store) GitServers() ([]GitServer, error) {
	rows, err := s.db.Query(`SELECT ` + serverCols + ` FROM git_servers ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitServer
	for rows.Next() {
		g, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GitServer returns one server by ID.
func (s *Store) GitServer(id string) (GitServer, bool, error) {
	g, err := scanServer(s.db.QueryRow(`SELECT `+serverCols+` FROM git_servers WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return GitServer{}, false, nil
	}
	return g, err == nil, err
}

// AddGitServer assigns a unique ID from the name and saves the server.
func (s *Store) AddGitServer(g GitServer) (GitServer, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return g, err
	}
	defer tx.Rollback()
	base := session.Slug(g.Name)
	id := base
	for n := 2; ; n++ {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM git_servers WHERE id = ?`, id).Scan(&exists); err != nil {
			return g, err
		}
		if exists == 0 {
			break
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	g.ID = id
	g.TokenSecret = "git-server-" + id
	insecure := 0
	if g.InsecureSkipVerify {
		insecure = 1
	}
	if _, err := tx.Exec(`INSERT INTO git_servers (`+serverCols+`, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.Name, g.Type, g.BaseURL, insecure, g.TokenSecret, s.now().Unix()); err != nil {
		return g, err
	}
	return g, tx.Commit()
}

// RemoveGitServer deletes a server and returns what was removed.
func (s *Store) RemoveGitServer(id string) (GitServer, bool, error) {
	g, ok, err := s.GitServer(id)
	if err != nil || !ok {
		return g, false, err
	}
	_, err = s.db.Exec(`DELETE FROM git_servers WHERE id = ?`, id)
	return g, err == nil, err
}

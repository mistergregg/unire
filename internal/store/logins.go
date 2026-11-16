package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

// LoginTTL is how long a login lasts without use. It slides forward while you
// keep using the dashboard.
const LoginTTL = 30 * 24 * time.Hour

// touchEvery limits how often a login's last-seen time is written.
const touchEvery = time.Minute

// Login is one logged-in browser or device.
type Login struct {
	ID        string // SHA-256 of the token; safe to show, cannot be used to log in
	CreatedAt time.Time
	LastSeen  time.Time
	UserAgent string
	IP        string
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateLogin starts a login and returns the token for the browser's cookie.
// The database keeps only the token's hash.
func (s *Store) CreateLogin(userAgent, ip string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	now := s.now()
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	if _, err := s.db.Exec(`DELETE FROM login_sessions WHERE expires_at < ?`, now.Unix()); err != nil {
		return "", err
	}
	_, err := s.db.Exec(`INSERT INTO login_sessions (id, created_at, last_seen, expires_at, user_agent, ip) VALUES (?, ?, ?, ?, ?, ?)`,
		hashToken(token), now.Unix(), now.Unix(), now.Add(LoginTTL).Unix(), userAgent, ip)
	return token, err
}

// CheckLogin validates a token. It returns the login's ID and extends the
// login while it is in use.
func (s *Store) CheckLogin(token string) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	id := hashToken(token)
	var lastSeen, expires int64
	err := s.db.QueryRow(`SELECT last_seen, expires_at FROM login_sessions WHERE id = ?`, id).Scan(&lastSeen, &expires)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	now := s.now()
	if now.Unix() >= expires {
		_, _ = s.db.Exec(`DELETE FROM login_sessions WHERE id = ?`, id)
		return "", false, nil
	}
	if now.Sub(time.Unix(lastSeen, 0)) >= touchEvery {
		if _, err := s.db.Exec(`UPDATE login_sessions SET last_seen = ?, expires_at = ? WHERE id = ?`,
			now.Unix(), now.Add(LoginTTL).Unix(), id); err != nil {
			return "", false, err
		}
	}
	return id, true, nil
}

// Logins lists active logins, newest activity first.
func (s *Store) Logins() ([]Login, error) {
	rows, err := s.db.Query(`SELECT id, created_at, last_seen, user_agent, ip FROM login_sessions WHERE expires_at >= ? ORDER BY last_seen DESC`, s.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Login
	for rows.Next() {
		var l Login
		var created, seen int64
		if err := rows.Scan(&l.ID, &created, &seen, &l.UserAgent, &l.IP); err != nil {
			return nil, err
		}
		l.CreatedAt, l.LastSeen = time.Unix(created, 0), time.Unix(seen, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

// RevokeLogin ends one login by ID.
func (s *Store) RevokeLogin(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM login_sessions WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RevokeAllLogins ends every login except keepID (pass "" to end all).
func (s *Store) RevokeAllLogins(keepID string) error {
	_, err := s.db.Exec(`DELETE FROM login_sessions WHERE id != ?`, keepID)
	return err
}

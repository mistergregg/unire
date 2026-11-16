// Package store is Unire's single database: settings, git servers, encrypted
// secrets, login sessions, and later workspaces, coding sessions and memory.
// It is one SQLite file, so backing up means copying one folder.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// migrations are applied in order, once each. Never edit an existing entry:
// add a new one, so upgrading never loses data.
var migrations = []string{
	// 1: foundation
	`CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE TABLE git_servers (
		id                   TEXT PRIMARY KEY,
		name                 TEXT NOT NULL,
		type                 TEXT NOT NULL,
		base_url             TEXT NOT NULL DEFAULT '',
		insecure_skip_verify INTEGER NOT NULL DEFAULT 0,
		token_secret         TEXT NOT NULL,
		created_at           INTEGER NOT NULL
	);
	CREATE TABLE secrets (
		name       TEXT PRIMARY KEY,
		nonce      BLOB NOT NULL,
		data       BLOB NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE login_sessions (
		id         TEXT PRIMARY KEY, -- SHA-256 of the browser's token, never the token
		created_at INTEGER NOT NULL,
		last_seen  INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		user_agent TEXT NOT NULL DEFAULT '',
		ip         TEXT NOT NULL DEFAULT ''
	);`,
	// 2: workspaces and coding sessions
	`CREATE TABLE workspaces (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		repos      TEXT NOT NULL, -- JSON list of repo references
		created_at INTEGER NOT NULL
	);
	CREATE TABLE sessions (
		id          TEXT PRIMARY KEY,
		task        TEXT NOT NULL,
		agent       TEXT NOT NULL,
		mode        TEXT NOT NULL,
		prompt      TEXT NOT NULL,
		branch      TEXT NOT NULL,
		status      TEXT NOT NULL,
		error       TEXT NOT NULL DEFAULT '',
		repos       TEXT NOT NULL, -- JSON list of repo references
		created_at  INTEGER NOT NULL,
		started_at  INTEGER NOT NULL DEFAULT 0,
		finished_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE session_events (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		ts         INTEGER NOT NULL,
		kind       TEXT NOT NULL, -- out (agent output) or log (progress)
		text       TEXT NOT NULL
	);
	CREATE INDEX session_events_by_session ON session_events (session_id, id);`,
}

// DBFileName is the database file inside the data directory.
const DBFileName = "unire.db"

// Store wraps the database.
type Store struct {
	db  *sql.DB
	now func() time.Time // replaced in tests
}

// Open opens or creates the database in dir and applies any new migrations.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, DBFileName) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection keeps SQLite simple and avoids lock contention. Code in
	// this package must finish reading rows before it issues another query.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle to packages that keep their own tables, such as the vault.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	if current > len(migrations) {
		return fmt.Errorf("database is from a newer Unire (schema %d, this build knows %d)", current, len(migrations))
	}
	for v := current + 1; v <= len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v-1]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, s.now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot writes a consistent copy of the database to path.
func (s *Store) Snapshot(path string) error {
	_, err := s.db.Exec(`VACUUM INTO ?`, path)
	return err
}

// ---- settings ----

// Setting returns a setting's value.
func (s *Store) Setting(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetSetting creates or replaces a setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SetSettingIfUnset stores value only when key has none. It reports whether
// it stored it, atomically, so first-run setup can happen only once.
func (s *Store) SetSettingIfUnset(key, value string) (bool, error) {
	res, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`, key, value)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

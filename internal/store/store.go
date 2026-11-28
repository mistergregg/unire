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
	// 3: network mode per session
	`ALTER TABLE sessions ADD COLUMN network TEXT NOT NULL DEFAULT 'internet';`,
	// 4: memory. A brief per repo, and a summary per finished session. Summaries
	// keep their own copy of the session's details so they outlive the session.
	`ALTER TABLE sessions ADD COLUMN memory INTEGER NOT NULL DEFAULT 1;
	CREATE TABLE repo_memory (
		repo_key   TEXT PRIMARY KEY,           -- server id + "/" + full name
		brief      TEXT NOT NULL,
		previous   TEXT NOT NULL DEFAULT '',   -- one step of undo
		session_id TEXT NOT NULL DEFAULT '',   -- the session that last changed it, or '' for an edit by the owner
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE session_summaries (
		session_id TEXT PRIMARY KEY,
		task       TEXT NOT NULL,
		agent      TEXT NOT NULL,
		branch     TEXT NOT NULL,
		status     TEXT NOT NULL,
		summary    TEXT NOT NULL,
		source     TEXT NOT NULL,              -- agent (written by the agent) or fallback (built by Unire)
		created_at INTEGER NOT NULL
	);
	CREATE TABLE summary_repos (
		session_id TEXT NOT NULL REFERENCES session_summaries(session_id) ON DELETE CASCADE,
		repo_key   TEXT NOT NULL,
		PRIMARY KEY (session_id, repo_key)
	);
	CREATE INDEX summary_repos_by_repo ON summary_repos (repo_key);`,
	// 5: a reply continues a conversation: it is a new session whose parent is the one before it
	`ALTER TABLE sessions ADD COLUMN parent_id TEXT NOT NULL DEFAULT '';`,
	// 6: the pull requests a conversation opened
	`CREATE TABLE pull_requests (
		root_id    TEXT NOT NULL,
		server_id  TEXT NOT NULL,
		repo       TEXT NOT NULL,
		number     INTEGER NOT NULL,
		branch     TEXT NOT NULL,
		title      TEXT NOT NULL,
		url        TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (server_id, repo, number)
	);
	CREATE INDEX pull_requests_by_root ON pull_requests (root_id);`,
	// 7: the branch a pull request was asked to go into (empty: the repo's default), so one branch can have several
	`ALTER TABLE pull_requests ADD COLUMN base TEXT NOT NULL DEFAULT '';`,
	// 8: what memory cost and whether it helped: characters of memory a run was given, how long saving it took,
	// and the last state seen of each pull request
	`ALTER TABLE sessions ADD COLUMN memory_chars INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE sessions ADD COLUMN memory_ms INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE pull_requests ADD COLUMN state TEXT NOT NULL DEFAULT '';`,
	// 9: links between conversations (on the first session of each): the one this was started from (a handoff, for
	// information) and a parent it keeps getting updates from, with how many of the parent's finished turns it has seen
	`ALTER TABLE sessions ADD COLUMN forked_from TEXT NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN upstream_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN upstream_seen INTEGER NOT NULL DEFAULT 0;`,
	// 10: fixing a pull request's failing checks by itself (a setting of the conversation, on by default), and how
	// many times that has been tried for each pull request, and for which commit
	`ALTER TABLE sessions ADD COLUMN auto_fix INTEGER NOT NULL DEFAULT 1;
	ALTER TABLE pull_requests ADD COLUMN fix_attempts INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE pull_requests ADD COLUMN fix_sha TEXT NOT NULL DEFAULT '';`,
	// 11: when a pull request's title and description were last set (0: when it was opened), so a later turn that
	// pushes to its branch can offer to update them
	`ALTER TABLE pull_requests ADD COLUMN described_at INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE pull_requests ADD COLUMN merged_at INTEGER NOT NULL DEFAULT 0;`,
	// 12: the agent's own conversation id for each turn (its session or thread), so a reply by the same agent
	// carries that conversation on and remembers everything said and done in it
	`ALTER TABLE sessions ADD COLUMN agent_conv TEXT NOT NULL DEFAULT '';`,
	// 13: the model chosen for the turn (empty: the agent's default)
	`ALTER TABLE sessions ADD COLUMN model TEXT NOT NULL DEFAULT '';`,
	// 14: latest usage limits reported by each agent
	`CREATE TABLE agent_usage (
		agent_id   TEXT PRIMARY KEY,
		reported   INTEGER NOT NULL DEFAULT 1,
		status     TEXT NOT NULL DEFAULT '',
		plan       TEXT NOT NULL DEFAULT '',
		windows    TEXT NOT NULL DEFAULT '[]',
		updated_at INTEGER NOT NULL
	);`,
	// 15: latest model lists discovered from each agent CLI
	`CREATE TABLE agent_models (
		agent_id   TEXT PRIMARY KEY,
		models     TEXT NOT NULL DEFAULT '[]',
		updated_at INTEGER NOT NULL
	);`,
	// 16: automations: a task that runs by itself on a schedule, in a chat of its own (root_id, empty until
	// the first run), and a line for every time it ran or was skipped
	`CREATE TABLE automations (
		id             TEXT PRIMARY KEY,
		name           TEXT NOT NULL,
		prompt         TEXT NOT NULL,
		agent          TEXT NOT NULL,
		model          TEXT NOT NULL DEFAULT '',
		repos          TEXT NOT NULL DEFAULT '[]', -- JSON list of repo references
		every_minutes  INTEGER NOT NULL DEFAULT 0, -- 0: once a day at daily_at
		daily_at       TEXT NOT NULL DEFAULT '',   -- "HH:MM" in the server's time zone
		max_per_day    INTEGER NOT NULL DEFAULT 0, -- 0: no cap
		max_usage      INTEGER NOT NULL DEFAULT 0, -- skip when the agent's usage is at this percent or more; 0: no cap
		may_merge      INTEGER NOT NULL DEFAULT 0,
		enabled        INTEGER NOT NULL DEFAULT 1,
		root_id        TEXT NOT NULL DEFAULT '',
		next_run       INTEGER NOT NULL DEFAULT 0,
		created_at     INTEGER NOT NULL
	);
	CREATE TABLE automation_runs (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		automation_id TEXT NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
		at            INTEGER NOT NULL,
		session_id    TEXT NOT NULL DEFAULT '', -- the turn it started; empty when skipped
		note          TEXT NOT NULL DEFAULT ''  -- why it was skipped, or how it was started
	);
	CREATE INDEX automation_runs_by_automation ON automation_runs (automation_id, at);`,
	// 17: the time zone an automation's daily time is in (an IANA name such as "America/New_York", taken from
	// the owner's browser); empty means the server's own
	`ALTER TABLE automations ADD COLUMN time_zone TEXT NOT NULL DEFAULT '';`,
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

// Counts are how much is in the database, for the health page.
type Counts struct {
	Sessions, Events, Summaries int
}

// Counts returns row counts and checks the database is readable.
func (s *Store) Counts() (Counts, error) {
	var c Counts
	for q, dst := range map[string]*int{
		`SELECT COUNT(*) FROM sessions`:          &c.Sessions,
		`SELECT COUNT(*) FROM session_events`:    &c.Events,
		`SELECT COUNT(*) FROM session_summaries`: &c.Summaries,
	} {
		if err := s.db.QueryRow(q).Scan(dst); err != nil {
			return c, err
		}
	}
	return c, nil
}

// QuickCheck runs SQLite's integrity check (the quick form) and returns what it says
// ("ok" when the database is sound).
func (s *Store) QuickCheck() (string, error) {
	var res string
	err := s.db.QueryRow(`PRAGMA quick_check(1)`).Scan(&res)
	return res, err
}

// Package vault is an encrypted, write-only secret store kept in the Unire
// database. Secrets go in through Set and come out only through Get, which
// only server-side code calls. Nothing in the HTTP API returns a value; the
// UI can see names only.
//
// The master key lives in master.key next to the database (or in
// UNIRE_MASTER_KEY). That protects against leaks of the database file alone,
// such as a copied backup or a log, not against someone who can read the
// whole data directory. On a shared machine, supply the key through the
// environment or a Docker secret.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// KeyFileName is the master key file inside the data directory.
const KeyFileName = "master.key"

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ErrNotFound is returned when a secret does not exist.
var ErrNotFound = errors.New("secret not found")

// Vault stores secrets encrypted with AES-256-GCM.
type Vault struct {
	db   *sql.DB
	aead cipher.AEAD
}

// Open returns a vault that stores its rows in db and keeps its master key in keyDir.
func Open(db *sql.DB, keyDir string) (*Vault, error) {
	key, err := loadKey(keyDir)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{db: db, aead: aead}, nil
}

// KeyInFile reports whether the master key is stored in a file (so a backup
// must include it) and not supplied through the environment.
func KeyInFile() bool { return strings.TrimSpace(os.Getenv("UNIRE_MASTER_KEY")) == "" }

func loadKey(dir string) ([]byte, error) {
	if h := strings.TrimSpace(os.Getenv("UNIRE_MASTER_KEY")); h != "" {
		k, err := hex.DecodeString(h)
		if err != nil || len(k) != 32 {
			return nil, errors.New("UNIRE_MASTER_KEY must be 64 hex characters")
		}
		return k, nil
	}
	p := filepath.Join(dir, KeyFileName)
	if b, err := os.ReadFile(p); err == nil {
		k, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) != 32 {
			return nil, errors.New("master.key is corrupt")
		}
		return k, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(hex.EncodeToString(k)), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// ValidName reports whether name is an acceptable secret name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Set stores or replaces a secret.
func (v *Vault) Set(name, value string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid secret name %q", name)
	}
	if value == "" {
		return errors.New("secret value is empty")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	// The name is authenticated data, so rows cannot be swapped.
	data := v.aead.Seal(nil, nonce, []byte(value), []byte(name))
	_, err := v.db.Exec(`INSERT INTO secrets (name, nonce, data, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET nonce = excluded.nonce, data = excluded.data, updated_at = excluded.updated_at`,
		name, nonce, data, time.Now().Unix())
	return err
}

// Get returns a secret's value. Only server-side code should call it.
func (v *Vault) Get(name string) (string, error) {
	var nonce, data []byte
	err := v.db.QueryRow(`SELECT nonce, data FROM secrets WHERE name = ?`, name).Scan(&nonce, &data)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	b, err := v.aead.Open(nil, nonce, data, []byte(name))
	if err != nil {
		return "", fmt.Errorf("cannot decrypt %q (wrong master key or tampered data)", name)
	}
	return string(b), nil
}

// Has reports whether a secret exists.
func (v *Vault) Has(name string) bool {
	var n int
	return v.db.QueryRow(`SELECT COUNT(*) FROM secrets WHERE name = ?`, name).Scan(&n) == nil && n > 0
}

// Names lists secret names, sorted.
func (v *Vault) Names() []string {
	rows, err := v.db.Query(`SELECT name FROM secrets ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	return names
}

// Delete removes a secret. Deleting a missing secret is not an error.
func (v *Vault) Delete(name string) error {
	_, err := v.db.Exec(`DELETE FROM secrets WHERE name = ?`, name)
	return err
}

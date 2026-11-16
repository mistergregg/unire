package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mistergregg/unire/internal/store"
)

func open(t *testing.T) (*Vault, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := Open(st.DB(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return v, st, dir
}

func TestRoundTripAndPersistence(t *testing.T) {
	v, st, dir := open(t)
	if err := v.Set("gitea-token", "s3cret-value"); err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get("gitea-token"); err != nil || got != "s3cret-value" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	st.Close()
	st2, _ := store.Open(dir)
	defer st2.Close()
	v2, err := Open(st2.DB(), dir) // same key file, same data
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := v2.Get("gitea-token"); got != "s3cret-value" {
		t.Errorf("after reopen got %q", got)
	}
}

func TestValueNotInDatabaseFiles(t *testing.T) {
	v, _, dir := open(t)
	if err := v.Set("tok", "plain-text-marker-12345"); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "unire.db*")) // db plus WAL files
	if len(matches) == 0 {
		t.Fatal("no database files found")
	}
	for _, f := range matches {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "plain-text-marker-12345") {
			t.Errorf("secret value is in clear text in %s", f)
		}
	}
}

func TestNamesDeleteHas(t *testing.T) {
	v, _, _ := open(t)
	_ = v.Set("b", "1")
	_ = v.Set("a", "2")
	if got := v.Names(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Names = %v", got)
	}
	_ = v.Delete("a")
	if v.Has("a") || !v.Has("b") {
		t.Error("Has wrong after delete")
	}
	if _, err := v.Get("a"); err != ErrNotFound {
		t.Errorf("Get missing = %v", err)
	}
}

func TestReplaceKeepsOneRow(t *testing.T) {
	v, _, _ := open(t)
	_ = v.Set("a", "old")
	_ = v.Set("a", "new")
	if got, _ := v.Get("a"); got != "new" || len(v.Names()) != 1 {
		t.Errorf("Get = %q, names = %v", got, v.Names())
	}
}

func TestInvalidInput(t *testing.T) {
	v, _, _ := open(t)
	for _, n := range []string{"", "Bad Name", "../x", "-x"} {
		if err := v.Set(n, "v"); err == nil {
			t.Errorf("Set(%q) should fail", n)
		}
	}
	if err := v.Set("ok", ""); err == nil {
		t.Error("empty value should fail")
	}
}

func TestSwappedRowsFail(t *testing.T) {
	v, st, _ := open(t)
	_ = v.Set("a", "value-a")
	_ = v.Set("b", "value-b")
	// Point name "a" at b's ciphertext.
	if _, err := st.DB().Exec(`UPDATE secrets SET nonce = (SELECT nonce FROM secrets WHERE name='b'), data = (SELECT data FROM secrets WHERE name='b') WHERE name='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get("a"); err == nil {
		t.Error("a swapped row should not decrypt")
	}
}

func TestWrongMasterKeyFails(t *testing.T) {
	v, st, dir := open(t)
	_ = v.Set("a", "value-a")
	_ = os.Remove(filepath.Join(dir, KeyFileName))
	v2, err := Open(st.DB(), dir) // generates a new key
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.Get("a"); err == nil {
		t.Error("a different master key must not decrypt")
	}
}

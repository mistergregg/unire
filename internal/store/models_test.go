package store

import (
	"slices"
	"testing"
	"time"
)

func TestModelsStoreAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1794870000, 0)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }

	if _, _, ok, err := s.AgentModels("antigravity"); err != nil || ok {
		t.Fatalf("empty AgentModels = %v, %v", ok, err)
	}

	want := []string{"gemini-4-hydrogen-high", "gemini-3.8-flash-high"}
	if err := s.SaveModels("antigravity", want, now.Unix()); err != nil {
		t.Fatal(err)
	}
	got, at, ok, err := s.AgentModels("antigravity")
	if err != nil || !ok || at != now.Unix() || !slices.Equal(got, want) {
		t.Fatalf("AgentModels = %v, %d, %v, %v", got, at, ok, err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()

	got2, at2, ok, err := s2.AgentModels("antigravity")
	if err != nil || !ok || at2 != now.Unix() || !slices.Equal(got2, want) {
		t.Fatalf("after reopen = %v, %d, %v, %v", got2, at2, ok, err)
	}

	want2 := []string{"gemini-4-hydrogen-xhigh", "gemini-4-hydrogen-high"}
	if err := s2.SaveModels("antigravity", want2, now.Unix()+120); err != nil {
		t.Fatal(err)
	}
	got3, at3, ok, err := s2.AgentModels("antigravity")
	if err != nil || !ok || at3 != now.Unix()+120 || !slices.Equal(got3, want2) {
		t.Fatalf("after update = %v, %d, %v, %v", got3, at3, ok, err)
	}
}

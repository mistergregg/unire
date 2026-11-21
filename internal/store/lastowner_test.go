package store

import (
	"testing"
	"time"
)

func TestLastOwnerEventIsWhenTheOwnerLastSpokeToARunningSession(t *testing.T) {
	s, _ := open(t)
	clock := time.Unix(1795000000, 0)
	s.now = func() time.Time { return clock }
	if err := s.CreateSession(Session{ID: "a", Task: "t", Agent: "claude", Mode: "hands-off", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LastOwnerEvent("a"); err != nil || !got.IsZero() {
		t.Fatalf("never spoken to: %v, %v", got, err)
	}
	// What the agent does is not the owner acting.
	clock = clock.Add(time.Minute)
	s.AddEvent("a", "log", "running claude")
	s.AddEvent("a", "out", "working")
	if got, _ := s.LastOwnerEvent("a"); !got.IsZero() {
		t.Errorf("agent output counted as the owner acting: %v", got)
	}
	// A message and an answer are.
	clock = clock.Add(time.Minute)
	s.AddEvent("a", "steer", "id1\nnext\nalso add tests")
	if got, _ := s.LastOwnerEvent("a"); !got.Equal(clock) {
		t.Errorf("after a message: %v, want %v", got, clock)
	}
	clock = clock.Add(time.Minute)
	s.AddEvent("a", "answer", "q1\nyes")
	clock = clock.Add(time.Minute)
	s.AddEvent("a", "out", "done")
	if got, _ := s.LastOwnerEvent("a"); !got.Equal(clock.Add(-time.Minute)) {
		t.Errorf("after an answer: %v, want %v", got, clock.Add(-time.Minute))
	}
	// Each session has its own.
	s.CreateSession(Session{ID: "b", Task: "t", Agent: "claude", Mode: "hands-off", Prompt: "p"})
	if got, _ := s.LastOwnerEvent("b"); !got.IsZero() {
		t.Errorf("another session's message leaked: %v", got)
	}
}

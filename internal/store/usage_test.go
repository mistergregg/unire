package store

import (
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/agent"
)

func TestUsageStoreAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1794870000, 0)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }

	// Initially empty
	_, ok, err := s.AgentUsage("claude")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false for unknown agent")
	}

	u := agent.AgentUsage{
		AgentID:  "claude",
		Reported: true,
		Status:   "allowed",
		Plan:     "pro",
		Windows: []agent.UsageWindow{
			{Name: "5-hour", DurationMins: 300, UsedPercent: 34, ResetsAt: 1794879000},
			{Name: "7-day", DurationMins: 10080, UsedPercent: 72, ResetsAt: 1795089600},
		},
		UpdatedAt: now.Unix(),
	}

	if err := s.SaveUsage(u); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.AgentUsage("claude")
	if err != nil || !ok {
		t.Fatalf("AgentUsage(claude) = %v, %v, %v", got, ok, err)
	}
	if got.AgentID != "claude" || !got.Reported || got.Status != "allowed" || len(got.Windows) != 2 {
		t.Errorf("got usage = %+v", got)
	}
	if got.Windows[0].Name != "5-hour" || got.Windows[0].UsedPercent != 34 || got.Windows[0].DurationMins != 300 {
		t.Errorf("window[0] = %+v", got.Windows[0])
	}
	if got.Windows[1].Name != "7-day" || got.Windows[1].UsedPercent != 72 || got.Windows[1].DurationMins != 10080 {
		t.Errorf("window[1] = %+v", got.Windows[1])
	}

	// Close and reopen database: verifies persistence across restarts
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	got2, ok, err := s2.AgentUsage("claude")
	if err != nil || !ok {
		t.Fatalf("after reopen: AgentUsage(claude) = %v, %v, %v", got2, ok, err)
	}
	if got2.AgentID != "claude" || got2.Windows[0].UsedPercent != 34 {
		t.Errorf("after reopen: got = %+v", got2)
	}

	// Test update (UPSERT)
	uUpdated := u
	uUpdated.Windows[0].UsedPercent = 50
	uUpdated.UpdatedAt = now.Unix() + 60
	if err := s2.SaveUsage(uUpdated); err != nil {
		t.Fatal(err)
	}

	got3, ok, err := s2.AgentUsage("claude")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got3.Windows[0].UsedPercent != 50 || got3.UpdatedAt != now.Unix()+60 {
		t.Errorf("after update: got = %+v", got3)
	}

	// Test AllUsage
	uCodex := agent.AgentUsage{
		AgentID:  "codex",
		Reported: true,
		Status:   "allowed",
		Plan:     "free",
		Windows: []agent.UsageWindow{
			{Name: "30-day", DurationMins: 43200, UsedPercent: 13, ResetsAt: 1797607630},
		},
		UpdatedAt: now.Unix() + 10,
	}
	if err := s2.SaveUsage(uCodex); err != nil {
		t.Fatal(err)
	}

	all, err := s2.AllUsage()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("AllUsage len = %d; want 2", len(all))
	}
	if all["codex"].Windows[0].Name != "30-day" {
		t.Errorf("all[codex] = %+v", all["codex"])
	}
}

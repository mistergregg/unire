package store

import (
	"strings"
	"testing"
)

func TestRepoMemoryUndoAndDelete(t *testing.T) {
	s, _ := open(t)
	if _, ok, _ := s.RepoMemory("g/me/api"); ok {
		t.Fatal("no memory yet")
	}
	_ = s.SetRepoMemory("g/me/api", "brief one", "s1")
	_ = s.SetRepoMemory("g/me/api", "brief two", "s2")
	m, ok, _ := s.RepoMemory("g/me/api")
	if !ok || m.Brief != "brief two" || m.Previous != "brief one" || m.SessionID != "s2" {
		t.Fatalf("memory = %+v", m)
	}
	if ok, _ := s.RestoreRepoMemory("g/me/api"); !ok {
		t.Fatal("restore failed")
	}
	m, _, _ = s.RepoMemory("g/me/api")
	if m.Brief != "brief one" || m.Previous != "brief two" || m.SessionID != "" {
		t.Errorf("after restore = %+v", m)
	}
	if ok, _ := s.RestoreRepoMemory("g/other"); ok {
		t.Error("restore with nothing previous should report false")
	}
	if list, _ := s.RepoMemories(); len(list) != 1 {
		t.Errorf("RepoMemories = %d", len(list))
	}
	if ok, _ := s.DeleteRepoMemory("g/me/api"); !ok {
		t.Error("delete failed")
	}
	if _, ok, _ := s.RepoMemory("g/me/api"); ok {
		t.Error("still there")
	}
}

func TestSummariesLinkSearchAndOutliveSessions(t *testing.T) {
	s, _ := open(t)
	// A session, its summary, then the session is deleted: the summary stays.
	_ = s.CreateSession(Session{ID: "s1", Task: "login", Agent: "claude", Mode: "m", Prompt: "p", Branch: "agent/claude/login"})
	_ = s.MarkFinished("s1", StatusDone, "")
	err := s.SaveSummary(Summary{SessionID: "s1", Task: "Add login", Agent: "claude", Branch: "agent/claude/login", Status: "done",
		Summary: "## Done\nAdded OAuth login with 100% coverage_check", Source: "agent", Repos: []string{"g/me/api", "g/me/web"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SaveSummary(Summary{SessionID: "s2", Task: "Fix css", Agent: "claude", Branch: "b", Status: "done",
		Summary: "## Done\nFixed the button colour", Source: "fallback", Repos: []string{"g/me/web"}})
	if ok, _ := s.DeleteSession("s1"); !ok {
		t.Fatal("delete session")
	}

	x, ok, _ := s.Summary("s1")
	if !ok || x.Source != "agent" || len(x.Repos) != 2 {
		t.Fatalf("summary after session delete = %+v", x)
	}
	api, _ := s.RepoSummaries("g/me/api", 10)
	web, _ := s.RepoSummaries("g/me/web", 10)
	if len(api) != 1 || len(web) != 2 {
		t.Errorf("api=%d web=%d summaries", len(api), len(web))
	}
	for q, want := range map[string]int{"oauth login": 1, "button": 1, "login button": 0, "": 2, "100%": 1, "coverage_check": 1, "50%": 0} {
		got, _ := s.SearchSummaries(q, "", 10)
		if len(got) != want {
			t.Errorf("search %q = %d results, want %d", q, len(got), want)
		}
	}
	if got, _ := s.SearchSummaries("", "g/me/api", 10); len(got) != 1 {
		t.Errorf("search within repo = %d", len(got))
	}
	if ok, _ := s.DeleteSummary("s1"); !ok {
		t.Error("delete summary")
	}
	if got, _ := s.RepoSummaries("g/me/api", 10); len(got) != 0 {
		t.Error("links should go with the summary")
	}
}

func TestSessionMemoryFlagRoundTrips(t *testing.T) {
	s, _ := open(t)
	_ = s.CreateSession(Session{ID: "a", Task: "t", Agent: "x", Mode: "m", Prompt: "p", Branch: "b", Memory: true})
	_ = s.CreateSession(Session{ID: "b", Task: "t", Agent: "x", Mode: "m", Prompt: "p", Branch: "b"})
	a, _, _ := s.Session("a")
	b, _, _ := s.Session("b")
	if !a.Memory || b.Memory {
		t.Errorf("memory flags: a=%v b=%v", a.Memory, b.Memory)
	}
}

func TestRepoKey(t *testing.T) {
	if k := RepoKey(RepoRef{ServerID: "home", FullName: "me/api"}); k != "home/me/api" || !strings.HasPrefix(k, "home/") {
		t.Errorf("RepoKey = %q", k)
	}
}

func TestEventsByKindPushEventsAndLastOwnerEventsAfterHeavyLogging(t *testing.T) {
	s, _ := open(t)
	if err := s.CreateSession(Session{ID: "s1", Task: "t", Agent: "claude", Mode: "hands-off", Prompt: "p", Branch: "b"}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO session_events (session_id, ts, kind, text) VALUES ('s1', 1, 'log', 'noisy progress line')`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5100; i++ {
		if _, err := stmt.Exec(); err != nil {
			t.Fatal(err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []struct{ kind, text string }{
		{"log", "api: pushed agent/claude/b"},
		{"steer", "mid-turn guidance"},
		{"ask", `{"id":"q1","question":"keep going?"}`},
		{"answer", "yes"},
		{"out", "final answer"},
	} {
		if err := s.AddEvent("s1", ev.kind, ev.text); err != nil {
			t.Fatal(err)
		}
	}

	byKind, err := s.EventsByKind("s1", "out", "steer", "answer")
	if err != nil {
		t.Fatal(err)
	}
	if len(byKind) != 3 {
		t.Fatalf("EventsByKind len = %d; want 3 (%+v)", len(byKind), byKind)
	}
	pushes, err := s.PushEvents("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pushes) != 1 || !strings.Contains(pushes[0].Text, ": pushed ") {
		t.Errorf("PushEvents = %+v", pushes)
	}
	owners, err := s.LastOwnerEvents([]string{"s1", "s2"})
	if err != nil {
		t.Fatal(err)
	}
	if owners["s1"].IsZero() {
		t.Errorf("LastOwnerEvents missing s1: %+v", owners)
	}
}

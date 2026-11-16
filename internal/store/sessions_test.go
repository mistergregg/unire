package store

import "testing"

func TestWorkspaces(t *testing.T) {
	s, _ := open(t)
	refs := []RepoRef{{ServerID: "g", FullName: "me/api", CloneURL: "https://g/me/api.git", Base: "main"}}
	if _, err := s.SaveWorkspace("My App", refs); err != nil {
		t.Fatal(err)
	}
	// Same name replaces.
	refs2 := append(refs, RepoRef{ServerID: "g", FullName: "me/web", CloneURL: "https://g/me/web.git"})
	if _, err := s.SaveWorkspace("My App", refs2); err != nil {
		t.Fatal(err)
	}
	list, _ := s.Workspaces()
	if len(list) != 1 || list[0].ID != "my-app" || len(list[0].Repos) != 2 || list[0].Repos[0].Base != "main" {
		t.Fatalf("workspaces = %+v", list)
	}
	if ok, _ := s.DeleteWorkspace("my-app"); !ok {
		t.Error("delete failed")
	}
	if list, _ := s.Workspaces(); len(list) != 0 {
		t.Error("workspace still there")
	}
}

func TestSessionLifecycleAndEvents(t *testing.T) {
	s, _ := open(t)
	x := Session{ID: "abc", Task: "t", Agent: "claude", Mode: "hands-off", Prompt: "p", Branch: "agent/claude/t",
		Repos: []RepoRef{{ServerID: "g", FullName: "me/api"}}}
	if err := s.CreateSession(x); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s.Session("abc")
	if !ok || got.Status != StatusQueued || got.Repos[0].FullName != "me/api" || !got.StartedAt.IsZero() {
		t.Fatalf("created = %+v", got)
	}
	_ = s.MarkRunning("abc")
	_ = s.AddEvent("abc", "log", "one")
	_ = s.AddEvent("abc", "out", "two")
	_ = s.AddEvent("abc", "out", "three")
	ev, _ := s.Events("abc", 0, 100)
	if len(ev) != 3 || ev[0].Text != "one" || ev[2].Text != "three" {
		t.Fatalf("events = %+v", ev)
	}
	if more, _ := s.Events("abc", ev[1].ID, 100); len(more) != 1 || more[0].Text != "three" {
		t.Errorf("after cursor = %+v", more)
	}
	if ok, _ := s.DeleteSession("abc"); ok {
		t.Error("a running session must not be deletable")
	}
	if err := s.MarkFinished("abc", StatusRunning, ""); err == nil {
		t.Error("MarkFinished should reject a non-final status")
	}
	_ = s.MarkFinished("abc", StatusDone, "")
	got, _, _ = s.Session("abc")
	if got.Status != StatusDone || got.StartedAt.IsZero() || got.FinishedAt.IsZero() {
		t.Errorf("finished = %+v", got)
	}
	if ok, _ := s.DeleteSession("abc"); !ok {
		t.Error("finished session should be deletable")
	}
	if ev, _ := s.Events("abc", 0, 10); len(ev) != 0 {
		t.Error("events should be deleted with the session")
	}
}

func TestInterruptUnfinished(t *testing.T) {
	s, _ := open(t)
	for _, id := range []string{"a", "b", "c"} {
		_ = s.CreateSession(Session{ID: id, Task: "t", Agent: "claude", Mode: "m", Prompt: "p", Branch: "b"})
	}
	_ = s.MarkRunning("b")
	_ = s.MarkFinished("c", StatusDone, "")
	n, _ := s.InterruptUnfinished()
	if n != 2 {
		t.Errorf("interrupted %d, want 2", n)
	}
	for id, want := range map[string]string{"a": StatusInterrupted, "b": StatusInterrupted, "c": StatusDone} {
		if x, _, _ := s.Session(id); x.Status != want {
			t.Errorf("%s status = %s, want %s", id, x.Status, want)
		}
	}
}

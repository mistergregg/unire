package store

import (
	"testing"
	"time"
)

func TestPullRequestsAreKeptPerConversation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AddPullRequest(PullRequest{RootID: "a", ServerID: "g", Repo: "o/r", Number: 1, Branch: "b", Title: "T", URL: "http://x/1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPullRequest(PullRequest{RootID: "b", ServerID: "g", Repo: "o/r", Number: 2, Branch: "c", Title: "U", URL: "http://x/2"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PullRequests("a")
	if err != nil || len(got) != 1 || got[0].Number != 1 || got[0].Title != "T" || got[0].State != "open" {
		t.Fatalf("PullRequests(a) = %+v, %v", got, err)
	}
	all, err := s.AllPullRequests()
	if err != nil || len(all) != 2 {
		t.Fatalf("AllPullRequests() = %+v, %v", all, err)
	}
	if got, _ := s.PullRequests("none"); len(got) != 0 {
		t.Errorf("another conversation has none: %+v", got)
	}
}

func TestMemoryStatsCompareRunsWithAndWithoutMemory(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mk := func(id string, memory bool, status string, chars int, saveMS int) {
		t.Helper()
		if err := s.CreateSession(Session{ID: id, Task: "t", Agent: "claude", Mode: "hands-off", Prompt: "p", Branch: "b", Memory: memory}); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRunning(id); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkFinished(id, status, ""); err != nil {
			t.Fatal(err)
		}
		if chars > 0 {
			s.SetMemoryStats(id, chars, time.Duration(saveMS)*time.Millisecond)
		}
	}
	mk("m1", true, StatusDone, 4000, 20000)
	mk("m2", true, StatusFailed, 6000, 0)
	mk("n1", false, StatusDone, 0, 0)
	s.AddPullRequest(PullRequest{RootID: "m1", ServerID: "g", Repo: "o/r", Number: 1, Branch: "b", Title: "T", URL: "u"})
	s.AddPullRequest(PullRequest{RootID: "m1", ServerID: "g", Repo: "o/r", Number: 2, Branch: "c", Title: "T", URL: "u"})
	s.AddPullRequest(PullRequest{RootID: "n1", ServerID: "g", Repo: "o/r", Number: 3, Branch: "d", Title: "T", URL: "u"})
	s.SetPullRequestState("g", "o/r", 1, "merged")
	s.SetPullRequestState("g", "o/r", 2, "closed")
	s.SetPullRequestState("g", "o/r", 3, "open")

	stats, err := s.MemoryStats(time.Now().AddDate(0, 0, -1))
	if err != nil || len(stats) != 2 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	on, off := stats[0], stats[1]
	if !on.Memory || on.Runs != 2 || on.Failed != 1 || on.AvgInjectedChars != 5000 || on.AvgSaveSeconds != 20 {
		t.Errorf("memory on: %+v", on)
	}
	if on.PRs != 2 || on.Merged != 1 || on.Closed != 1 || on.Open != 0 {
		t.Errorf("pull requests with memory: %+v", on)
	}
	if off.Memory || off.Runs != 1 || off.PRs != 1 || off.Open != 1 || off.AvgInjectedChars != 0 {
		t.Errorf("memory off: %+v", off)
	}
	if later, _ := s.MemoryStats(time.Now().Add(time.Hour)); later[0].Runs != 0 || later[1].Runs != 0 {
		t.Errorf("nothing is newer than the future: %+v", later)
	}
}

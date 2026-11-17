package store

import "testing"

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
	if err != nil || len(got) != 1 || got[0].Number != 1 || got[0].Title != "T" {
		t.Fatalf("PullRequests(a) = %+v, %v", got, err)
	}
	if got, _ := s.PullRequests("none"); len(got) != 0 {
		t.Errorf("another conversation has none: %+v", got)
	}
}

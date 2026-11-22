package pulls

import (
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/gitprovider"
	"github.com/mistergregg/unire/internal/store"
)

func TestCoveredMergedPullRequestOnlyCoversEarlierPushes(t *testing.T) {
	opened := time.Unix(1000, 0)
	p := pushed{"s", "o/r", "agent/x", ""}
	rec := func(state string) []store.PullRequest {
		return []store.PullRequest{{ServerID: "s", Repo: "o/r", Branch: "agent/x", Base: "main", State: state, CreatedAt: opened}}
	}
	before := store.Session{CreatedAt: opened.Add(-time.Minute)}
	after := store.Session{CreatedAt: opened.Add(time.Hour)}
	cases := []struct {
		name  string
		have  []store.PullRequest
		turn  store.Session
		wants bool
	}{
		{"open, later push", rec("open"), after, true},
		{"merged, push before it opened", rec(gitprovider.PRMerged), before, true},
		{"merged, push after it opened", rec(gitprovider.PRMerged), after, false},
		{"none", nil, after, false},
	}
	for _, c := range cases {
		if got := covered(c.have, p, "main", c.turn); got != c.wants {
			t.Errorf("%s: covered = %v, want %v", c.name, got, c.wants)
		}
	}
}

package server

import (
	"context"
	"strings"
	"testing"
)

func (f *fakeCI) setBehind(n int, conflict bool) {
	f.svc.ClearCache()
	f.mu.Lock()
	f.behind, f.conflict, f.noChecks = n, conflict, true // no checks, so only the update is in play
	f.mu.Unlock()
}

func (f *fakeCI) updateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updates
}

func TestPullRequestBehindItsBaseIsUpdatedByItself(t *testing.T) {
	e, ci, _ := autoFixEnv(t)
	ci.setBehind(3, false)
	if n := e.srv.autoFixOnce(context.Background()); n != 0 {
		t.Fatalf("a clean update needs no agent turn, started %d", n)
	}
	if ci.updateCount() != 1 {
		t.Fatalf("the branch should be updated once, got %d", ci.updateCount())
	}
	// Now current: nothing more to do, and no second update.
	e.srv.autoFixOnce(context.Background())
	if ci.updateCount() != 1 {
		t.Fatalf("a branch that is current is left alone, updates=%d", ci.updateCount())
	}
}

func TestPullRequestThatIsCurrentIsNotUpdated(t *testing.T) {
	e, ci, _ := autoFixEnv(t)
	ci.setBehind(0, false)
	e.srv.autoFixOnce(context.Background())
	if ci.updateCount() != 0 {
		t.Fatalf("nothing to bring in, updates=%d", ci.updateCount())
	}
}

func TestConflictWithTheBaseStartsATurnForTheAgentTwiceAtMost(t *testing.T) {
	e, ci, _ := autoFixEnv(t)
	ci.setBehind(2, true)
	ctx := context.Background()
	for try := 1; try <= maxKeepTries; try++ {
		if n := e.srv.autoFixOnce(ctx); n != 1 {
			t.Fatalf("try %d: a conflict starts a turn, started %d", try, n)
		}
		turn := lastTurn(t, e)
		for _, want := range []string{"fallen behind main", "cannot be updated automatically", "git merge origin/main", "do not just pick one side"} {
			if !strings.Contains(turn.Prompt, want) {
				t.Errorf("the prompt should contain %q:\n%s", want, turn.Prompt)
			}
		}
		e.waitStatus(turn.ID, "done")
	}
	if n := e.srv.autoFixOnce(ctx); n != 0 {
		t.Fatalf("after %d tries it stops, started %d", maxKeepTries, n)
	}
	if ci.updateCount() != 0 {
		t.Fatal("a conflicting branch must never be reported as updated")
	}
}

func TestBranchUpdatesAreOffWhenTheSwitchIsOff(t *testing.T) {
	e, ci, _ := autoFixEnv(t)
	ci.setBehind(3, false)
	e.do("POST", "/api/sessions/c1/autofix", map[string]any{"on": false})
	e.srv.autoFixOnce(context.Background())
	if ci.updateCount() != 0 {
		t.Fatalf("with the switch off nothing is updated, updates=%d", ci.updateCount())
	}
}

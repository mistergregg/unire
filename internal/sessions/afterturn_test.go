package sessions

import (
	"context"
	"strings"
	"testing"

	"github.com/mistergregg/unire/internal/store"
)

func TestAfterTurnRunsOnceTheWorkIsPushedAndIsLogged(t *testing.T) {
	sb := &fakeSandbox{agentOutput: "done", heads: 0}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var called []string
	m, err := New(st, func() Config {
		return Config{Runner: &fakeRunner{sb: sb}, AutoPR: true, AfterTurn: func(_ context.Context, id string) []string {
			called = append(called, id)
			return []string{"opened pull request #3 in me/api"}
		}}
	})
	if err != nil {
		t.Fatal(err)
	}
	x, err := m.Start(req(""))
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if len(called) != 1 || called[0] != x.ID {
		t.Fatalf("AfterTurn calls = %v", called)
	}
	if log := logText(st, x.ID); !strings.Contains(log, "log: opened pull request #3 in me/api") {
		t.Errorf("the line is logged:\n%s", log)
	}
}

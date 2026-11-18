package sessions

import (
	"strings"
	"testing"
)

func TestAMessageToARunningAgentIsLoggedAndDelivered(t *testing.T) {
	sb := &fakeSandbox{agentOutput: "working on it", block: make(chan struct{})}
	m, st := setup(t, sb)
	x, err := m.Start(req(""))
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(t, st, x.ID)

	id, err := m.Steer(x.ID, "also add tests", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	log := logText(st, x.ID)
	for _, want := range []string{"steer: " + id + "\nnext\nalso add tests", "steerstate: " + id + "\nqueued"} {
		if !strings.Contains(log, want) {
			t.Errorf("log is missing %q:\n%s", want, log)
		}
	}
	close(sb.block) // the agent goes on
	m.Wait()
	if log := logText(st, x.ID); !strings.Contains(log, "steerstate: "+id+"\ndelivered") {
		t.Errorf("the agent took the message, so it should be marked delivered:\n%s", log)
	}
	if got, _, _ := st.Session(x.ID); got.Status != "done" {
		t.Errorf("status = %s", got.Status)
	}
}

func TestSendNowIsLoggedAsNow(t *testing.T) {
	sb := &fakeSandbox{agentOutput: "x", block: make(chan struct{})}
	m, st := setup(t, sb)
	x, _ := m.Start(req(""))
	waitRunning(t, st, x.ID)
	id, err := m.Steer(x.ID, "stop and do X", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	close(sb.block)
	m.Wait()
	if log := logText(st, x.ID); !strings.Contains(log, "steer: "+id+"\nnow\nstop and do X") {
		t.Errorf("log:\n%s", log)
	}
}

func TestSteeringNeedsARunningSession(t *testing.T) {
	m, st := setup(t, &fakeSandbox{})
	if _, err := m.Steer("nope", "hi", nil, false); err == nil || !strings.Contains(err.Error(), "no such session") {
		t.Errorf("unknown session: %v", err)
	}
	x, _ := m.Start(req(""))
	m.Wait()
	if _, err := m.Steer(x.ID, "too late", nil, false); err == nil || !strings.Contains(err.Error(), "finished") {
		t.Errorf("a finished session should say so: %v", err)
	}
	_ = st
}

func TestMessagesTheAgentNeverReadAreMarkedFailed(t *testing.T) {
	// The agent ends (stopped) with a message still waiting: it was never delivered.
	sb := &fakeSandbox{block: make(chan struct{})}
	m, st := setup(t, sb)
	x, _ := m.Start(req(""))
	waitRunning(t, st, x.ID)
	var ids []string
	for i := 0; i < 3; i++ {
		id, err := m.Steer(x.ID, "message", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	m.Stop(x.ID)
	m.Wait()
	log := logText(st, x.ID)
	failed := 0
	for _, id := range ids {
		if strings.Contains(log, "steerstate: "+id+"\nfailed") {
			failed++
		}
	}
	if failed == 0 {
		t.Errorf("messages that were never read should be marked failed:\n%s", log)
	}
}

func TestAWaitingMessageCanBePromoted(t *testing.T) {
	sb := &fakeSandbox{agentOutput: "x", block: make(chan struct{})}
	m, st := setup(t, sb)
	x, _ := m.Start(req(""))
	waitRunning(t, st, x.ID)
	id, err := m.Steer(x.ID, "do this sooner", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Promote(x.ID, id); err != nil {
		t.Fatal(err)
	}
	if log := logText(st, x.ID); !strings.Contains(log, "steerstate: "+id+"\npromoting") {
		t.Errorf("the page is told it is being promoted:\n%s", log)
	}
	close(sb.block)
	m.Wait()
	if err := m.Promote(x.ID, id); err == nil {
		t.Error("a finished session has no agent to promote a message for")
	}
}

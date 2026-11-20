package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/agent"
)

func TestUsageAPIAuthenticationRequired(t *testing.T) {
	e := newEnv(t)
	// Not set up or logged in
	code, _ := e.do("GET", "/api/usage", nil)
	if code != 401 {
		t.Fatalf("GET /api/usage without auth = %d; want 401", code)
	}
}

func TestUsageAPIDefaultsAndAntigravity(t *testing.T) {
	e := newEnv(t)
	e.setup()

	code, body := e.do("GET", "/api/usage", nil)
	if code != 200 {
		t.Fatalf("GET /api/usage = %d %s", code, body)
	}

	var list []agent.AgentUsage
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	byID := map[string]agent.AgentUsage{}
	for _, u := range list {
		byID[u.AgentID] = u
	}

	// Must include all three agents
	for _, id := range []string{"antigravity", "claude", "codex"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing agent %q in /api/usage", id)
		}
	}

	// Antigravity does not report usage limits
	agy := byID["antigravity"]
	if agy.Reported {
		t.Errorf("antigravity Reported = true; want false")
	}
	if agy.Status != "not reported" {
		t.Errorf("antigravity Status = %q; want 'not reported'", agy.Status)
	}
	if agy.Stale {
		t.Errorf("antigravity Stale = true; want false (not reported is not stale)")
	}
	if len(agy.Windows) != 0 {
		t.Errorf("antigravity Windows = %+v; want empty", agy.Windows)
	}

	// Claude without reading is reported=true, stale=true
	claude := byID["claude"]
	if !claude.Reported {
		t.Errorf("claude Reported = false; want true")
	}
	if !claude.Stale {
		t.Errorf("claude without reading Stale = false; want true")
	}
}

func TestUsageAPIStoresAndCalculatesStaleness(t *testing.T) {
	e := newEnv(t)
	e.setup()

	baseTime := time.Unix(1794870000, 0)
	e.srv.now = func() time.Time { return baseTime }

	// Store a real reading for Claude
	reading := agent.AgentUsage{
		AgentID:  "claude",
		Reported: true,
		Status:   "allowed",
		Plan:     "pro",
		Windows: []agent.UsageWindow{
			{Name: "5-hour", DurationMins: 300, UsedPercent: 34, ResetsAt: 1794879000},
			{Name: "7-day", DurationMins: 10080, UsedPercent: 72, ResetsAt: 1795089600},
		},
		UpdatedAt: baseTime.Unix(),
	}
	if err := e.srv.store.SaveUsage(reading); err != nil {
		t.Fatal(err)
	}

	// Store a reading for Codex
	codexReading := agent.AgentUsage{
		AgentID:  "codex",
		Reported: true,
		Status:   "allowed",
		Plan:     "free",
		Windows: []agent.UsageWindow{
			{Name: "30-day", DurationMins: 43200, UsedPercent: 13, ResetsAt: 1797607630},
		},
		UpdatedAt: baseTime.Unix(),
	}
	if err := e.srv.store.SaveUsage(codexReading); err != nil {
		t.Fatal(err)
	}

	// Check /api/usage when fresh (5 minutes later)
	e.srv.now = func() time.Time { return baseTime.Add(5 * time.Minute) }
	code, body := e.do("GET", "/api/usage", nil)
	if code != 200 {
		t.Fatalf("GET /api/usage = %d %s", code, body)
	}

	var list []agent.AgentUsage
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	byID := map[string]agent.AgentUsage{}
	for _, u := range list {
		byID[u.AgentID] = u
	}

	cl := byID["claude"]
	if cl.Stale {
		t.Errorf("claude at 5m Stale = true; want false")
	}
	if len(cl.Windows) != 2 || cl.Windows[0].UsedPercent != 34 || cl.Windows[1].UsedPercent != 72 {
		t.Errorf("claude windows = %+v", cl.Windows)
	}

	cx := byID["codex"]
	if cx.Stale {
		t.Errorf("codex at 5m Stale = true; want false")
	}
	if len(cx.Windows) != 1 || cx.Windows[0].UsedPercent != 13 || cx.Windows[0].Name != "30-day" {
		t.Errorf("codex windows = %+v", cx.Windows)
	}

	// Advance time to 15 minutes later (> 10 minutes): readings become stale
	e.srv.now = func() time.Time { return baseTime.Add(15 * time.Minute) }
	code, body = e.do("GET", "/api/usage", nil)
	if code != 200 {
		t.Fatalf("GET /api/usage at 15m = %d %s", code, body)
	}
	json.Unmarshal([]byte(body), &list)
	for _, u := range list {
		byID[u.AgentID] = u
	}
	if !byID["claude"].Stale {
		t.Errorf("claude at 15m Stale = false; want true")
	}
	if byID["antigravity"].Stale {
		t.Errorf("antigravity Stale = true; want false")
	}

	// Check that GET /api/agents includes the same usage data
	code, body = e.do("GET", "/api/agents", nil)
	if code != 200 {
		t.Fatalf("GET /api/agents = %d %s", code, body)
	}
	var agentViews []map[string]any
	json.Unmarshal([]byte(body), &agentViews)
	for _, av := range agentViews {
		id := av["id"].(string)
		uMap, ok := av["usage"].(map[string]any)
		if !ok {
			t.Fatalf("agent %s missing usage in /api/agents", id)
		}
		if uMap["agentId"] != id {
			t.Errorf("agent %s usage.agentId = %v", id, uMap["agentId"])
		}
		if id == "claude" {
			wins, _ := uMap["windows"].([]any)
			if len(wins) != 2 {
				t.Errorf("claude windows in /api/agents = %v; want 2", wins)
			}
			stale, _ := uMap["stale"].(bool)
			if !stale {
				t.Errorf("claude stale in /api/agents = %v; want true", stale)
			}
		}
	}

	// Restart server and verify readings persist
	e.restart()
	e.srv.now = func() time.Time { return baseTime.Add(5 * time.Minute) }
	code, body = e.do("GET", "/api/usage", nil)
	if code != 200 {
		t.Fatalf("after restart GET /api/usage = %d %s", code, body)
	}
	json.Unmarshal([]byte(body), &list)
	byID = map[string]agent.AgentUsage{}
	for _, u := range list {
		byID[u.AgentID] = u
	}
	if byID["claude"].Windows[0].UsedPercent != 34 || byID["claude"].Stale {
		t.Errorf("after restart claude = %+v", byID["claude"])
	}
	if byID["codex"].Windows[0].UsedPercent != 13 || byID["codex"].Stale {
		t.Errorf("after restart codex = %+v", byID["codex"])
	}
}

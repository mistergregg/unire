package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"
)

func (e *env) agentModels() map[string][]string {
	e.t.Helper()
	code, body := e.do("GET", "/api/agents", nil)
	if code != 200 {
		e.t.Fatalf("GET /api/agents = %d %s", code, body)
	}
	var list []struct {
		ID     string   `json:"id"`
		Models []string `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		e.t.Fatal(err)
	}
	out := make(map[string][]string, len(list))
	for _, a := range list {
		out[a.ID] = a.Models
	}
	return out
}

func (e *env) waitModelsProbes() { e.srv.models.wg.Wait() }

func (b *fakeBox) modelCalls(agentID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modelsAsked[agentID]
}

func TestModelsAutoUpdateForAllAgentsAndPersist(t *testing.T) {
	e := newEnv(t)
	e.setup()
	base := time.Unix(1794870000, 0)
	e.srv.now = func() time.Time { return base }

	e.box.setStatus(map[string]statusResult{
		"claude": {`{"loggedIn": true}`, true},
		"codex":  {"Logged in", true},
		"sh":     {"", true},
	})
	e.box.modelsOut = map[string]string{
		"antigravity": "gemini-5-nova-high\tGemini 5 Nova (High)\ngemini-4-hydrogen-high\tGemini 4 Hydrogen (High)\n",
		"claude":      `{"type":"control_response","response":{"subtype":"success","request_id":"1","response":{"models":[{"value":"default"},{"value":"opus"},{"value":"claude-opus-5-9"}]}}}`,
		"codex":       `{"id":2,"result":{"data":[{"id":"gpt-7-nova","hidden":false},{"id":"gpt-6-luna","hidden":false}]}}`,
	}

	// First GET returns fallback models immediately and probes in the background.
	_ = e.agentModels()
	e.waitModelsProbes()

	got := e.agentModels()
	if !slices.Equal(got["antigravity"], []string{"gemini-5-nova-high", "gemini-4-hydrogen-high"}) {
		t.Errorf("antigravity models = %v", got["antigravity"])
	}
	if !slices.Equal(got["claude"], []string{"opus", "claude-opus-5-9"}) {
		t.Errorf("claude models = %v", got["claude"])
	}
	if !slices.Equal(got["codex"], []string{"gpt-7-nova", "gpt-6-luna"}) {
		t.Errorf("codex models = %v", got["codex"])
	}

	// Subsequent requests within modelsRefreshAfter do not re-probe.
	e.waitModelsProbes()
	for _, id := range []string{"antigravity", "claude", "codex"} {
		if n := e.box.modelCalls(id); n != 1 {
			t.Errorf("%s probed %d times; want 1", id, n)
		}
	}

	// Persists across server restart without losing discovered models.
	e.restart()
	e.srv.now = func() time.Time { return base.Add(5 * time.Minute) }
	restarted := e.agentModels()
	if !slices.Equal(restarted["antigravity"], []string{"gemini-5-nova-high", "gemini-4-hydrogen-high"}) {
		t.Errorf("after restart antigravity models = %v", restarted["antigravity"])
	}
	e.waitModelsProbes()
	for _, id := range []string{"antigravity", "claude", "codex"} {
		if n := e.box.modelCalls(id); n != 1 {
			t.Errorf("after restart within TTL %s probed %d times; want 1", id, n)
		}
	}

	// Advancing past modelsRefreshAfter triggers a background refresh.
	e.box.mu.Lock()
	e.box.modelsOut["antigravity"] = "gemini-5-ultra\tGemini 5 Ultra\n"
	e.box.mu.Unlock()
	e.srv.now = func() time.Time { return base.Add(16 * time.Minute) }
	_ = e.agentModels()
	e.waitModelsProbes()
	refreshed := e.agentModels()
	if !slices.Equal(refreshed["antigravity"], []string{"gemini-5-ultra"}) {
		t.Errorf("after TTL refresh antigravity models = %v", refreshed["antigravity"])
	}
}

func TestModelsRetryBackoffRecheckAndSignedOutBehavior(t *testing.T) {
	e := newEnv(t)
	e.setup()
	now := time.Unix(1794870000, 0)
	e.srv.now = func() time.Time { return now }

	// Antigravity is signed out, so it is skipped while claude/codex can still probe.
	e.box.setStatus(map[string]statusResult{
		"claude": {`{"loggedIn": false}`, true},
		"codex":  {"Not logged in", true},
		"sh":     {"", false},
	})
	e.box.modelsOut = map[string]string{
		"claude": "not-json", // simulate failure -> fallback kept & backs off for 1m
		"codex":  `{"id":2,"result":{"data":[{"id":"gpt-6-luna","hidden":false}]}}`,
	}

	first := e.agentModels()
	e.waitModelsProbes()
	if len(first["antigravity"]) == 0 || len(first["claude"]) == 0 {
		t.Fatalf("expected fallback models on initial load: %v", first)
	}
	if n := e.box.modelCalls("antigravity"); n != 0 {
		t.Errorf("signed-out antigravity probed %d times, want 0", n)
	}
	if n := e.box.modelCalls("claude"); n != 1 {
		t.Errorf("claude probed %d times, want 1", n)
	}

	// Within retry backoff (1m), failed claude probe is not retried.
	_ = e.agentModels()
	e.waitModelsProbes()
	if n := e.box.modelCalls("claude"); n != 1 {
		t.Errorf("claude probed %d times within retry window, want 1", n)
	}

	// Sign-in / recheck invalidates status and forces an immediate model refresh on next GET.
	e.box.setStatus(map[string]statusResult{
		"claude": {`{"loggedIn": true}`, true},
		"codex":  {"Logged in", true},
		"sh":     {"", true},
	})
	e.box.mu.Lock()
	e.box.modelsOut["antigravity"] = "gemini-4-hydrogen-high\tGemini 4 Hydrogen (High)\n"
	e.box.modelsOut["claude"] = `{"type":"control_response","response":{"subtype":"success","request_id":"1","response":{"models":[{"value":"opus"},{"value":"claude-sonnet-5"}]}}}`
	e.box.mu.Unlock()

	code, _ := e.do("POST", "/api/agents/recheck", nil)
	if code != http.StatusOK {
		t.Fatalf("POST /api/agents/recheck = %d", code)
	}
	e.waitModelsProbes()
	after := e.agentModels()
	if !slices.Equal(after["antigravity"], []string{"gemini-4-hydrogen-high"}) {
		t.Errorf("after recheck antigravity = %v", after["antigravity"])
	}
	if !slices.Equal(after["claude"], []string{"opus", "claude-sonnet-5"}) {
		t.Errorf("after recheck claude = %v", after["claude"])
	}
}

func TestModelsNotAskedWhileAgentRuns(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.box.block = make(chan struct{})
	e.box.modelsOut = map[string]string{
		"codex": `{"id":2,"result":{"data":[{"id":"gpt-7-codex","hidden":false}]}}`,
	}

	_, sess := e.launch(base, map[string]any{"agent": "codex"})
	id := sess["id"].(string)
	e.waitStatus(id, "running")

	_ = e.agentModels()
	e.waitModelsProbes()
	if n := e.box.modelCalls("codex"); n != 0 {
		t.Errorf("probed codex %d times while a Codex session was running", n)
	}

	if code, _ := e.do("POST", "/api/sessions/"+id+"/stop", nil); code != http.StatusOK {
		t.Fatalf("stop = %d", code)
	}
	e.waitStatus(id, "stopped")
	e.srv.sessions.Wait()

	_ = e.agentModels()
	e.waitModelsProbes()
	if n := e.box.modelCalls("codex"); n != 1 {
		t.Errorf("probed codex %d times after session stopped, want 1", n)
	}
}

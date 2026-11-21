package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/runner"
)

// RunOnline makes the fake runner answer the usage question with box.usageOut.
func (r fakeRunner) RunOnline(_ context.Context, argv []string) (string, bool, error) {
	r.box.mu.Lock()
	defer r.box.mu.Unlock()
	if len(argv) > 0 && argv[0] == "agy" {
		r.box.agyAsked++
		return r.box.agyUsageOut, true, nil
	}
	if len(argv) > 0 && argv[0] == "claude" {
		r.box.claudeAsked++
		return r.box.claudeUsageOut, true, nil
	}
	r.box.usageAsked++
	return r.box.usageOut, true, nil
}

var _ runner.OnlineShot = fakeRunner{}

func (b *fakeBox) usageCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usageAsked
}

// Real answers from codex app-server 0.169 in the sandbox.
const (
	codexUsageAnswer     = `{"id":2,"result":{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex","limitName":null,"normalModelSlug":null,"primary":{"usedPercent":13,"windowDurationMins":43200,"resetsAt":1797607630},"secondary":null,"credits":{"hasCredits":false,"unlimited":false,"balance":null},"individualLimit":null,"spendControlReached":false,"planType":"free","rateLimitReachedType":null},"rateLimitResetCredits":{"availableCount":0,"credits":[]},"rateLimitUpsell":null}}`
	codexSignedOutAnswer = `{"error":{"code":-32600,"message":"codex account authentication required to read rate limits"},"id":2}`
)

// A real answer from `agy --print=/usage --output-format=json` (agy 1.5.9).
const agyUsageAnswer = `{"status":"SUCCESS","response":"Gemini Models\tWeekly Limit Remaining\t78%\t2026-11-25T01:12:50Z\n","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","description":"Models within this group: Gemini 3.8 Flash","buckets":[{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.7781962156295776,"reset_time":"2026-11-25T01:12:50Z"},{"id":"gemini-5h","window":"5h","remaining_fraction":0.782702624797821,"reset_time":"2026-11-21T01:50:34Z"}]},{"name":"Claude and GPT models","description":"Models within this group: Claude Sonnet 4.6, Claude Opus 4.6, GPT-OSS 120B","buckets":[{"id":"3p-weekly","window":"weekly","remaining_fraction":0.9879376292228699,"reset_time":"2026-11-27T22:02:00Z"},{"id":"3p-5h","window":"5h","remaining_fraction":0.975875198841095,"reset_time":"2026-11-21T03:02:00Z"}]}]}}}`

func (e *env) usage() map[string]agent.AgentUsage {
	e.t.Helper()
	code, body := e.do("GET", "/api/usage", nil)
	if code != 200 {
		e.t.Fatalf("GET /api/usage = %d %s", code, body)
	}
	var list []agent.AgentUsage
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		e.t.Fatal(err)
	}
	byID := map[string]agent.AgentUsage{}
	for _, u := range list {
		byID[u.AgentID] = u
	}
	return byID
}

// waitUsageProbes waits for background usage tries to end.
func (e *env) waitUsageProbes() { e.srv.usage.wg.Wait() }

// Codex is asked in the background, in a throwaway sandbox; the request does
// not wait for it and the reading appears on the next one.
func TestCodexUsageIsAskedInTheBackgroundAndStored(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.usageOut = "some noise\n" + codexUsageAnswer + "\n"

	if got := e.usage()["codex"]; got.UpdatedAt != 0 {
		t.Fatalf("first answer already has a reading: %+v", got)
	}
	e.waitUsageProbes()
	cx := e.usage()["codex"]
	if cx.UpdatedAt == 0 || cx.Stale || cx.Plan != "free" || len(cx.Windows) != 1 ||
		cx.Windows[0].Name != "30-day" || cx.Windows[0].UsedPercent != 13 || cx.Windows[0].ResetsAt != 1797607630 {
		t.Fatalf("codex = %+v", cx)
	}
	e.waitUsageProbes()
	if n := e.box.usageCalls(); n != 1 {
		t.Errorf("codex asked %d times; a fresh reading must not be asked again", n)
	}
	e.box.mu.Lock()
	e.box.usageAsked = 0
	e.box.mu.Unlock()
	e.srv.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	e.usage()
	e.waitUsageProbes()
	if n := e.box.usageCalls(); n != 1 {
		t.Errorf("after 6 minutes codex asked %d times, want 1 (and no other agent)", n)
	}
}

// A failed try is remembered for a minute, so polling pages do not start a
// sandbox on every request; why it failed is shown.
func TestAFailedUsageTryIsNotRepeatedForAMinute(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.usageOut = codexSignedOutAnswer
	now := time.Now()
	e.srv.now = func() time.Time { return now }

	e.usage()
	e.waitUsageProbes()
	for range 5 {
		cx := e.usage()["codex"]
		if cx.UpdatedAt != 0 || cx.Note == "" {
			t.Fatalf("after a failed try codex = %+v", cx)
		}
		e.do("GET", "/api/agents", nil)
	}
	e.waitUsageProbes()
	if n := e.box.usageCalls(); n != 1 {
		t.Fatalf("asked %d times within a minute, want 1", n)
	}
	e.srv.now = func() time.Time { return now.Add(61 * time.Second) }
	e.usage()
	e.waitUsageProbes()
	if n := e.box.usageCalls(); n != 2 {
		t.Errorf("asked %d times after a minute, want 2", n)
	}
}

// A signed-out agent is not asked at all.
func TestUsageIsNotAskedOfASignedOutAgent(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.setStatus(map[string]statusResult{"codex": {"Not logged in", true}, "claude": {"", false}, "sh": {"", true}})
	e.do("GET", "/api/agents", nil) // learns that Codex is signed out
	e.waitUsageProbes()
	e.box.mu.Lock()
	e.box.usageAsked = 0
	e.box.mu.Unlock()
	e.srv.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	e.usage()
	e.waitUsageProbes()
	if n := e.box.usageCalls(); n != 0 {
		t.Errorf("a signed-out Codex was asked %d times", n)
	}
}

// Nothing is started while a chat with the same agent is queued or running:
// both would use the same login.
func TestUsageIsNotAskedWhileAChatWithTheAgentRuns(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.box.block = make(chan struct{})
	e.box.usageOut = codexUsageAnswer

	_, sess := e.launch(base, map[string]any{"agent": "codex"})
	id := sess["id"].(string)
	e.waitStatus(id, "running")
	e.usage()
	e.waitUsageProbes()
	if n := e.box.usageCalls(); n != 0 {
		t.Errorf("asked %d times while a Codex chat ran", n)
	}
	if code, _ := e.do("POST", "/api/sessions/"+id+"/stop", nil); code != http.StatusOK {
		t.Fatalf("stop = %d", code)
	}
	e.waitStatus(id, "stopped")
	e.srv.sessions.Wait()
	e.usage()
	e.waitUsageProbes()
	if n := e.box.usageCalls(); n != 1 {
		t.Errorf("asked %d times after the chat ended, want 1", n)
	}
}

// Antigravity is asked with its own `agy --print=/usage` run (no model call),
// in the background, and the groups and their models are kept with each window.
func TestAntigravityUsageIsAskedInTheBackgroundAndStored(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.agyUsageOut = agyUsageAnswer
	e.usage()
	e.waitUsageProbes()
	agy := e.usage()["antigravity"]
	if !agy.Reported || agy.UpdatedAt == 0 || agy.Stale || len(agy.Windows) != 4 {
		t.Fatalf("antigravity = %+v", agy)
	}
	if w := agy.Windows[0]; w.Group != "Gemini Models" || w.Name != "5-hour" || w.Models != "Gemini 3.8 Flash" {
		t.Errorf("first window = %+v", w)
	}
	e.waitUsageProbes()
	e.box.mu.Lock()
	asked := e.box.agyAsked
	e.box.mu.Unlock()
	if asked != 1 {
		t.Errorf("agy asked %d times; a fresh reading must not be asked again", asked)
	}
}

// An answer in a shape Unire does not know gives "no reading" and a note,
// never a number.
func TestAntigravityUsageWithAnUnknownShapeIsNoReading(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.agyUsageOut = `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"G","buckets":[{"window":"5h","remaining_fraction":"most"}]}]}}}`
	e.usage()
	e.waitUsageProbes()
	agy := e.usage()["antigravity"]
	if agy.UpdatedAt != 0 || len(agy.Windows) != 0 || agy.Note == "" {
		t.Errorf("antigravity = %+v; want no reading with a note", agy)
	}
}

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

	// Antigravity reports its limits too; there is just no reading yet.
	agy := byID["antigravity"]
	if !agy.Reported {
		t.Errorf("antigravity Reported = false; want true")
	}
	if agy.Stale || agy.UpdatedAt != 0 {
		t.Errorf("antigravity without a reading = %+v", agy)
	}
	if len(agy.Windows) != 0 {
		t.Errorf("antigravity Windows = %+v; want empty", agy.Windows)
	}

	// Claude without a reading: it reports usage, there is just nothing yet
	// (not "stale": there is no old number to dim).
	claude := byID["claude"]
	if !claude.Reported || claude.Stale || claude.UpdatedAt != 0 {
		t.Errorf("claude without reading = %+v", claude)
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

// Claude is asked with `claude -p /usage` (no model call) in the background.
func TestClaudeUsageIsAskedInTheBackgroundAndStored(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.claudeUsageOut = `{"is_error":false,"result":"Current session: 45% used · resets Nov 21, 6:50am (UTC)\nCurrent week (all models): 30% used · resets Nov 26, 12pm (UTC)"}`
	e.usage()
	e.waitUsageProbes()
	cl := e.usage()["claude"]
	if cl.UpdatedAt == 0 || len(cl.Windows) != 2 || cl.Windows[0].UsedPercent != 45 || cl.Windows[1].UsedPercent != 30 {
		t.Fatalf("claude = %+v", cl)
	}
}

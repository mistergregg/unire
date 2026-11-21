package agent

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseClaudeRealRateLimitSample(t *testing.T) {
	// Real sample captured from Claude Code stream in this sandbox
	sample := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1794879000,"rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false,"unifiedWindows":{"five_hour":{"utilization":0.34,"resetsAt":1794879000,"status":"allowed"},"seven_day":{"utilization":0.72,"resetsAt":1795089600,"status":"allowed"}}},"uuid":"5c641997-2055-4699-97ee-08df29611865","session_id":"2b58df82-6042-4de2-b216-a5ad513f26ea"}`

	var m map[string]any
	if err := json.Unmarshal([]byte(sample), &m); err != nil {
		t.Fatal(err)
	}

	u := ParseClaudeUsage(m)
	if u == nil {
		t.Fatal("expected non-nil AgentUsage from Claude sample")
	}
	if u.AgentID != "claude" {
		t.Errorf("AgentID = %q; want 'claude'", u.AgentID)
	}
	if !u.Reported {
		t.Errorf("Reported = false; want true")
	}
	if u.Status != "allowed" {
		t.Errorf("Status = %q; want 'allowed'", u.Status)
	}
	if len(u.Windows) != 2 {
		t.Fatalf("len(Windows) = %d; want 2", len(u.Windows))
	}

	w0 := u.Windows[0]
	if w0.Name != "5-hour" || w0.DurationMins != 300 || w0.UsedPercent != 34 || w0.ResetsAt != 1794879000 {
		t.Errorf("w0 = %+v; want 5-hour, 300 mins, 34%%, resets 1794879000", w0)
	}

	w1 := u.Windows[1]
	if w1.Name != "7-day" || w1.DurationMins != 10080 || w1.UsedPercent != 72 || w1.ResetsAt != 1795089600 {
		t.Errorf("w1 = %+v; want 7-day, 10080 mins, 72%%, resets 1795089600", w1)
	}

	// Verify claude adapter ParseEvent also attaches this Usage
	a, _ := Get("claude")
	streamer := a.(Streamer)
	ev := streamer.ParseEvent(sample)
	if !ev.Known {
		t.Error("expected ev.Known = true")
	}
	if ev.Progress != "usage: 34% of the 5-hour limit, 72% of the 7-day limit" {
		t.Errorf("ev.Progress = %q", ev.Progress)
	}
	if ev.Usage == nil {
		t.Fatal("expected ev.Usage to be set by ParseEvent")
	}
	if ev.Usage.AgentID != "claude" || len(ev.Usage.Windows) != 2 {
		t.Errorf("ev.Usage = %+v", ev.Usage)
	}
}

func TestParseClaudeMinimalAndFallback(t *testing.T) {
	// An event that names no window is no reading: it must not replace a good one.
	minimal := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`
	var m1 map[string]any
	_ = json.Unmarshal([]byte(minimal), &m1)
	if u1 := ParseClaudeUsage(m1); u1 != nil {
		t.Errorf("minimal = %+v, want nil", u1)
	}

	// Top-level utilization fallback
	topLevel := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","rateLimitType":"five_hour","utilization":0.85,"resetsAt":1794880000}}`
	var m2 map[string]any
	_ = json.Unmarshal([]byte(topLevel), &m2)
	u2 := ParseClaudeUsage(m2)
	if u2 == nil || len(u2.Windows) != 1 || u2.Windows[0].UsedPercent != 85 || u2.Windows[0].DurationMins != 300 {
		t.Errorf("fallback = %+v", u2)
	}
}

func TestParseCodexRealAppServerSample(t *testing.T) {
	// Real sample returned by `codex app-server` for `account/rateLimits/read`
	sample := `{
		"ordinaryUsageAllowed": true,
		"rateLimits": {
			"limitId": "codex",
			"limitName": null,
			"normalModelSlug": null,
			"primary": {
				"usedPercent": 13,
				"windowDurationMins": 43200,
				"resetsAt": 1797607630
			},
			"secondary": null,
			"credits": {
				"hasCredits": false,
				"unlimited": false,
				"balance": null
			},
			"individualLimit": null,
			"spendControlReached": false,
			"planType": "free",
			"rateLimitReachedType": null
		},
		"rateLimitsByLimitId": {
			"codex": {
				"limitId": "codex",
				"primary": {
					"usedPercent": 13,
					"windowDurationMins": 43200,
					"resetsAt": 1797607630
				}
			}
		},
		"rateLimitResetCredits": {
			"availableCount": 0,
			"credits": []
		},
		"accountId": "258563de-9442-4493-8346-3576ff586bc7"
	}`

	var m map[string]any
	if err := json.Unmarshal([]byte(sample), &m); err != nil {
		t.Fatal(err)
	}

	u := ParseCodexUsage(m)
	if u == nil {
		t.Fatal("expected non-nil AgentUsage from Codex sample")
	}
	if u.AgentID != "codex" {
		t.Errorf("AgentID = %q; want 'codex'", u.AgentID)
	}
	if !u.Reported {
		t.Errorf("Reported = false; want true")
	}
	if u.Status != "allowed" {
		t.Errorf("Status = %q; want 'allowed'", u.Status)
	}
	if u.Plan != "free" {
		t.Errorf("Plan = %q; want 'free'", u.Plan)
	}
	if len(u.Windows) != 1 {
		t.Fatalf("len(Windows) = %d; want 1", len(u.Windows))
	}
	w0 := u.Windows[0]
	if w0.Name != "30-day" || w0.DurationMins != 43200 || w0.UsedPercent != 13 || w0.ResetsAt != 1797607630 {
		t.Errorf("w0 = %+v; want 30-day, 43200 mins, 13%%, resets 1797607630", w0)
	}
}

func TestParseCodexLiveNotification(t *testing.T) {
	// Test account/rateLimits/updated notification in codexLive driver
	_, d := liveOf(t, "codex", LiveOptions{Mode: HandsOff, Dir: "/workspace"})

	notif := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":25,"windowDurationMins":300,"resetsAt":1797600000},"secondary":{"usedPercent":65,"windowDurationMins":10080,"resetsAt":1797900000},"planType":"plus"}}}`
	ev := d.Line(notif)
	if !ev.Known {
		t.Error("expected ev.Known = true")
	}
	if ev.Usage == nil {
		t.Fatal("expected ev.Usage from account/rateLimits/updated")
	}
	if ev.Usage.AgentID != "codex" || ev.Usage.Plan != "plus" || len(ev.Usage.Windows) != 2 {
		t.Fatalf("ev.Usage = %+v", ev.Usage)
	}
	if ev.Usage.Windows[0].Name != "5-hour" || ev.Usage.Windows[0].UsedPercent != 25 {
		t.Errorf("Windows[0] = %+v", ev.Usage.Windows[0])
	}
	if ev.Usage.Windows[1].Name != "7-day" || ev.Usage.Windows[1].UsedPercent != 65 {
		t.Errorf("Windows[1] = %+v", ev.Usage.Windows[1])
	}
}

// A real answer from `agy --print=/usage --output-format=json` (agy 1.5.9).
const agyUsageSample = `{"conversation_id":"","status":"SUCCESS","response":"Gemini Models\tWeekly Limit Remaining\t78%\t2026-11-25T01:12:50Z\n","duration_seconds":0,"num_turns":0,"usage":{"input_tokens":0,"output_tokens":0,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":0},"command":{"name":"usage","data":{"description":"Within each group, models share a weekly limit and a 5-hour limit.","groups":[{"name":"Gemini Models","description":"Models within this group: Gemini 3.8 Flash","buckets":[{"id":"gemini-weekly","name":"Weekly Limit Remaining","description":"x","window":"weekly","remaining_fraction":0.7781962156295776,"reset_time":"2026-11-25T01:12:50Z"},{"id":"gemini-5h","name":"Five Hour Limit Remaining","description":"x","window":"5h","remaining_fraction":0.782702624797821,"reset_time":"2026-11-21T01:50:34Z"}]},{"name":"Claude and GPT models","description":"Models within this group: Claude Sonnet 4.6, Claude Opus 4.6, GPT-OSS 120B","buckets":[{"id":"3p-weekly","window":"weekly","remaining_fraction":0.9879376292228699,"reset_time":"2026-11-27T22:02:00Z"},{"id":"3p-5h","window":"5h","remaining_fraction":0.975875198841095,"reset_time":"2026-11-21T03:02:00Z"}]}]}}}`

func TestAntigravityUsageDefaultsToReported(t *testing.T) {
	u := DefaultUsage("antigravity")
	if u.AgentID != "antigravity" || !u.Reported || len(u.Windows) != 0 {
		t.Errorf("DefaultUsage = %+v; want reported, no windows yet", u)
	}
}

func TestParseAntigravityUsageRealSample(t *testing.T) {
	u, err := ParseAntigravityUsageOutput("update available\n" + agyUsageSample + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if !u.Reported || u.AgentID != "antigravity" || len(u.Windows) != 4 {
		t.Fatalf("usage = %+v", u)
	}
	w := u.Windows[0] // Gemini 5h (shortest window first in each group)
	reset, _ := time.Parse(time.RFC3339, "2026-11-21T01:50:34Z")
	if w.Group != "Gemini Models" || w.Models != "Gemini 3.8 Flash" || w.Name != "5-hour" || w.DurationMins != 300 ||
		w.ResetsAt != reset.Unix() || math.Abs(w.UsedPercent-21.7297) > 0.001 {
		t.Errorf("Gemini 5h = %+v", w)
	}
	g := u.Windows[3] // Claude and GPT weekly
	if g.Group != "Claude and GPT models" || g.Name != "7-day" || math.Abs(g.UsedPercent-1.2062) > 0.001 ||
		g.Models != "Claude Sonnet 4.6, Claude Opus 4.6, GPT-OSS 120B" {
		t.Errorf("Claude/GPT weekly = %+v", g)
	}
}

func TestParseAntigravityUsageMalformed(t *testing.T) {
	for name, out := range map[string]string{
		"empty":           "",
		"text table":      "Quota:\nGemini Models  Weekly Limit Remaining  78%  2026-11-25 01:12 UTC\n",
		"stream error":    `{"event":"result","result":{"status":"ERROR","error":"/usage is unavailable"}}`,
		"error status":    `{"status":"ERROR","error":"not signed in"}`,
		"no groups":       `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[]}}}`,
		"other command":   `{"status":"SUCCESS","command":{"name":"help","data":{}}}`,
		"no fraction":     `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"G","buckets":[{"window":"5h","reset_time":"2026-11-21T01:50:34Z"}]}]}}}`,
		"percent not 0-1": `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"G","buckets":[{"window":"5h","remaining_fraction":78,"reset_time":"2026-11-21T01:50:34Z"}]}]}}}`,
		"bad reset":       `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"G","buckets":[{"window":"5h","remaining_fraction":0.5,"reset_time":"tomorrow"}]}]}}}`,
		"missing reset":   `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"G","buckets":[{"window":"5h","remaining_fraction":0.5}]}]}}}`,
		"unknown window":  `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"G","buckets":[{"window":"monthly","remaining_fraction":0.5,"reset_time":"2026-11-21T01:50:34Z"}]}]}}`,
		"no buckets":      `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"G","buckets":[]}]}}}`,
	} {
		if u, err := ParseAntigravityUsageOutput(out); err == nil {
			t.Errorf("%s: got a reading %+v, want an error", name, u)
		}
	}
}

func TestAntigravityUsageIsAskedWithItsOwnPrintCommand(t *testing.T) {
	ad, err := Get("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	info := ad.Info()
	want := []string{"agy", "--print=/usage", "--output-format=json"}
	if strings.Join(info.UsageArgv, " ") != strings.Join(want, " ") || info.ParseUsage == nil {
		t.Errorf("UsageArgv = %v; want %v (a plain --print run: stream-json refuses /usage)", info.UsageArgv, want)
	}
}

func TestWindowName(t *testing.T) {
	cases := []struct {
		mins     int
		fallback string
		want     string
	}{
		{300, "Primary", "5-hour"},
		{60, "Primary", "1-hour"},
		{10080, "Secondary", "7-day"},
		{43200, "Primary", "30-day"},
		{45, "Window", "45-min"},
		{0, "Fallback", "Fallback"},
		{-1, "Fallback", "Fallback"},
	}
	for _, c := range cases {
		if got := WindowName(c.mins, c.fallback); got != c.want {
			t.Errorf("WindowName(%d, %q) = %q; want %q", c.mins, c.fallback, got, c.want)
		}
	}
}

// Captured from claude 2026-11-20 (stream-json --verbose): utilization is 0 to 1,
// resetsAt is Unix seconds, windows come shortest first.
func TestParseClaudeSecondRealSample(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1795225200,"rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false,"unifiedWindows":{"seven_day":{"utilization":0.19,"resetsAt":1795694400,"status":"allowed"},"five_hour":{"utilization":0.53,"resetsAt":1795225200,"status":"allowed"}}},"uuid":"4eeafcc7-59bd-4e94-b9fc-db224b013703","session_id":"553f0dea-e0d9-477d-81c7-c3317b015f75"}`
	var m map[string]any
	_ = json.Unmarshal([]byte(line), &m)
	u := ParseClaudeUsage(m)
	if u == nil || len(u.Windows) != 2 {
		t.Fatalf("u = %+v", u)
	}
	want := []UsageWindow{{Name: "5-hour", DurationMins: 300, UsedPercent: 53, ResetsAt: 1795225200}, {Name: "7-day", DurationMins: 10080, UsedPercent: 19, ResetsAt: 1795694400}}
	for i, w := range want {
		if u.Windows[i] != w {
			t.Errorf("window %d = %+v, want %+v", i, u.Windows[i], w)
		}
	}
}

func TestParseCodexUsageOutput(t *testing.T) {
	// Real output of codexUsageScript, signed in and signed out.
	ok := `{"id":2,"result":{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex","limitName":null,"normalModelSlug":null,"primary":{"usedPercent":13,"windowDurationMins":43200,"resetsAt":1797607630},"secondary":null,"credits":{"hasCredits":false,"unlimited":false,"balance":null},"individualLimit":null,"spendControlReached":false,"planType":"free","rateLimitReachedType":null},"rateLimitResetCredits":{"availableCount":0,"credits":[]},"accountId":"x","rateLimitUpsell":null}}`
	u, err := ParseCodexUsageOutput("Unable to find image locally\n" + ok + "\n")
	if err != nil || u.Plan != "free" || len(u.Windows) != 1 || u.Windows[0] != (UsageWindow{Name: "30-day", DurationMins: 43200, UsedPercent: 13, ResetsAt: 1797607630}) {
		t.Fatalf("u = %+v, err = %v", u, err)
	}
	out := `{"error":{"code":-32600,"message":"codex account authentication required to read rate limits"},"id":2}`
	if _, err := ParseCodexUsageOutput(out); err == nil || !strings.Contains(err.Error(), "authentication required") {
		t.Errorf("signed out: err = %v", err)
	}
	if _, err := ParseCodexUsageOutput(""); err == nil {
		t.Error("no output must be an error")
	}
}

func TestCodexUsageEdgeCases(t *testing.T) {
	parse := func(s string) *AgentUsage {
		var m map[string]any
		_ = json.Unmarshal([]byte(s), &m)
		return ParseCodexUsage(m)
	}
	// A separate limit (for one model) is not Codex's main limit.
	if u := parse(`{"rateLimits":{"limitId":"codex_other","primary":{"usedPercent":90,"windowDurationMins":300}}}`); u != nil {
		t.Errorf("other limit = %+v", u)
	}
	// Shortest window first; out-of-range percents are clamped; a reached limit says so.
	u := parse(`{"ordinaryUsageAllowed":false,"rateLimits":{"primary":{"usedPercent":140,"windowDurationMins":10080},"secondary":{"usedPercent":-3,"windowDurationMins":300},"rateLimitReachedType":"rate_limit_reached"}}`)
	if u == nil || u.Windows[0].Name != "5-hour" || u.Windows[0].UsedPercent != 0 || u.Windows[1].UsedPercent != 100 || u.Status != "rate_limit_reached" {
		t.Errorf("u = %+v", u)
	}
	if u := parse(`{"rateLimits":{"primary":null,"secondary":null}}`); u != nil {
		t.Errorf("no windows = %+v", u)
	}
}

func TestParseClaudeUsageOutput(t *testing.T) {
	out := `{"is_error":false,"result":"You are currently using your subscription\n\nCurrent session: 45% used · resets Nov 21, 6:50am (UTC)\nCurrent week (all models): 30% used · resets Nov 26, 12pm (UTC)\nCurrent week (Opus): 5% used","type":"result"}`
	now := time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)
	u, err := parseClaudeUsageOutput(out, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Windows) != 2 || u.Windows[0].Name != "5-hour" || u.Windows[0].UsedPercent != 45 ||
		u.Windows[1].Name != "7-day" || u.Windows[1].UsedPercent != 30 {
		t.Fatalf("windows: %+v", u.Windows)
	}
	if want := time.Date(2026, 11, 21, 6, 50, 0, 0, time.UTC).Unix(); u.Windows[0].ResetsAt != want {
		t.Fatalf("reset %d want %d", u.Windows[0].ResetsAt, want)
	}
	if want := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC).Unix(); u.Windows[1].ResetsAt != want {
		t.Fatalf("reset %d want %d", u.Windows[1].ResetsAt, want)
	}
	if _, err := parseClaudeUsageOutput(`{"result":"hello"}`, now); err == nil {
		t.Fatal("want an error for an unexpected shape")
	}
}

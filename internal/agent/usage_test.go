package agent

import (
	"encoding/json"
	"testing"
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
	// Minimal sample with no unifiedWindows
	minimal := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`
	var m1 map[string]any
	_ = json.Unmarshal([]byte(minimal), &m1)
	u1 := ParseClaudeUsage(m1)
	if u1 == nil || !u1.Reported || u1.Status != "allowed" || len(u1.Windows) != 0 {
		t.Errorf("minimal = %+v", u1)
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

func TestAntigravityReportsNothingUsable(t *testing.T) {
	u := DefaultUsage("antigravity")
	if u.AgentID != "antigravity" {
		t.Errorf("AgentID = %q", u.AgentID)
	}
	if u.Reported {
		t.Errorf("Reported = true; Antigravity should be Reported = false")
	}
	if u.Status != "not reported" {
		t.Errorf("Status = %q; want 'not reported'", u.Status)
	}
	if len(u.Windows) != 0 {
		t.Errorf("Windows = %+v; want empty", u.Windows)
	}
}

func TestFormatWindowDurationName(t *testing.T) {
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
		if got := FormatWindowDurationName(c.mins, c.fallback); got != c.want {
			t.Errorf("FormatWindowDurationName(%d, %q) = %q; want %q", c.mins, c.fallback, got, c.want)
		}
	}
}

package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// UsageWindow is how much of one usage limit window (e.g. 5 hours, 7 days) is used.
type UsageWindow struct {
	Name         string  `json:"name"`                   // from the window length: "5-hour", "7-day", "30-day"
	DurationMins int     `json:"durationMins,omitempty"` // window length in minutes; 0 when the CLI does not say
	UsedPercent  float64 `json:"usedPercent"`            // 0 to 100
	ResetsAt     int64   `json:"resetsAt,omitempty"`     // Unix seconds, as the CLI reported it; 0 when unknown
}

// AgentUsage is the latest usage limits reading for an agent.
type AgentUsage struct {
	AgentID   string        `json:"agentId"`
	Label     string        `json:"label,omitempty"`  // the agent's name in the UI (set by the server)
	Reported  bool          `json:"reported"`         // false: the CLI reports no usage limits
	Status    string        `json:"status,omitempty"` // the CLI's own status ("allowed", ...) or "not reported"
	Plan      string        `json:"plan,omitempty"`   // e.g. "free", "plus", when the CLI says
	Windows   []UsageWindow `json:"windows"`
	UpdatedAt int64         `json:"updatedAt"`      // Unix seconds when Unire read it; 0: never
	Stale     bool          `json:"stale"`          // the reading is old (set by the server)
	Note      string        `json:"note,omitempty"` // why the last try to read it failed (set by the server)
}

// WindowName names a window by its length: 300 minutes is "5-hour", 10080 is
// "7-day", 43200 is "30-day". With no length it returns fallback.
func WindowName(mins int, fallback string) string {
	switch {
	case mins <= 0:
		return fallback
	case mins%1440 == 0:
		return fmt.Sprintf("%d-day", mins/1440)
	case mins%60 == 0:
		return fmt.Sprintf("%d-hour", mins/60)
	}
	return fmt.Sprintf("%d-min", mins)
}

// DefaultUsage is the reading for an agent before anything was read: Claude and
// Codex report their limits, the others do not.
func DefaultUsage(agentID string) AgentUsage {
	u := AgentUsage{AgentID: agentID, Windows: []UsageWindow{}}
	switch agentID {
	case "claude", "codex":
		u.Reported = true
	default:
		u.Status = "not reported"
	}
	return u
}

// claudeWindowMins are the lengths of the windows Claude Code names in
// rate_limit_info.unifiedWindows (it gives names, not lengths).
var claudeWindowMins = map[string]int{"five_hour": 300, "seven_day": 10080}

// ParseClaudeUsage reads Claude Code's stream-json rate_limit_event, e.g.
//
//	{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1795225200,
//	 "rateLimitType":"five_hour",...,"unifiedWindows":{"five_hour":{"utilization":0.53,
//	 "resetsAt":1795225200,"status":"allowed"},"seven_day":{"utilization":0.19,...}}}}
//
// utilization is 0 to 1. It returns nil when the event names no window.
func ParseClaudeUsage(m map[string]any) *AgentUsage {
	info, _ := m["rate_limit_info"].(map[string]any)
	if info == nil {
		return nil
	}
	var windows []UsageWindow
	add := func(key string, win map[string]any) {
		u, ok := toFloat(win["utilization"])
		if !ok {
			return
		}
		mins := claudeWindowMins[key]
		w := UsageWindow{Name: WindowName(mins, strings.ReplaceAll(key, "_", "-")), DurationMins: mins, UsedPercent: clampPercent(u * 100)}
		w.ResetsAt, _ = toInt64(win["resetsAt"])
		windows = append(windows, w)
	}
	if all, ok := info["unifiedWindows"].(map[string]any); ok {
		keys := make([]string, 0, len(all))
		for k := range all {
			keys = append(keys, k)
		}
		// Shortest known window first, then any others by name.
		sort.Slice(keys, func(i, j int) bool {
			a, b := claudeWindowMins[keys[i]], claudeWindowMins[keys[j]]
			if (a == 0) != (b == 0) {
				return a != 0
			}
			if a != b {
				return a < b
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			if win, ok := all[k].(map[string]any); ok {
				add(k, win)
			}
		}
	} else if t := firstString(info, "rateLimitType"); t != "" {
		add(t, info) // an older event that carries a single window at the top level
	}
	if len(windows) == 0 {
		return nil
	}
	return &AgentUsage{AgentID: "claude", Reported: true, Status: firstString(info, "status"), Windows: windows}
}

// ParseCodexUsage reads a Codex app-server rate limit reading: the result of
// account/rateLimits/read ({"rateLimits":{...},"ordinaryUsageAllowed":true,...})
// or the params of the account/rateLimits/updated notification
// ({"rateLimits":{...}}). usedPercent is 0 to 100. It returns nil when there is
// no window, or when the snapshot is for a limit other than Codex's main one.
func ParseCodexUsage(m map[string]any) *AgentUsage {
	snap, _ := m["rateLimits"].(map[string]any)
	if snap == nil {
		return nil
	}
	if id := firstString(snap, "limitId"); id != "" && id != "codex" {
		return nil // a separate limit (for one model); the main one is what the owner wants
	}
	var windows []UsageWindow
	for _, key := range []string{"primary", "secondary"} {
		win, _ := snap[key].(map[string]any)
		pct, ok := toFloat(win["usedPercent"])
		if !ok {
			continue
		}
		mins64, _ := toInt64(win["windowDurationMins"])
		w := UsageWindow{Name: WindowName(int(mins64), key), DurationMins: int(mins64), UsedPercent: clampPercent(pct)}
		w.ResetsAt, _ = toInt64(win["resetsAt"])
		windows = append(windows, w)
	}
	if len(windows) == 0 {
		return nil
	}
	sort.SliceStable(windows, func(i, j int) bool {
		a, b := windows[i].DurationMins, windows[j].DurationMins
		return a != 0 && (b == 0 || a < b)
	})
	status := "allowed"
	if ok, has := m["ordinaryUsageAllowed"].(bool); has && !ok {
		status = "limit reached"
	}
	if r := firstString(snap, "rateLimitReachedType"); r != "" {
		status = r
	}
	return &AgentUsage{AgentID: "codex", Reported: true, Status: status, Plan: firstString(snap, "planType"), Windows: windows}
}

// codexUsageScript asks codex app-server for the account's rate limits and
// prints its answer line. app-server ends as soon as its input closes, before
// it answers, so the input stays open until the answer is seen (or 20 seconds
// pass). Nothing in it comes from outside: the requests are fixed.
const codexUsageScript = `d=$(mktemp -d) || exit 1
{
  printf '%s\n' '{"method":"initialize","id":1,"params":{"clientInfo":{"name":"unire","title":"Unire","version":"1"}}}' '{"method":"initialized"}' '{"method":"account/rateLimits/read","id":2}'
  i=0; while [ ! -e "$d/done" ] && [ $i -lt 100 ]; do sleep 0.2; i=$((i+1)); done
} | timeout 25 codex app-server 2>/dev/null | while IFS= read -r l; do
  case "$l" in '{"id":2,'*|*',"id":2}') printf '%s\n' "$l"; touch "$d/done";; esac
done
rm -rf "$d"`

// ParseCodexUsageOutput reads what codexUsageScript printed.
func ParseCodexUsageOutput(out string) (*AgentUsage, error) {
	for _, line := range strings.Split(out, "\n") {
		var m struct {
			ID     int            `json:"id"`
			Result map[string]any `json:"result"`
			Error  map[string]any `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &m) != nil || m.ID != 2 {
			continue
		}
		if m.Error != nil {
			msg, _ := m.Error["message"].(string)
			return nil, errors.New("codex: " + clip(msg, 200))
		}
		if u := ParseCodexUsage(m.Result); u != nil {
			return u, nil
		}
		return nil, errors.New("codex reported no usage window")
	}
	return nil, errors.New("codex gave no answer about its usage")
}

func clampPercent(p float64) float64 {
	return max(0, min(100, p))
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func toInt64(v any) (int64, bool) {
	f, ok := toFloat(v)
	return int64(f), ok
}

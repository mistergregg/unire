package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// UsageWindow represents usage for a specific time window (e.g. 5-hour, 7-day).
type UsageWindow struct {
	Name         string  `json:"name"`                   // e.g. "5-hour", "7-day", "30-day"
	DurationMins int     `json:"durationMins,omitempty"` // window length in minutes
	UsedPercent  float64 `json:"usedPercent"`            // percentage used (0 to 100)
	ResetsAt     int64   `json:"resetsAt,omitempty"`     // Unix timestamp in seconds
}

// AgentUsage represents the latest usage limits reading for an agent.
type AgentUsage struct {
	AgentID   string        `json:"agentId"`
	Reported  bool          `json:"reported"`         // true if the agent reports usage limits
	Status    string        `json:"status,omitempty"` // e.g. "allowed", "not reported"
	Plan      string        `json:"plan,omitempty"`   // e.g. "pro", "free" if reported
	Windows   []UsageWindow `json:"windows"`          // per-window readings
	UpdatedAt int64         `json:"updatedAt"`        // Unix timestamp when Unire read it
	Stale     bool          `json:"stale"`            // true when older than staleness threshold
}

// FormatWindowDurationName formats a duration in minutes into a readable window name.
func FormatWindowDurationName(mins int, fallback string) string {
	if mins <= 0 {
		return fallback
	}
	if mins%1440 == 0 {
		return fmt.Sprintf("%d-day", mins/1440)
	}
	if mins%60 == 0 {
		return fmt.Sprintf("%d-hour", mins/60)
	}
	return fmt.Sprintf("%d-min", mins)
}

// DefaultUsage returns a baseline reading for an agent.
func DefaultUsage(agentID string) AgentUsage {
	reported := agentID == "claude" || agentID == "codex"
	status := ""
	if !reported {
		status = "not reported"
	}
	return AgentUsage{
		AgentID:  agentID,
		Reported: reported,
		Status:   status,
		Windows:  []UsageWindow{},
	}
}

// ParseClaudeUsage parses the rate_limit_event payload from Claude Code.
func ParseClaudeUsage(m map[string]any) *AgentUsage {
	info, _ := m["rate_limit_info"].(map[string]any)
	if info == nil {
		return nil
	}
	status := firstString(info, "status")
	if status == "" {
		status = "allowed"
	}
	plan := firstString(info, "planTier", "subscriptionType")

	var windows []UsageWindow
	windowsMap, _ := info["unifiedWindows"].(map[string]any)
	if windowsMap != nil {
		knownWindows := []struct {
			key      string
			name     string
			duration int
		}{
			{"five_hour", "5-hour", 300},
			{"seven_day", "7-day", 10080},
			{"seven_day_overage_included", "7-day (overage)", 10080},
			{"seven_day_sonnet", "7-day Sonnet", 10080},
			{"seven_day_opus", "7-day Opus", 10080},
		}
		seen := make(map[string]bool)
		for _, kw := range knownWindows {
			if win, ok := windowsMap[kw.key].(map[string]any); ok {
				seen[kw.key] = true
				if w := parseClaudeWindow(win, kw.name, kw.duration); w != nil {
					windows = append(windows, *w)
				}
			}
		}
		// Any remaining windows in windowsMap
		var extraKeys []string
		for k := range windowsMap {
			if !seen[k] {
				extraKeys = append(extraKeys, k)
			}
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			if win, ok := windowsMap[k].(map[string]any); ok {
				name := strings.ReplaceAll(k, "_", "-")
				if w := parseClaudeWindow(win, name, 0); w != nil {
					windows = append(windows, *w)
				}
			}
		}
	} else if u, ok := toFloat(info["utilization"]); ok {
		// Top-level utilization fallback
		t := firstString(info, "rateLimitType")
		name := "primary"
		duration := 0
		if t == "five_hour" {
			name = "5-hour"
			duration = 300
		} else if t == "seven_day" {
			name = "7-day"
			duration = 10080
		} else if t != "" {
			name = strings.ReplaceAll(t, "_", "-")
		}
		w := UsageWindow{
			Name:         name,
			DurationMins: duration,
			UsedPercent:  u * 100,
		}
		if r, ok := toInt64(info["resetsAt"]); ok {
			w.ResetsAt = r
		}
		windows = append(windows, w)
	}

	if windows == nil {
		windows = []UsageWindow{}
	}

	return &AgentUsage{
		AgentID:  "claude",
		Reported: true,
		Status:   status,
		Plan:     plan,
		Windows:  windows,
	}
}

func parseClaudeWindow(win map[string]any, name string, duration int) *UsageWindow {
	u, ok := toFloat(win["utilization"])
	if !ok {
		return nil
	}
	w := &UsageWindow{
		Name:         name,
		DurationMins: duration,
		UsedPercent:  u * 100,
	}
	if r, ok := toInt64(win["resetsAt"]); ok {
		w.ResetsAt = r
	}
	return w
}

// ParseCodexUsage parses the rate limit snapshot from Codex app-server.
func ParseCodexUsage(m map[string]any) *AgentUsage {
	if m == nil {
		return nil
	}
	// If wrapped in result or params or rateLimits
	if res, ok := m["result"].(map[string]any); ok {
		m = res
	}
	ordinaryAllowed, hasOrdinary := m["ordinaryUsageAllowed"].(bool)
	if p, ok := m["params"].(map[string]any); ok {
		m = p
	}
	if rl, ok := m["rateLimits"].(map[string]any); ok {
		m = rl
	}

	plan := firstString(m, "planType")
	status := "allowed"
	if hasOrdinary && !ordinaryAllowed {
		status = "rejected"
	}
	if reachedType := firstString(m, "rateLimitReachedType"); reachedType != "" {
		status = reachedType
	}

	var windows []UsageWindow
	if primary, ok := m["primary"].(map[string]any); ok && primary != nil {
		if w := parseCodexWindow(primary, "Primary"); w != nil {
			windows = append(windows, *w)
		}
	}
	if secondary, ok := m["secondary"].(map[string]any); ok && secondary != nil {
		if w := parseCodexWindow(secondary, "Secondary"); w != nil {
			windows = append(windows, *w)
		}
	}

	if windows == nil {
		windows = []UsageWindow{}
	}

	return &AgentUsage{
		AgentID:  "codex",
		Reported: true,
		Status:   status,
		Plan:     plan,
		Windows:  windows,
	}
}

func parseCodexWindow(win map[string]any, fallbackName string) *UsageWindow {
	pct, ok := toFloat(win["usedPercent"])
	if !ok {
		return nil
	}
	mins := 0
	if m, ok := toInt64(win["windowDurationMins"]); ok {
		mins = int(m)
	}
	name := FormatWindowDurationName(mins, fallbackName)
	w := &UsageWindow{
		Name:         name,
		DurationMins: mins,
		UsedPercent:  pct,
	}
	if r, ok := toInt64(win["resetsAt"]); ok {
		w.ResetsAt = r
	}
	return w
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// QueryCodexUsage queries codex app-server directly for current rate limits.
func QueryCodexUsage(ctx context.Context) (*AgentUsage, error) {
	cmd := exec.CommandContext(ctx, "codex", "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	initReq := `{"method":"initialize","id":1,"params":{"clientInfo":{"name":"unire","title":"Unire","version":"1"}}}` + "\n"
	if _, err := io.WriteString(stdin, initReq); err != nil {
		return nil, err
	}
	readReq := `{"method":"account/rateLimits/read","id":2,"params":null}` + "\n"
	if _, err := io.WriteString(stdin, readReq); err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		id, _ := m["id"].(float64)
		if int(id) == 2 {
			res, _ := m["result"].(map[string]any)
			if res == nil {
				return nil, fmt.Errorf("empty result from codex rateLimits")
			}
			return ParseCodexUsage(res), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("codex app-server did not return rate limits")
}

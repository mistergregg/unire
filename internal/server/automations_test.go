package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/store"
	"github.com/mistergregg/unire/internal/tools"
)

func TestNextRunSkipsMissedTimesAndRollsDailyOver(t *testing.T) {
	now := time.Date(2026, 11, 28, 10, 7, 0, 0, time.Local)
	every := store.Automation{EveryMinutes: 5}
	if got := nextRun(every, now); !got.Equal(now.Add(5 * time.Minute)) {
		t.Errorf("a new interval starts from now: %v", got)
	}
	// Down for an hour: it runs once now, and the next time stays on the old beat.
	every.NextRun = now.Add(-62 * time.Minute)
	if got := nextRun(every, now); !got.Equal(now.Add(3*time.Minute)) || !got.After(now) {
		t.Errorf("missed times are skipped, keeping the beat: %v", got)
	}
	daily := store.Automation{DailyAt: "09:00"}
	if got := nextRun(daily, now); got.Day() != 29 || got.Hour() != 9 || got.Minute() != 0 {
		t.Errorf("09:00 has passed today, so tomorrow: %v", got)
	}
	daily.DailyAt = "23:30"
	if got := nextRun(daily, now); got.Day() != 28 || got.Hour() != 23 || got.Minute() != 30 {
		t.Errorf("23:30 is still ahead today: %v", got)
	}
	if got := nextRun(store.Automation{DailyAt: "25:00"}, now); !got.IsZero() {
		t.Errorf("a bad time is never scheduled: %v", got)
	}
}

func TestAutomationFromChecksWhatThePageSent(t *testing.T) {
	ok := automationIn{Prompt: "merge dependabot", Agent: "claude", DailyAt: "9:05"}
	a, err := automationFrom(ok)
	if err != nil || a.DailyAt != "09:05" || !a.Enabled || a.Name == "" {
		t.Fatalf("a good one is kept (name from the prompt, time tidied, on): %+v %v", a, err)
	}
	for name, in := range map[string]automationIn{
		"no prompt":     {Agent: "claude", DailyAt: "09:00"},
		"unknown agent": {Prompt: "p", Agent: "nope", DailyAt: "09:00"},
		"no schedule":   {Prompt: "p", Agent: "claude"},
		"too often":     {Prompt: "p", Agent: "claude", EveryMinutes: 2},
		"usage over":    {Prompt: "p", Agent: "claude", DailyAt: "09:00", MaxUsage: 120},
		"negative cap":  {Prompt: "p", Agent: "claude", DailyAt: "09:00", MaxPerDay: -1},
	} {
		if _, err := automationFrom(in); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
}

func TestUsageOverLooksAtTheFullestWindow(t *testing.T) {
	u := agent.AgentUsage{Reported: true, UpdatedAt: 1, Windows: []agent.UsageWindow{{UsedPercent: 40}, {UsedPercent: 85}}}
	if over, high, ok := usageOver(u, 80); !over || high != 85 || !ok {
		t.Errorf("85%% in one window is over 80%%: %v %v %v", over, high, ok)
	}
	if over, _, _ := usageOver(u, 90); over {
		t.Error("85% is under 90%")
	}
	if _, _, ok := usageOver(agent.AgentUsage{Reported: true}, 80); ok {
		t.Error("no reading: nothing to go by")
	}
}

func TestAutomationRunsIntoItsOwnChatAndKeepsItsScheduleOverARestart(t *testing.T) {
	e := newEnv(t)
	e.setup()
	code, body := e.do("POST", "/api/automations", map[string]any{"name": "Nightly", "prompt": "say hello", "agent": "claude", "everyMinutes": 10, "maxPerDay": 2})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var v automationView
	_ = json.Unmarshal([]byte(body), &v)
	if v.NextRun == 0 || v.Schedule != "every 10 minutes" {
		t.Fatalf("it is scheduled: %+v", v)
	}
	ctx := context.Background()
	if n := e.srv.automationsOnce(ctx); n != 0 {
		t.Fatalf("not due yet, started %d", n)
	}

	due := func() {
		t.Helper()
		if err := e.srv.store.SetAutomationNextRun(v.ID, time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	due()
	if n := e.srv.automationsOnce(ctx); n != 1 {
		t.Fatalf("a due automation starts, started %d", n)
	}
	a, _, _ := e.srv.store.Automation(v.ID)
	if a.RootID == "" || !a.NextRun.After(time.Now()) {
		t.Fatalf("its chat is recorded and the next time moved on: %+v", a)
	}
	first, _, _ := e.srv.store.Session(a.RootID)
	if first.Prompt != "say hello" || first.Task != "Nightly" {
		t.Errorf("the chat shows the automation's task: %+v", first)
	}
	e.waitStatus(first.ID, "done")
	e.box.mu.Lock()
	told := strings.Join(e.box.prompts, "\n")
	e.box.mu.Unlock()
	for _, want := range []string{"started by itself, on a schedule", `automation "Nightly"`, "unire pr-list", "may not merge", "say hello"} {
		if !strings.Contains(told, want) {
			t.Errorf("the agent should be told %q", want)
		}
	}
	if strings.Contains(told, "unire pr-merge \"owner") {
		t.Error("an automation that may not merge does not get pr-merge")
	}
	if events, _ := e.srv.store.Events(first.ID, 0, 1000); !hasEventText(events, "scheduled run") {
		t.Error("the activity log says it was a scheduled run")
	}

	// A restart keeps the automation; the next run is a turn in the same chat.
	e.restart()
	due()
	if n := e.srv.automationsOnce(ctx); n != 1 {
		t.Fatalf("after a restart it still runs, started %d", n)
	}
	thread, _ := e.srv.threadOf(a.RootID)
	if len(thread) != 2 || thread[1].Prompt != "say hello" {
		t.Fatalf("the second run is the chat's second turn: %d turns", len(thread))
	}
	e.waitStatus(thread[1].ID, "done")

	// Two runs a day: the third is skipped, and the history says why.
	due()
	if n := e.srv.automationsOnce(ctx); n != 0 {
		t.Fatalf("over the daily cap, started %d", n)
	}
	runs, _ := e.srv.store.AutomationRuns(v.ID, 10)
	if len(runs) != 3 || runs[0].SessionID != "" || !strings.Contains(runs[0].Note, "2 of 2 times today") {
		t.Errorf("the skip is recorded: %+v", runs)
	}
	// "Run now" ignores the daily cap.
	if code, body := e.do("POST", "/api/automations/"+v.ID+"/run", nil); code != http.StatusOK {
		t.Fatalf("run now = %d %s", code, body)
	}
	thread, _ = e.srv.threadOf(a.RootID)
	e.waitStatus(thread[len(thread)-1].ID, "done")

	// Paused: never due.
	if code, _ := e.do("POST", "/api/automations/"+v.ID+"/enabled", map[string]bool{"on": false}); code != http.StatusOK {
		t.Fatal("pause failed")
	}
	if n := e.srv.automationsOnce(ctx); n != 0 {
		t.Errorf("a paused automation does not run, started %d", n)
	}
	if code, _ := e.do("DELETE", "/api/automations/"+v.ID, nil); code != http.StatusNoContent {
		t.Fatal("delete failed")
	}
	if _, ok, _ := e.srv.store.Session(a.RootID); !ok {
		t.Error("deleting an automation keeps its chat")
	}
}

func TestAutomationSkipsWhenUsageIsOverItsLimit(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a, err := e.srv.store.SaveAutomation(store.Automation{Name: "n", Prompt: "p", Agent: "claude", EveryMinutes: 5, MaxUsage: 50, Enabled: true, NextRun: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.srv.store.SaveUsage(agent.AgentUsage{AgentID: "claude", Reported: true, UpdatedAt: time.Now().Unix(), Windows: []agent.UsageWindow{{Name: "5-hour", UsedPercent: 72}}}); err != nil {
		t.Fatal(err)
	}
	if n := e.srv.automationsOnce(context.Background()); n != 0 {
		t.Fatalf("72%% used, limit 50%%: started %d", n)
	}
	runs, _ := e.srv.store.AutomationRuns(a.ID, 5)
	if len(runs) != 1 || !strings.Contains(runs[0].Note, "72%") {
		t.Errorf("the skip says why: %+v", runs)
	}
}

func hasEventText(events []store.Event, part string) bool {
	for _, ev := range events {
		if strings.Contains(ev.Text, part) {
			return true
		}
	}
	return false
}

// A GitHub with three open pull requests: #7 passes, #8 fails, #9 has no checks.
type fakeDependabot struct {
	mu     sync.Mutex
	merged []string // "number sha method"
}

func (f *fakeDependabot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pr := func(n int, title string) string {
		return `{"number":` + strconv.Itoa(n) + `,"title":"` + title + `","html_url":"http://gh/pr/` + strconv.Itoa(n) + `","state":"open","user":{"login":"dependabot[bot]"},"head":{"ref":"dependabot/x","sha":"s` + strconv.Itoa(n) + `"},"base":{"ref":"main"}}`
	}
	switch p := r.URL.Path; {
	case p == "/api/v3/user":
		w.Write([]byte(`{"login":"greg"}`))
	case p == "/api/v3/repos/o/r/pulls" && r.URL.Query().Get("state") == "open":
		w.Write([]byte("[" + pr(7, "Bump a") + "," + pr(8, "Bump b") + "," + pr(9, "Bump c") + "]"))
	case p == "/api/v3/repos/o/r/pulls/7":
		w.Write([]byte(pr(7, "Bump a")))
	case p == "/api/v3/repos/o/r/pulls/8":
		w.Write([]byte(pr(8, "Bump b")))
	case p == "/api/v3/repos/o/r/pulls/9":
		w.Write([]byte(pr(9, "Bump c")))
	case p == "/api/v3/repos/o/r/commits/s7/check-runs":
		w.Write([]byte(`{"check_runs":[{"id":1,"name":"test","status":"completed","conclusion":"success"}]}`))
	case p == "/api/v3/repos/o/r/commits/s8/check-runs":
		w.Write([]byte(`{"check_runs":[{"id":2,"name":"test","status":"completed","conclusion":"failure"}]}`))
	case strings.HasSuffix(p, "/check-runs"):
		w.Write([]byte(`{"check_runs":[]}`))
	case strings.HasSuffix(p, "/status"):
		w.Write([]byte(`{"statuses":[]}`))
	case strings.HasSuffix(p, "/merge") && r.Method == "PUT":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["merge_method"] == "merge" {
			http.Error(w, `{"message":"Merge commits are not allowed on this repository."}`, http.StatusMethodNotAllowed)
			return
		}
		f.mu.Lock()
		f.merged = append(f.merged, strings.TrimSuffix(strings.TrimPrefix(p, "/api/v3/repos/o/r/pulls/"), "/merge")+" "+body["sha"]+" "+body["merge_method"])
		f.mu.Unlock()
		w.Write([]byte(`{"merged":true}`))
	default:
		http.Error(w, "{}", 404)
	}
}

func TestPRMergeOnlyMergesPullRequestsWhoseChecksAllPassed(t *testing.T) {
	e := newEnv(t)
	e.setup()
	gh := &fakeDependabot{}
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	if code, body := e.do("POST", "/api/servers", map[string]any{"name": "G", "type": "github", "baseUrl": srv.URL, "token": "t"}); code != http.StatusCreated {
		t.Fatalf("add server = %d %s", code, body)
	}
	servers, _ := e.srv.store.GitServers()
	ref := store.RepoRef{ServerID: servers[0].ID, FullName: "o/r", CloneURL: srv.URL + "/o/r.git"}
	a, err := e.srv.store.SaveAutomation(store.Automation{Name: "deps", Prompt: "p", Agent: "claude", DailyAt: "09:00", Repos: []store.RepoRef{ref}, MayMerge: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := e.srv.automationToolsFor(a.ID, ""); len(got) != 2 || got[1].Name != tools.PRMerge.Name {
		t.Fatalf("an automation that may merge gets both tools: %+v", got)
	}
	if got := e.srv.automationToolsFor("", "some-other-chat"); got != nil {
		t.Fatalf("an ordinary chat gets neither: %+v", got)
	}

	list := e.srv.automationCall(ctx, a.ID, "", tools.PRList.Name, "")
	for _, want := range []string{"3 open pull request", "#7 Bump a", "dependabot[bot]", "all 1 checks passed", "1 of 1 checks failed", "no checks"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list should say %q:\n%s", want, list)
		}
	}
	if got := e.srv.automationCall(ctx, a.ID, "", tools.PRMerge.Name, "o/r#8"); !strings.HasPrefix(got, "Not merged") {
		t.Errorf("a failing pull request is not merged: %s", got)
	}
	if got := e.srv.automationCall(ctx, a.ID, "", tools.PRMerge.Name, "#9"); !strings.Contains(got, "no checks") {
		t.Errorf("a pull request without checks is not merged: %s", got)
	}
	if got := e.srv.automationCall(ctx, a.ID, "", tools.PRMerge.Name, "x/y#7"); !strings.Contains(got, "not one of this chat's repos") {
		t.Errorf("only the automation's repos: %s", got)
	}
	if got := e.srv.automationCall(ctx, a.ID, "", tools.PRMerge.Name, `"o/r#7"`); !strings.HasPrefix(got, "Merged o/r#7") {
		t.Errorf("a passing pull request is merged: %s", got)
	}
	gh.mu.Lock()
	merged := gh.merged
	gh.mu.Unlock()
	if len(merged) != 1 || merged[0] != "7 s7 squash" {
		t.Errorf("merged once, on the checked commit, with a method the repo allows: %v", merged)
	}

	// With merging turned off the agent is told so.
	a.MayMerge = false
	if _, err := e.srv.store.SaveAutomation(a); err != nil {
		t.Fatal(err)
	}
	if got := e.srv.automationCall(ctx, a.ID, "", tools.PRMerge.Name, "o/r#7"); !strings.Contains(got, "turned off") {
		t.Errorf("merging off: %s", got)
	}
}

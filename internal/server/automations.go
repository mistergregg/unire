package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/safe"
	"github.com/mistergregg/unire/internal/sessions"
	"github.com/mistergregg/unire/internal/store"
)

// Automations are tasks that run by themselves on a schedule: every few minutes or once
// a day. Each has a chat of its own and every run is a new turn in it, so its history
// reads like a conversation (and the owner can reply in it). The schedule is kept in the
// database, so a restart loses nothing: a run that fell due while Unire was down runs
// once when it is back, not once for every time it was missed.
//
// A run is skipped, with the reason recorded, when the last run is still going, when the
// automation already ran as often today as the owner allows, or when the agent's usage
// is at or above the owner's limit.

const (
	automationEvery      = 30 * time.Second // how often Unire looks for automations that are due
	minAutomationMinutes = 5
	maxAutomationMinutes = 7 * 24 * 60
	automationRunsShown  = 30
)

// runAutomations starts the automations that are due, a little after start-up and then
// every automationEvery, until the server closes.
func (s *Server) runAutomations(stop <-chan struct{}) {
	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			func() {
				defer safe.Recover("automations", nil)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				s.automationsOnce(ctx)
			}()
			t.Reset(automationEvery)
		}
	}
}

// automationsOnce starts every automation that is due and returns how many it started.
func (s *Server) automationsOnce(ctx context.Context) int {
	list, err := s.store.Automations()
	if err != nil {
		return 0
	}
	now := s.timeNow()
	n := 0
	for _, a := range list {
		if ctx.Err() != nil {
			break
		}
		if !a.Enabled || a.NextRun.IsZero() || now.Before(a.NextRun) {
			continue
		}
		// The next time is saved first, so a run that fails to start (or a crash) is
		// not tried again every 30 seconds.
		_ = s.store.SetAutomationNextRun(a.ID, nextRun(a, now))
		if id, _ := s.runAutomation(ctx, a, false); id != "" {
			n++
		}
	}
	return n
}

// nextRun is the first time after now that a runs on its schedule. Times missed while
// Unire was down are skipped. Daily times are in the server's time zone.
func nextRun(a store.Automation, now time.Time) time.Time {
	if a.EveryMinutes > 0 {
		step := time.Duration(a.EveryMinutes) * time.Minute
		if a.NextRun.IsZero() || a.NextRun.After(now.Add(step)) {
			return now.Add(step)
		}
		next := a.NextRun
		if !next.After(now) {
			missed := now.Sub(next)/step + 1
			next = next.Add(missed * step)
		}
		return next
	}
	h, m, ok := parseClock(a.DailyAt)
	if !ok {
		return time.Time{}
	}
	local := now.In(time.Local)
	next := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, time.Local)
	if !next.After(now) {
		next = time.Date(local.Year(), local.Month(), local.Day()+1, h, m, 0, 0, time.Local)
	}
	return next
}

// parseClock reads "HH:MM" (24-hour).
func parseClock(s string) (h, m int, ok bool) {
	hs, ms, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found || len(ms) != 2 || len(hs) < 1 || len(hs) > 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// scheduleText says when an automation runs, for the page.
func scheduleText(a store.Automation) string {
	switch {
	case a.EveryMinutes > 0 && a.EveryMinutes%(24*60) == 0:
		return plural(a.EveryMinutes/(24*60), "every day", "every %d days")
	case a.EveryMinutes > 0 && a.EveryMinutes%60 == 0:
		return plural(a.EveryMinutes/60, "every hour", "every %d hours")
	case a.EveryMinutes > 0:
		return fmt.Sprintf("every %d minutes", a.EveryMinutes)
	}
	return "every day at " + a.DailyAt
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return fmt.Sprintf(many, n)
}

// startOfDay is the start of now's day in the server's time zone: daily caps count from it.
func startOfDay(now time.Time) time.Time {
	l := now.In(time.Local)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.Local)
}

// usageOver reports whether the agent's usage is at or above limit percent in any of
// its windows, and the highest reading. ok is false when there is no reading to go by.
func usageOver(u agent.AgentUsage, limit int) (over bool, highest float64, ok bool) {
	if !u.Reported || u.UpdatedAt == 0 || len(u.Windows) == 0 {
		return false, 0, false
	}
	for _, w := range u.Windows {
		highest = math.Max(highest, w.UsedPercent)
	}
	return highest >= float64(limit), highest, true
}

// automationPreamble tells the agent that this turn was started by a schedule, not by
// the owner typing, and how to behave when nobody is watching.
func automationPreamble(a store.Automation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This turn was started by itself, on a schedule: it is a run of the owner's automation %q (%s). ", a.Name, scheduleText(a))
	b.WriteString("The owner is probably not watching, so do not wait on questions: do the task with your best judgment, and if something needs their decision, leave it and say so. ")
	b.WriteString("Your reply is the run's report, read later in this chat: start with what you did and found, keep it short, and say plainly when there was nothing to do. ")
	b.WriteString("To see open pull requests on the repos, use unire pr-list.")
	if a.MayMerge {
		b.WriteString(" The owner allows this automation to merge pull requests (unire pr-merge), only ones whose checks all passed; merge only what the task asks for.")
	} else {
		b.WriteString(" This automation may not merge pull requests: if the task asks for it, say what you would merge and that merging is turned off for it.")
	}
	b.WriteString("\n\nThe task:\n\n")
	return b.String()
}

// runAutomation starts one run of a now (manual: the owner pressed "Run now", which
// ignores the daily cap but not a run in progress). It returns the turn it started, or
// "" and why not; either way the outcome is recorded in the automation's history.
func (s *Server) runAutomation(ctx context.Context, a store.Automation, manual bool) (string, string) {
	id, note := s.startAutomation(ctx, a, manual)
	if id == "" {
		log.Printf("automation %q skipped: %s", a.Name, note)
	}
	_ = s.store.AddAutomationRun(a.ID, store.AutomationRun{At: s.timeNow(), SessionID: id, Note: note})
	return id, note
}

func (s *Server) startAutomation(ctx context.Context, a store.Automation, manual bool) (string, string) {
	now := s.timeNow()
	// The chat so far, if it still exists.
	var last *store.Session
	if a.RootID != "" {
		if thread, err := s.threadOf(a.RootID); err == nil && len(thread) > 0 {
			last = &thread[len(thread)-1]
		}
	}
	if last != nil && !store.Finished(last.Status) {
		return "", "skipped: the last run is still going"
	}
	if a.MaxPerDay > 0 && !manual {
		if n, err := s.store.AutomationRunsSince(a.ID, startOfDay(now)); err == nil && n >= a.MaxPerDay {
			return "", fmt.Sprintf("skipped: already ran %d of %d times today", n, a.MaxPerDay)
		}
	}
	var notes []string
	if manual {
		notes = append(notes, "automation "+a.Name+": started by the owner (Run now)")
	} else {
		notes = append(notes, "automation "+a.Name+": scheduled run ("+scheduleText(a)+")")
	}
	if a.MaxUsage > 0 {
		over, highest, ok := usageOver(s.usageFor(a.Agent), a.MaxUsage)
		switch {
		case over:
			return "", fmt.Sprintf("skipped: %s usage is at %.0f%% (limit %d%%)", a.Agent, highest, a.MaxUsage)
		case !ok:
			notes = append(notes, fmt.Sprintf("automation: no usage reading for %s yet, so the %d%% limit could not be checked", a.Agent, a.MaxUsage))
		}
	}
	model := a.Model
	pre := automationPreamble(a)
	if last != nil {
		started, err := s.startReplyWith(ctx, *last, a.Prompt, a.Agent, &model, nil, replyExtra{Preamble: pre, Notes: notes, Automation: a.ID})
		if err != nil {
			return "", "could not start: " + err.Error()
		}
		return started.ID, startedNote(manual)
	}
	// The first run (or the chat was deleted): a new chat.
	ad, err := agent.Get(a.Agent)
	if err != nil {
		return "", "could not start: " + err.Error()
	}
	if msg := s.requireSignedIn(ctx, ad); msg != "" {
		return "", "could not start: " + msg
	}
	if model == "" {
		model = s.agentDefaultModel(a.Agent)
	}
	repos, err := s.resolveRepos(a.Repos)
	if err != nil {
		return "", "could not start: " + err.Error()
	}
	x, err := s.sessions.Start(sessions.Request{Task: a.Name, Agent: a.Agent, Model: model, Prompt: a.Prompt, Mode: agent.HandsOff,
		Network: s.networkMode(), Memory: true, Repos: repos, Notes: notes, Preamble: pre, Automation: a.ID})
	if err != nil {
		return "", "could not start: " + err.Error()
	}
	_ = s.store.SetAutomationChat(a.ID, x.ID)
	return x.ID, startedNote(manual)
}

func startedNote(manual bool) string {
	if manual {
		return "started by you"
	}
	return "started on schedule"
}

// ---- API ----

type automationRunView struct {
	At        int64  `json:"at"`
	SessionID string `json:"sessionId,omitempty"`
	Note      string `json:"note"`
	Status    string `json:"status,omitempty"` // the turn's status, when it started one
}

type automationView struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Prompt       string              `json:"prompt"`
	Agent        string              `json:"agent"`
	Model        string              `json:"model"`
	Repos        []store.RepoRef     `json:"repos"`
	EveryMinutes int                 `json:"everyMinutes"`
	DailyAt      string              `json:"dailyAt"`
	MaxPerDay    int                 `json:"maxPerDay"`
	MaxUsage     int                 `json:"maxUsage"`
	MayMerge     bool                `json:"mayMerge"`
	Enabled      bool                `json:"enabled"`
	ChatID       string              `json:"chatId,omitempty"`
	Schedule     string              `json:"schedule"`
	NextRun      int64               `json:"nextRun"`
	RunsToday    int                 `json:"runsToday"`
	Running      bool                `json:"running"`
	Runs         []automationRunView `json:"runs"`
}

func (s *Server) automationView(a store.Automation) automationView {
	v := automationView{ID: a.ID, Name: a.Name, Prompt: a.Prompt, Agent: a.Agent, Model: a.Model, Repos: a.Repos,
		EveryMinutes: a.EveryMinutes, DailyAt: a.DailyAt, MaxPerDay: a.MaxPerDay, MaxUsage: a.MaxUsage, MayMerge: a.MayMerge,
		Enabled: a.Enabled, Schedule: scheduleText(a), Runs: []automationRunView{}}
	if a.Enabled && !a.NextRun.IsZero() {
		v.NextRun = a.NextRun.Unix()
	}
	if a.RootID != "" {
		if thread, err := s.threadOf(a.RootID); err == nil && len(thread) > 0 {
			v.ChatID = a.RootID
			v.Running = !store.Finished(thread[len(thread)-1].Status)
		}
	}
	v.RunsToday, _ = s.store.AutomationRunsSince(a.ID, startOfDay(s.timeNow()))
	runs, _ := s.store.AutomationRuns(a.ID, automationRunsShown)
	for _, r := range runs {
		rv := automationRunView{At: r.At.Unix(), SessionID: r.SessionID, Note: r.Note}
		if r.SessionID != "" {
			if x, ok, err := s.store.Session(r.SessionID); err == nil && ok {
				rv.Status = x.Status
			}
		}
		v.Runs = append(v.Runs, rv)
	}
	return v
}

func (s *Server) listAutomations(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.Automations()
	if err != nil {
		serverError(w)
		return
	}
	out := make([]automationView, 0, len(list))
	for _, a := range list {
		out = append(out, s.automationView(a))
	}
	now := s.timeNow().In(time.Local)
	zone, _ := now.Zone()
	name := time.Local.String() // "Local" when it comes from /etc/localtime rather than TZ
	if name == "Local" {
		name = zone
	}
	writeJSON(w, http.StatusOK, map[string]any{"automations": out, "timeZone": name, "zone": zone, "now": now.Unix(), "serverClock": now.Format("15:04")})
}

type automationIn struct {
	Name         string          `json:"name"`
	Prompt       string          `json:"prompt"`
	Agent        string          `json:"agent"`
	Model        string          `json:"model"`
	Repos        []store.RepoRef `json:"repos"`
	EveryMinutes int             `json:"everyMinutes"`
	DailyAt      string          `json:"dailyAt"`
	MaxPerDay    int             `json:"maxPerDay"`
	MaxUsage     int             `json:"maxUsage"`
	MayMerge     bool            `json:"mayMerge"`
	Enabled      *bool           `json:"enabled"` // omitted: on
}

// automationFrom checks what the page sent and turns it into an automation.
func automationFrom(in automationIn) (store.Automation, error) {
	a := store.Automation{Name: strings.TrimSpace(in.Name), Prompt: strings.TrimSpace(in.Prompt), Agent: strings.TrimSpace(in.Agent),
		Model: strings.TrimSpace(in.Model), Repos: in.Repos, EveryMinutes: in.EveryMinutes, DailyAt: strings.TrimSpace(in.DailyAt),
		MaxPerDay: in.MaxPerDay, MaxUsage: in.MaxUsage, MayMerge: in.MayMerge, Enabled: in.Enabled == nil || *in.Enabled}
	if a.Name == "" {
		a.Name = taskNameFrom(a.Prompt)
	}
	switch {
	case a.Prompt == "" || len(a.Prompt) > 20000:
		return a, errors.New("say what the automation should do (20,000 characters max)")
	case len(a.Name) > 80:
		return a, errors.New("the name is too long (80 characters max)")
	case len(a.Repos) > 10:
		return a, errors.New("an automation can use at most 10 repos")
	case a.MaxPerDay < 0 || a.MaxPerDay > 1000:
		return a, errors.New("runs a day must be between 0 (no limit) and 1000")
	case a.MaxUsage < 0 || a.MaxUsage > 100:
		return a, errors.New("the usage limit must be a percentage between 0 (no limit) and 100")
	}
	if _, err := agent.Get(a.Agent); err != nil {
		return a, err
	}
	if a.Model != "" {
		if err := agent.ValidateModel(a.Model); err != nil {
			return a, err
		}
	}
	if a.EveryMinutes != 0 {
		if a.EveryMinutes < minAutomationMinutes || a.EveryMinutes > maxAutomationMinutes {
			return a, fmt.Errorf("run it every %d minutes to 7 days", minAutomationMinutes)
		}
		a.DailyAt = ""
	} else {
		h, m, ok := parseClock(a.DailyAt)
		if !ok {
			return a, errors.New("give a time of day like 09:00, or how many minutes between runs")
		}
		a.DailyAt = fmt.Sprintf("%02d:%02d", h, m)
	}
	return a, nil
}

func (s *Server) createAutomation(w http.ResponseWriter, r *http.Request) {
	var in automationIn
	if !readJSON(w, r, &in) {
		return
	}
	a, err := automationFrom(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.resolveRepos(a.Repos); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if a.Enabled {
		a.NextRun = nextRun(a, s.timeNow())
	}
	saved, err := s.store.SaveAutomation(a)
	if err != nil {
		serverError(w)
		return
	}
	writeJSON(w, http.StatusCreated, s.automationView(saved))
}

func (s *Server) lookupAutomation(w http.ResponseWriter, r *http.Request) (store.Automation, bool) {
	a, ok, err := s.store.Automation(r.PathValue("id"))
	if err != nil {
		serverError(w)
		return a, false
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such automation")
	}
	return a, ok
}

func (s *Server) updateAutomation(w http.ResponseWriter, r *http.Request) {
	old, ok := s.lookupAutomation(w, r)
	if !ok {
		return
	}
	var in automationIn
	if !readJSON(w, r, &in) {
		return
	}
	a, err := automationFrom(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.resolveRepos(a.Repos); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.ID, a.RootID = old.ID, old.RootID
	if a.Enabled {
		// A new schedule starts from now; an unchanged one keeps its next time.
		if old.Enabled && old.EveryMinutes == a.EveryMinutes && old.DailyAt == a.DailyAt && !old.NextRun.IsZero() {
			a.NextRun = old.NextRun
		} else {
			a.NextRun = nextRun(a, s.timeNow())
		}
	}
	saved, err := s.store.SaveAutomation(a)
	if err != nil {
		serverError(w)
		return
	}
	writeJSON(w, http.StatusOK, s.automationView(saved))
}

func (s *Server) deleteAutomation(w http.ResponseWriter, r *http.Request) {
	a, ok := s.lookupAutomation(w, r)
	if !ok {
		return
	}
	if _, err := s.store.DeleteAutomation(a.ID); err != nil {
		serverError(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setAutomationEnabled pauses an automation or turns it back on (it then runs at the
// next time on its schedule, not at once).
func (s *Server) setAutomationEnabled(w http.ResponseWriter, r *http.Request) {
	a, ok := s.lookupAutomation(w, r)
	if !ok {
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var next time.Time
	if req.On {
		a.NextRun = time.Time{}
		next = nextRun(a, s.timeNow())
	}
	if err := s.store.SetAutomationEnabled(a.ID, req.On, next); err != nil {
		serverError(w)
		return
	}
	a.Enabled, a.NextRun = req.On, next
	writeJSON(w, http.StatusOK, s.automationView(a))
}

// runAutomationNow is the "Run now" button. The schedule is not moved.
func (s *Server) runAutomationNow(w http.ResponseWriter, r *http.Request) {
	a, ok := s.lookupAutomation(w, r)
	if !ok {
		return
	}
	id, note := s.runAutomation(r.Context(), a, true)
	if id == "" {
		writeErr(w, http.StatusConflict, note)
		return
	}
	if got, ok, _ := s.store.Automation(a.ID); ok {
		a = got
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": id, "automation": s.automationView(a)})
}

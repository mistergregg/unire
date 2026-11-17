package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/egress"
	"github.com/mistergregg/unire/internal/repopick"
	"github.com/mistergregg/unire/internal/sessions"
	"github.com/mistergregg/unire/internal/store"
)

// A conversation is a chain of sessions: the first one, then one per reply.
// Each reply is a new run that starts from the branch the one before pushed.

const maxThread = 100 // a guard against a loop, far more than anyone replies

// threadOf returns every session of the conversation that id belongs to, first to last.
func (s *Server) threadOf(id string) ([]store.Session, error) {
	x, ok, err := s.store.Session(id)
	if err != nil || !ok {
		return nil, err
	}
	for i := 0; i < maxThread && x.ParentID != ""; i++ {
		p, ok, err := s.store.Session(x.ParentID)
		if err != nil {
			return nil, err
		}
		if !ok {
			break // the earlier turns were deleted
		}
		x = p
	}
	out := []store.Session{x}
	for len(out) < maxThread {
		c, ok, err := s.store.ChildOf(x.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		out, x = append(out, c), c
	}
	return out, nil
}

// viewWithPushed is toView plus the repos the session pushed a branch to.
func (s *Server) viewWithPushed(x store.Session) sessionView {
	v := toView(x)
	if events, err := s.store.Events(x.ID, 0, 5000); err == nil {
		v.Pushed = pushedFrom(events, x.Repos)
	}
	return v
}

func (s *Server) getThread(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	list, err := s.threadOf(x.ID)
	if err != nil {
		serverError(w)
		return
	}
	out := make([]sessionView, 0, len(list))
	for _, t := range list {
		out = append(out, s.viewWithPushed(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// requireSignedIn refuses when an agent whose sign-in check is verified is
// known to be signed out. A guess never blocks anyone.
func (s *Server) requireSignedIn(r *http.Request, ad agent.Adapter) string {
	if ad.Info().Verified.Status {
		if st := s.agentStatus(r.Context(), ad); st.known && !st.loggedIn {
			return ad.Info().Label + " is not signed in. Open the Agents page and sign in first."
		}
	}
	return ""
}

func (s *Server) replyToSession(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
		Agent  string `json:"agent"` // empty: the same agent
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	switch {
	case req.Prompt == "" || len(req.Prompt) > 20000:
		writeErr(w, http.StatusBadRequest, "write a message (20,000 characters max)")
		return
	case !store.Finished(x.Status):
		writeErr(w, http.StatusConflict, "wait until the agent has finished, or stop it")
		return
	}
	if _, has, _ := s.store.ChildOf(x.ID); has {
		writeErr(w, http.StatusConflict, "this conversation has already moved on")
		return
	}
	thread, err := s.threadOf(x.ID)
	if err != nil {
		serverError(w)
		return
	}
	if req.Agent == "" {
		req.Agent = x.Agent
	}
	ad, err := agent.Get(req.Agent)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := s.requireSignedIn(r, ad); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	// Build on the branch the last turn pushed; a repo it left alone starts as before.
	pushed := map[string]bool{}
	if events, err := s.store.Events(x.ID, 0, 5000); err == nil {
		for _, name := range pushedFrom(events, x.Repos) {
			pushed[name] = true
		}
	}
	refs := make([]store.RepoRef, len(x.Repos))
	for i, ref := range x.Repos {
		if pushed[ref.FullName] {
			ref.Base = x.Branch
		}
		refs[i] = ref
	}
	// A repo the owner now mentions (or one usually used with the repos here) joins
	// the conversation. The repos already in it stay.
	var picked []repopick.Candidate
	if len(refs) < 10 {
		picked = repopick.Auto(s.rankRepos(r.Context(), req.Prompt, refs))
		if room := 10 - len(refs); len(picked) > room {
			picked = picked[:room]
		}
		for _, c := range picked {
			refs = append(refs, refOf(c))
		}
	}
	repos, err := s.resolveRepos(refs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	mode, ok := egress.ParseMode(x.Network)
	if !ok {
		mode = s.networkMode()
	}
	started, err := s.sessions.Start(sessions.Request{
		Task: x.Task, Agent: req.Agent, Prompt: req.Prompt, Preamble: replyPreamble(thread, s.lastAnswer), Branch: sameAgentBranch(x, req.Agent),
		Mode: agent.HandsOff, Network: mode, Memory: x.Memory, Repos: repos, Parent: x.ID, Notes: pickNotes(picked),
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toView(started))
}

// Sizes of what a reply tells the agent about the conversation so far, in
// characters. The latest turn is given nearly in full; older ones are boiled down
// to what was asked and how it ended, newest first, until the budget is spent.
const (
	lastRequestChars = 2000
	lastAnswerChars  = 5000
	olderRequest     = 400
	olderAnswer      = 600
	olderBudget      = 5000
)

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// replyPreamble tells the agent that this continues earlier work: what was asked,
// what came of it, and where the work is. It goes before what the owner wrote and
// is not shown as part of it. A reply is a new conversation for the agent, and the
// owner may have switched agents, so the whole conversation is boiled down rather
// than only the last turn. thread is every turn so far, oldest first.
func replyPreamble(thread []store.Session, answer func(id string) string) string {
	if len(thread) == 0 {
		return ""
	}
	last := thread[len(thread)-1]
	b := "This continues earlier work in the same conversation.\n"

	// Older turns, newest first while the budget lasts, then shown oldest first.
	var older []string
	used, left := 0, 0
	for i := len(thread) - 2; i >= 0; i-- {
		t := thread[i]
		entry := fmt.Sprintf("Turn %d (%s)\nAsked: %s\n", i+1, t.Agent, clip(t.Prompt, olderRequest))
		if a := clip(answer(t.ID), olderAnswer); a != "" {
			entry += "Result: " + a + "\n"
		}
		if used+len(entry) > olderBudget && len(older) > 0 {
			left = i + 1
			break
		}
		used += len(entry)
		older = append([]string{entry}, older...)
	}
	if left > 0 {
		b += fmt.Sprintf("(%d earlier turns are left out.)\n", left)
	}
	if len(older) > 0 {
		b += "What happened before the last turn:\n<earlier-turns>\n" + strings.Join(older, "\n") + "</earlier-turns>\n"
	}
	b += "In the last turn you were asked:\n<earlier-request>\n" + clip(last.Prompt, lastRequestChars) + "\n</earlier-request>\n"
	if a := strings.TrimSpace(answer(last.ID)); a != "" {
		b += "and the answer was:\n<earlier-answer>\n" + clip(a, lastAnswerChars) + "\n</earlier-answer>\n"
	}
	return b + "The work so far is on branch " + last.Branch + " in the repos that changed " +
		"(git log and git diff show it). The owner now writes:\n\n"
}

// lastAnswer is what the agent printed as its answer in a session.
func (s *Server) lastAnswer(id string) string {
	events, err := s.store.Events(id, 0, 5000)
	if err != nil {
		return ""
	}
	var lines []string
	for _, e := range events {
		if e.Kind == "out" {
			lines = append(lines, e.Text)
		}
	}
	return strings.Join(lines, "\n")
}

// answerQuestion gives the owner's answer to a question the agent is waiting on.
func (s *Server) answerQuestion(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	var req struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.ID == "" || req.Text == "" || len(req.Text) > 10000 {
		writeErr(w, http.StatusBadRequest, "write an answer (10,000 characters max)")
		return
	}
	if err := s.sessions.Answer(x.ID, req.ID, req.Text); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// sameAgentBranch is the branch a reply continues on: its conversation's own,
// when the same agent carries on. A different agent starts a new branch from
// the same work.
func sameAgentBranch(prev store.Session, agentID string) string {
	if agentID == prev.Agent {
		return prev.Branch
	}
	return ""
}

// steerSession gives a running agent a message (see sessions.Manager.Steer).
func (s *Server) steerSession(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	var req struct {
		Text string `json:"text"`
		Now  bool   `json:"now"` // interrupt and carry on from the message
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || len(req.Text) > 20000 {
		writeErr(w, http.StatusBadRequest, "write a message (20,000 characters max)")
		return
	}
	id, err := s.sessions.Steer(x.ID, req.Text, req.Now)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// promoteSteer makes the agent take a message that is still waiting, now.
func (s *Server) promoteSteer(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	if err := s.sessions.Promote(x.ID, r.PathValue("msg")); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

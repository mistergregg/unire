package server

import (
	"net/http"
	"strings"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/egress"
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
		Task: x.Task, Agent: req.Agent, Prompt: req.Prompt, Preamble: replyPreamble(x),
		Mode: agent.HandsOff, Network: mode, Memory: x.Memory, Repos: repos, Parent: x.ID,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toView(started))
}

// replyPreamble tells the agent that this continues earlier work. It goes before
// what the owner wrote and is not shown as part of it.
func replyPreamble(prev store.Session) string {
	earlier := prev.Prompt
	if r := []rune(earlier); len(r) > 2000 {
		earlier = string(r[:2000]) + "…"
	}
	return "This continues earlier work in the same conversation. Earlier you were asked:\n<earlier>\n" + earlier +
		"\n</earlier>\nThe work so far is on branch " + prev.Branch + " in the repos that changed " +
		"(git log and git diff show it). The owner now writes:\n\n"
}

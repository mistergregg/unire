package server

import (
	"net/http"
	"strings"

	"github.com/mistergregg/unire/internal/pulls"
)

// getPulls returns a conversation's pull requests with their current state, and
// the pushed branches that could become one. The owner is asked about those.
func (s *Server) getPulls(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	thread, err := s.threadOf(x.ID)
	if err != nil || len(thread) == 0 {
		serverError(w)
		return
	}
	props, err := s.pulls.Proposals(thread)
	if err != nil {
		serverError(w)
		return
	}
	list, err := s.pulls.List(r.Context(), thread[0].ID)
	if err != nil {
		serverError(w)
		return
	}
	if props == nil {
		props = []pulls.Proposal{}
	}
	if list == nil {
		list = []pulls.View{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": props, "pulls": list})
}

// openPull opens a pull request for a branch the conversation pushed, after the
// owner said yes.
func (s *Server) openPull(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	var req pulls.Proposal
	if !readJSON(w, r, &req) {
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if len(req.Title) > 250 || len(req.Body) > 20000 {
		writeErr(w, http.StatusBadRequest, "the title or description is too long")
		return
	}
	thread, err := s.threadOf(x.ID)
	if err != nil || len(thread) == 0 {
		serverError(w)
		return
	}
	v, err := s.pulls.Open(r.Context(), thread, req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

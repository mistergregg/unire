package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mistergregg/unire/internal/gitprovider"
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

// testPullRequestAccess checks what a git server's token can do with pull
// requests in one repo (the first it can see, unless the owner names one),
// without changing anything.
func (s *Server) testPullRequestAccess(w http.ResponseWriter, r *http.Request) {
	g, ok := s.lookup(w, r)
	if !ok {
		return
	}
	var req struct {
		Repo string `json:"repo"`
	}
	if r.ContentLength > 0 && !readJSON(w, r, &req) {
		return
	}
	p, err := s.provider(g)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	checker, ok := p.(gitprovider.PermissionChecker)
	if !ok {
		writeErr(w, http.StatusBadRequest, "this git server type cannot open pull requests")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	repo := strings.TrimSpace(req.Repo)
	var caps []gitprovider.Capability
	if repo != "" {
		caps, err = checker.CheckPullRequestAccess(ctx, repo)
	} else {
		// No repo named: try a few and report the first one the account can write to,
		// since a read-only repo or one with pull requests turned off says nothing
		// about the token.
		repos, lerr := p.ListRepos(ctx, "")
		if lerr != nil {
			writeErr(w, http.StatusBadRequest, lerr.Error())
			return
		}
		if len(repos) == 0 {
			writeErr(w, http.StatusBadRequest, "the token can see no repos to check against")
			return
		}
		for i, r := range repos {
			if i >= 6 {
				break
			}
			c, cerr := checker.CheckPullRequestAccess(ctx, r.FullName)
			if cerr != nil {
				if err == nil && caps == nil {
					err = cerr
				}
				continue
			}
			if caps == nil || (c[len(c)-1].OK && !caps[len(caps)-1].OK) {
				repo, caps, err = r.FullName, c, nil
			}
			if c[len(c)-1].OK && c[0].OK {
				break // writable, with pull requests on: a fair test
			}
		}
	}
	if err != nil || caps == nil {
		if err == nil {
			err = errors.New("could not check any repo")
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repo": repo, "capabilities": caps})
}

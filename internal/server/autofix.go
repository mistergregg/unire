package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/mistergregg/unire/internal/safe"
	"github.com/mistergregg/unire/internal/store"
)

// A conversation whose pull request has failing checks starts a turn to fix them by
// itself: Unire reads why the checks failed (only it holds the token that can read the
// logs), tells the agent, and the agent pushes a fix to the same branch. It is on by
// default for each conversation, can be turned off on the pull request's card, and stops
// after a few tries per pull request, so a failure the agent cannot fix cannot loop.

const (
	maxAutoFix   = 3
	autoFixEvery = time.Minute
)

// runAutoFix looks for failing checks a little after start-up and then every minute,
// until the server closes.
func (s *Server) runAutoFix(stop <-chan struct{}) {
	t := time.NewTimer(90 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			func() {
				defer safe.Recover("automatic fix", nil)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				s.autoFixOnce(ctx)
			}()
			t.Reset(autoFixEvery)
		}
	}
}

// autoFixOnce starts a fix in every conversation that needs one and returns how many.
func (s *Server) autoFixOnce(ctx context.Context) int {
	roots, err := s.store.ConversationsWithOpenPullRequests()
	if err != nil {
		return 0
	}
	n := 0
	for _, root := range roots {
		if ctx.Err() != nil {
			break
		}
		if s.autoFixConversation(ctx, root) {
			n++
		}
	}
	return n
}

// autoFixConversation starts one fix turn if the conversation is idle and one of its open
// pull requests has finished checks that failed, on a commit not tried yet, with tries left.
func (s *Server) autoFixConversation(ctx context.Context, rootID string) bool {
	root, ok, err := s.store.Session(rootID)
	if err != nil || !ok || !root.AutoFix {
		return false
	}
	thread, err := s.threadOf(rootID)
	if err != nil || len(thread) == 0 {
		return false
	}
	last := thread[len(thread)-1]
	if last.Status != store.StatusDone {
		return false // still working, or ended some other way: the owner decides
	}
	recs, err := s.store.PullRequests(rootID)
	if err != nil {
		return false
	}
	views, err := s.pulls.List(ctx, rootID)
	if err != nil || len(views) != len(recs) {
		return false
	}
	for i, v := range views {
		rec := recs[i]
		switch {
		case v.State != "open", v.Summary != "failure", v.Pending, v.HeadSHA == "":
			continue // not failing, or not finished (the logs would be incomplete)
		case rec.FixSHA == v.HeadSHA:
			continue // this commit was already tried
		case rec.FixAttempts >= maxAutoFix:
			continue
		}
		try := rec.FixAttempts + 1
		prompt := fmt.Sprintf("Unire is starting this turn by itself because the checks on pull request #%d failed (try %d of %d; the owner can turn this off on the pull request's card).\n\n%s\n"+
			"Find the cause from the log above, fix it on this branch, check what you can by running it, and push so the pull request updates. "+
			"If the failure is not something you can fix (a missing secret, a service that is down), say so plainly instead of guessing.",
			v.Number, try, maxAutoFix, s.pulls.FailureMessage(ctx, v))
		// Recorded first, so a start that is slow or fails is never repeated for this commit.
		_ = s.store.RecordFixAttempt(rec.ServerID, rec.Repo, rec.Number, v.HeadSHA)
		if _, err := s.startReply(ctx, last, prompt, "", "", nil); err != nil {
			log.Printf("automatic fix of %s#%d could not start: %v", rec.Repo, rec.Number, err)
			return false
		}
		return true // one turn per conversation at a time
	}
	return false
}

// setAutoFix turns fixing failing checks by itself on or off for a conversation.
func (s *Server) setAutoFix(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	thread, err := s.threadOf(x.ID)
	if err != nil || len(thread) == 0 {
		serverError(w)
		return
	}
	if err := s.store.SetAutoFix(thread[0].ID, req.On); err != nil {
		serverError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"autoFix": req.On, "maxFix": maxAutoFix})
}

// getPullFailure gives the message for the owner's "fix the checks" button: why the
// checks of one pull request failed, with the log, ready to send as a reply.
func (s *Server) getPullFailure(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	number, _ := strconv.Atoi(r.URL.Query().Get("number"))
	thread, err := s.threadOf(x.ID)
	if err != nil || len(thread) == 0 {
		serverError(w)
		return
	}
	views, err := s.pulls.List(r.Context(), thread[0].ID)
	if err != nil {
		serverError(w)
		return
	}
	for _, v := range views {
		if v.ServerID == r.URL.Query().Get("serverId") && v.Repo == r.URL.Query().Get("repo") && v.Number == number {
			msg := s.pulls.FailureMessage(r.Context(), v)
			if msg == "" {
				writeErr(w, http.StatusConflict, "no check is failing on that pull request")
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"message": msg + "\nFind the cause from the log, fix it on this branch, check what you can by running it, and push so the pull request updates."})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "this conversation has no such pull request")
}

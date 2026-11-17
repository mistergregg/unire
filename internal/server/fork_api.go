package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/mistergregg/unire/internal/store"
)

// A chat can be started from another one: the new chat gets a boiled-down account
// of the old conversation and starts from the branches it pushed, so work carries on
// without the old chat's whole history. (A parent chat is different: it keeps
// supplying updates, see upstream.go.)

// forkState is the repos a chat built on this conversation starts with (each from the
// branch the conversation last pushed, when it pushed one), and lines saying where the
// work is, for the new agent.
func (s *Server) forkState(ctx context.Context, thread []store.Session) (repos []store.RepoRef, where []string) {
	if len(thread) == 0 {
		return nil, nil
	}
	last := thread[len(thread)-1]
	pushedBy := map[string]string{} // repo full name -> the branch the latest turn that pushed it used
	for _, t := range thread {
		for _, name := range s.viewWithPushed(t).Pushed {
			pushedBy[name] = t.Branch
		}
	}
	for _, ref := range last.Repos {
		if b, ok := pushedBy[ref.FullName]; ok {
			ref.Base = b
			where = append(where, fmt.Sprintf("- %s: the work is on branch %s", ref.FullName, b))
		} else {
			where = append(where, fmt.Sprintf("- %s: nothing was pushed", ref.FullName))
		}
		repos = append(repos, ref)
	}
	if prs, err := s.pulls.List(ctx, thread[0].ID); err == nil {
		for _, p := range prs {
			where = append(where, fmt.Sprintf("- pull request #%d in %s (%s): %s", p.Number, p.Repo, orUnknown(p.State), p.URL))
		}
	}
	return repos, where
}

func orUnknown(s string) string {
	if s == "" {
		return "state unknown"
	}
	return s
}

// handoffPreamble tells the agent of a new chat what happened in the conversation it
// was started from.
func handoffPreamble(title string, thread []store.Session, answer func(id string) string, where []string) string {
	b := fmt.Sprintf("This chat was started from another conversation, %q, so its work can be carried on here. What happened there:\n", title)
	b += conversationDigest(thread, answer, "In its last turn the agent there was asked:", "and answered:")
	if len(where) > 0 {
		b += "Where the work is (the repos here start from these branches):\n" + strings.Join(where, "\n") + "\n"
	}
	return b + "The owner now writes:\n\n"
}

// getFork gives the New chat form what it needs to start from a conversation: its
// repos on the branches it pushed, and how to describe it.
func (s *Server) getFork(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	thread, err := s.threadOf(x.ID)
	if err != nil || len(thread) == 0 {
		serverError(w)
		return
	}
	repos, _ := s.forkState(r.Context(), thread)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": thread[0].ID, "title": thread[0].Task, "agent": thread[len(thread)-1].Agent, "repos": repos,
	})
}

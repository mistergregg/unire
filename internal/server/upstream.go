package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/mistergregg/unire/internal/store"
)

// A conversation can have a parent: another conversation it keeps getting updates
// from. The child is told what the parent has done since it last looked, with each
// message it sends (and can ask for the latest while it works), so the two stay in
// step without the child repeating the parent's whole history. The link is kept on the
// first session of the child; "seen" counts the parent's finished turns it was told about.

// finishedTurns are the turns of a conversation that have ended.
func finishedTurns(thread []store.Session) []store.Session {
	var out []store.Session
	for _, t := range thread {
		if store.Finished(t.Status) {
			out = append(out, t)
		}
	}
	return out
}

// upstreamUpdate is what the child conversation (its first session) has not yet been
// told about its parent: everything the first time, then the turns finished since. It
// returns the text ("" when there is nothing new) and how many finished turns of the
// parent that makes seen.
func (s *Server) upstreamUpdate(ctx context.Context, child store.Session, all bool) (string, int) {
	if child.UpstreamID == "" {
		return "", child.UpstreamSeen
	}
	parent, err := s.threadOf(child.UpstreamID)
	if err != nil || len(parent) == 0 {
		return "", child.UpstreamSeen
	}
	done := finishedTurns(parent)
	seen := child.UpstreamSeen
	if all {
		seen = 0
	}
	if len(done) <= seen {
		return "", len(done)
	}
	_, where := s.forkState(ctx, parent)
	var b strings.Builder
	title := parent[0].Task
	if seen == 0 {
		fmt.Fprintf(&b, "This chat has a parent chat, %q, which it follows. What has happened there so far:\n", title)
		b.WriteString(conversationDigest(done, s.turnDigestText, "Its latest request was:", "and the result:"))
	} else {
		fmt.Fprintf(&b, "The parent chat, %q, has moved on since you last heard (%d new turn(s)):\n", title, len(done)-seen)
		b.WriteString(conversationDigest(done[seen:], s.turnDigestText, "Its latest request was:", "and the result:"))
	}
	if len(where) > 0 {
		b.WriteString("Where the parent's work is now:\n" + strings.Join(where, "\n") + "\n")
	}
	return b.String(), len(done)
}

// Latest answers the agent's parent tool: everything about the parent as it stands, and
// it counts as seen.
func (s *Server) Latest(ctx context.Context, childRoot string) string {
	child, ok, err := s.store.Session(childRoot)
	if err != nil || !ok || child.UpstreamID == "" {
		return "This chat has no parent chat."
	}
	text, n := s.upstreamUpdate(ctx, child, true)
	if text == "" {
		return "The parent chat has not finished anything yet."
	}
	_ = s.store.SetUpstreamSeen(child.ID, n)
	return text
}

// linkView describes a conversation's links for the chat header.
type linkView struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status,omitempty"`
	Updates int    `json:"updates,omitempty"` // finished turns of the parent not yet told to the child
}

func (s *Server) linkOf(id string, child *store.Session) (linkView, bool) {
	p, ok, err := s.store.Session(id)
	if err != nil || !ok {
		return linkView{}, false
	}
	v := linkView{ID: p.ID, Title: p.Task}
	if thread, err := s.threadOf(p.ID); err == nil && len(thread) > 0 {
		last := thread[len(thread)-1]
		v.Title, v.Status = thread[0].Task, last.Status
		if child != nil {
			if n := len(finishedTurns(thread)) - child.UpstreamSeen; n > 0 {
				v.Updates = n
			}
		}
	}
	return v, true
}

// getLinks says how a conversation relates to others: where it was started from, its
// parent (and how many updates it has not been told), and the conversations that have
// it as their parent.
func (s *Server) getLinks(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	thread, err := s.threadOf(x.ID)
	if err != nil || len(thread) == 0 {
		serverError(w)
		return
	}
	root := thread[0]
	out := map[string]any{"children": []linkView{}}
	if root.ForkedFrom != "" {
		if v, ok := s.linkOf(root.ForkedFrom, nil); ok {
			out["forkedFrom"] = v
		}
	}
	if root.UpstreamID != "" {
		if v, ok := s.linkOf(root.UpstreamID, &root); ok {
			out["parent"] = v
		}
	}
	if kids, err := s.store.ConversationsLinkedTo(root.ID); err == nil {
		list := []linkView{}
		for _, k := range kids {
			if v, ok := s.linkOf(k.ID, nil); ok {
				list = append(list, v)
			}
		}
		out["children"] = list
	}
	writeJSON(w, http.StatusOK, out)
}

// setParent links a conversation to a parent (or unlinks it with an empty parent).
func (s *Server) setParent(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	var req struct {
		Parent string `json:"parent"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	thread, err := s.threadOf(x.ID)
	if err != nil || len(thread) == 0 {
		serverError(w)
		return
	}
	root := thread[0]
	if req.Parent != "" {
		pt, err := s.threadOf(req.Parent)
		if err != nil || len(pt) == 0 {
			writeErr(w, http.StatusBadRequest, "that chat does not exist")
			return
		}
		if pt[0].ID == root.ID {
			writeErr(w, http.StatusBadRequest, "a chat cannot be its own parent")
			return
		}
		// No loops: the new parent must not already follow this chat, however far up.
		up := pt[0]
		for i := 0; i < 50 && up.UpstreamID != ""; i++ {
			if up.UpstreamID == root.ID {
				writeErr(w, http.StatusBadRequest, "that chat already follows this one")
				return
			}
			next, ok, _ := s.store.Session(up.UpstreamID)
			if !ok {
				break
			}
			up = next
		}
		req.Parent = pt[0].ID
	}
	if err := s.store.SetUpstream(root.ID, req.Parent); err != nil {
		serverError(w)
		return
	}
	s.getLinks(w, r)
}

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
func (s *Server) requireSignedIn(ctx context.Context, ad agent.Adapter) string {
	if ad.Info().Verified.Status {
		if st := s.agentStatus(ctx, ad); st.known && !st.loggedIn {
			return ad.Info().Label + " is not signed in. Open the Agents page and sign in first."
		}
	}
	return ""
}

// replyError is why a reply could not be started, with the HTTP status to report it with.
type replyError struct {
	code int
	msg  string
}

func (e replyError) Error() string { return e.msg }

func replyFail(code int, msg string) error { return replyError{code, msg} }

// startReply starts the next turn of the conversation that x ends. It is the one path
// for a reply the owner types and for one Unire starts itself (see autofix.go).
func (s *Server) startReply(ctx context.Context, x store.Session, prompt, agentID string, images []agent.Image) (store.Session, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && len(images) > 0 {
		prompt = "Inspect the attached image or images and use them as context for this task."
	}
	switch {
	case prompt == "" || len(prompt) > 20000:
		return store.Session{}, replyFail(http.StatusBadRequest, "write a message (20,000 characters max)")
	case !store.Finished(x.Status):
		return store.Session{}, replyFail(http.StatusConflict, "wait until the agent has finished, or stop it")
	}
	if _, has, _ := s.store.ChildOf(x.ID); has {
		return store.Session{}, replyFail(http.StatusConflict, "this conversation has already moved on")
	}
	thread, err := s.threadOf(x.ID)
	if err != nil {
		return store.Session{}, err
	}
	if agentID == "" {
		agentID = x.Agent
	}
	ad, err := agent.Get(agentID)
	if err != nil {
		return store.Session{}, replyFail(http.StatusBadRequest, err.Error())
	}
	if msg := s.requireSignedIn(ctx, ad); msg != "" {
		return store.Session{}, replyFail(http.StatusBadRequest, msg)
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
		picked = repopick.Auto(s.rankRepos(ctx, prompt, refs))
		if room := 10 - len(refs); len(picked) > room {
			picked = picked[:room]
		}
		for _, c := range picked {
			refs = append(refs, refOf(c))
		}
	}
	repos, err := s.resolveRepos(refs)
	if err != nil {
		return store.Session{}, replyFail(http.StatusBadRequest, err.Error())
	}
	mode, ok := egress.ParseMode(x.Network)
	if !ok {
		mode = s.networkMode()
	}
	// A chat that follows a parent hears what the parent has done since it last looked.
	preamble := replyPreamble(thread, s.lastAnswer)
	rootSession := thread[0]
	upSeen := rootSession.UpstreamSeen
	if rootSession.UpstreamID != "" {
		if text, n := s.upstreamUpdate(ctx, rootSession, false); text != "" {
			preamble = text + "\n" + preamble
			upSeen = n
		}
	}
	started, err := s.sessions.Start(sessions.Request{
		Task: x.Task, Agent: agentID, Prompt: prompt, Preamble: preamble, Branch: replyBranch(x),
		Images: images,
		Mode: agent.HandsOff, Network: mode, Memory: x.Memory, Repos: repos, Parent: x.ID, Notes: pickNotes(picked),
	})
	if err != nil {
		return store.Session{}, replyFail(http.StatusBadRequest, err.Error())
	}
	if rootSession.UpstreamID != "" && upSeen != rootSession.UpstreamSeen {
		_ = s.store.SetUpstreamSeen(rootSession.ID, upSeen)
	}
	if len(images) > 0 {
		b, _ := json.Marshal(images)
		_ = s.store.AddEvent(started.ID, "userimages", string(b))
	}
	return started, nil
}

func (s *Server) replyToSession(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
		Agent  string `json:"agent"` // empty: the same agent
		Images []agent.Image `json:"images"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateImages(req.Images); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	started, err := s.startReply(r.Context(), x, req.Prompt, req.Agent, req.Images)
	if err != nil {
		var re replyError
		if errors.As(err, &re) {
			writeErr(w, re.code, re.msg)
		} else {
			serverError(w)
		}
		return
	}
	writeJSON(w, http.StatusCreated, toView(started))
}

func validateImages(images []agent.Image) error {
	if len(images) > 4 {
		return errors.New("attach up to 4 images")
	}
	total := 0
	for _, img := range images {
		if img.MIME != "image/png" && img.MIME != "image/jpeg" && img.MIME != "image/gif" && img.MIME != "image/webp" {
			return errors.New("images must be PNG, JPEG, GIF, or WebP")
		}
		if img.Name == "" || strings.ContainsAny(img.Name, "\\/\r\n") {
			return errors.New("invalid image name")
		}
		b, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			return errors.New("invalid image data")
		}
		if http.DetectContentType(b) != img.MIME {
			return errors.New("image content does not match its type")
		}
		total += len(b)
	}
	if total > 5<<20 {
		return errors.New("images exceed 5 MB total")
	}
	return nil
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

// conversationDigest boils a conversation down for an agent that was not in it: the
// older turns clipped newest first within a budget (a line says when some are left
// out), then the latest turn nearly in full. asked and answered introduce the latest
// turn. thread is every turn so far, oldest first.
func conversationDigest(thread []store.Session, answer func(id string) string, asked, answered string) string {
	if len(thread) == 0 {
		return ""
	}
	last := thread[len(thread)-1]
	var b string
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
	b += asked + "\n<earlier-request>\n" + clip(last.Prompt, lastRequestChars) + "\n</earlier-request>\n"
	if a := strings.TrimSpace(answer(last.ID)); a != "" {
		b += answered + "\n<earlier-answer>\n" + clip(a, lastAnswerChars) + "\n</earlier-answer>\n"
	}
	return b
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
	return "This continues earlier work in the same conversation.\n" +
		conversationDigest(thread, answer, "In the last turn you were asked:", "and the answer was:") +
		"The work so far is on branch " + last.Branch + " in the repos that changed " +
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

// replyBranch is the branch a reply continues on: the conversation's own, whichever agent
// answers. A conversation is one branch and so one pull request: a later turn (even by a
// different agent) adds commits to it, which updates the pull request, instead of starting
// a second branch and a second pull request that repeats the first one's changes.
func replyBranch(prev store.Session) string { return prev.Branch }

// steerSession gives a running agent a message (see sessions.Manager.Steer).
func (s *Server) steerSession(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	var req struct {
		Text string `json:"text"`
		Images []agent.Image `json:"images"`
		Now  bool   `json:"now"` // interrupt and carry on from the message
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil { writeErr(w, http.StatusBadRequest, "invalid request body"); return }
	req.Text = strings.TrimSpace(req.Text)
	if len(req.Images) > 0 && x.Agent != "claude" { writeErr(w, http.StatusBadRequest, "image steering is currently supported for Claude sessions only"); return }
	if (req.Text == "" && len(req.Images) == 0) || len(req.Text) > 20000 || len(req.Images) > 4 {
		writeErr(w, http.StatusBadRequest, "write a message and attach up to 4 images")
		return
	}
	total := 0
	for _, img := range req.Images {
		if img.MIME != "image/png" && img.MIME != "image/jpeg" && img.MIME != "image/gif" && img.MIME != "image/webp" { writeErr(w, http.StatusBadRequest, "images must be PNG, JPEG, GIF, or WebP"); return }
		if img.Name == "" || strings.ContainsAny(img.Name, "\\/\r\n") { writeErr(w, http.StatusBadRequest, "invalid image name"); return }
		b, e := base64.StdEncoding.DecodeString(img.Data); if e != nil { writeErr(w, http.StatusBadRequest, "invalid image data"); return }; if http.DetectContentType(b) != img.MIME { writeErr(w, http.StatusBadRequest, "image content does not match its type"); return }; total += len(b)
	}
	if total > 5<<20 { writeErr(w, http.StatusRequestEntityTooLarge, "images exceed 5 MB total"); return }
	id, err := s.sessions.Steer(x.ID, req.Text, req.Images, req.Now)
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

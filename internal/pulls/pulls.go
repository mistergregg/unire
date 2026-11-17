// Package pulls is how a conversation opens and follows pull requests: it finds
// the branches a conversation pushed, opens a pull request for one when the owner
// agrees, and reports each pull request's state, checks and comments from its git
// server. The same service answers the owner (the web UI) and the agent (the
// `unire pr-*` tools).
package pulls

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mistergregg/unire/internal/gitprovider"
	"github.com/mistergregg/unire/internal/sessions"
	"github.com/mistergregg/unire/internal/store"
)

// Service finds, opens and follows a conversation's pull requests.
type Service struct {
	Store    *store.Store
	Provider func(store.GitServer) (gitprovider.Provider, error)

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at time.Time
	v  View
}

const cacheFor = 15 * time.Second

// Proposal is a branch a conversation pushed that has no pull request yet.
type Proposal struct {
	ServerID string `json:"serverId"`
	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
	Base     string `json:"base"`
	Title    string `json:"title"`
	Body     string `json:"body"`
}

// View is a pull request with what its server says about it now.
type View struct {
	ServerID string                `json:"serverId"`
	Repo     string                `json:"repo"`
	Number   int                   `json:"number"`
	Title    string                `json:"title"`
	URL      string                `json:"url"`
	Branch   string                `json:"branch"`
	Base     string                `json:"base,omitempty"`
	State    string                `json:"state,omitempty"`
	Draft    bool                  `json:"draft,omitempty"`
	Checks   []gitprovider.Check   `json:"checks"`
	Comments []gitprovider.Comment `json:"comments"`
	Summary  string                `json:"summary"` // "success", "failure", "pending" or "none"
	Error    string                `json:"error,omitempty"`
}

const maxComments = 40

// pushed is a branch some turn of the conversation pushed.
type pushed struct{ serverID, repo, branch string }

// pushes lists every (repo, branch) the conversation pushed, oldest first, and
// the last turn that pushed each.
func (s *Service) pushes(thread []store.Session) ([]pushed, map[pushed]store.Session) {
	var order []pushed
	last := map[pushed]store.Session{}
	for _, t := range thread {
		events, err := s.Store.Events(t.ID, 0, 5000)
		if err != nil {
			continue
		}
		names := make([]string, len(t.Repos))
		for i, r := range t.Repos {
			names[i] = r.FullName
		}
		dirs := sessions.RepoDirNames(names)
		for _, e := range events {
			if e.Kind != "log" {
				continue
			}
			for i, d := range dirs {
				if strings.HasPrefix(e.Text, d+": pushed ") {
					p := pushed{t.Repos[i].ServerID, t.Repos[i].FullName, t.Branch}
					if _, seen := last[p]; !seen {
						order = append(order, p)
					}
					last[p] = t
				}
			}
		}
	}
	return order, last
}

// Proposals lists the branches of a conversation that could become a pull request.
func (s *Service) Proposals(thread []store.Session) ([]Proposal, error) {
	if len(thread) == 0 {
		return nil, nil
	}
	have, err := s.Store.PullRequests(thread[0].ID)
	if err != nil {
		return nil, err
	}
	opened := map[pushed]bool{}
	for _, p := range have {
		opened[pushed{p.ServerID, p.Repo, p.Branch}] = true
	}
	title := suggestedTitle(s.Store, thread)
	order, last := s.pushes(thread)
	var out []Proposal
	for _, p := range order {
		if opened[p] {
			continue
		}
		out = append(out, Proposal{ServerID: p.serverID, Repo: p.repo, Branch: p.branch, Base: baseOf(thread[0], p), Title: title, Body: bodyOf(s.Store, last[p], thread[0].Task)})
	}
	return out, nil
}

// baseOf is the branch the conversation started from in a repo: what the owner
// chose for its first turn (empty means the repo's default branch).
func baseOf(first store.Session, p pushed) string {
	for _, r := range first.Repos {
		if r.ServerID == p.serverID && r.FullName == p.repo {
			return r.Base
		}
	}
	return ""
}

// suggestedTitle is the title the agent suggested with `unire pr`, else the
// conversation's title.
func suggestedTitle(st *store.Store, thread []store.Session) string {
	title := thread[0].Task
	for _, t := range thread {
		events, err := st.Events(t.ID, 0, 5000)
		if err != nil {
			continue
		}
		for _, e := range events {
			if e.Kind == "prtitle" && strings.TrimSpace(e.Text) != "" {
				title = strings.TrimSpace(e.Text)
			}
		}
	}
	return title
}

// bodyOf describes the change from the agent's final answer, which says what it
// did. It is shown to the owner before anything is opened.
func bodyOf(st *store.Store, t store.Session, task string) string {
	var b strings.Builder
	events, err := st.Events(t.ID, 0, 5000)
	if err == nil {
		for _, e := range events {
			if e.Kind == "out" {
				b.WriteString(e.Text)
			}
		}
	}
	text := strings.TrimSpace(b.String())
	if r := []rune(text); len(r) > 3000 {
		text = strings.TrimSpace(string(r[len(r)-3000:])) // the end of the answer is its summary
	}
	out := "Opened from a Unire conversation: " + task
	if text != "" {
		out += "\n\n" + text
	}
	return out
}

// Open opens a pull request for one of the conversation's proposals. The branch
// must be one the conversation pushed, so a request cannot name anything else.
func (s *Service) Open(ctx context.Context, thread []store.Session, want Proposal) (View, error) {
	props, err := s.Proposals(thread)
	if err != nil {
		return View{}, err
	}
	var p *Proposal
	for i := range props {
		if props[i].ServerID == want.ServerID && props[i].Repo == want.Repo && props[i].Branch == want.Branch {
			p = &props[i]
		}
	}
	if p == nil {
		return View{}, errors.New("that branch was not pushed by this conversation, or it already has a pull request")
	}
	title, body := strings.TrimSpace(want.Title), want.Body
	if title == "" {
		title = p.Title
	}
	if body == "" {
		body = p.Body
	}
	prs, _, err := s.prsFor(p.ServerID)
	if err != nil {
		return View{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pr, err := prs.CreatePR(ctx, p.Repo, p.Branch, p.Base, title, body)
	if err != nil {
		return View{}, err
	}
	rec := store.PullRequest{RootID: thread[0].ID, ServerID: p.ServerID, Repo: p.Repo, Number: pr.Number, Branch: p.Branch, Title: title, URL: pr.URL}
	if err := s.Store.AddPullRequest(rec); err != nil {
		return View{}, err
	}
	return s.view(ctx, rec), nil
}

func (s *Service) prsFor(serverID string) (gitprovider.PullRequests, store.GitServer, error) {
	g, ok, err := s.Store.GitServer(serverID)
	if err != nil {
		return nil, g, err
	}
	if !ok {
		return nil, g, errors.New("the git server of this repo was removed")
	}
	p, err := s.Provider(g)
	if err != nil {
		return nil, g, err
	}
	prs, err := gitprovider.AsPullRequests(p)
	return prs, g, err
}

// List returns the conversation's pull requests with what their servers say now.
func (s *Service) List(ctx context.Context, rootID string) ([]View, error) {
	recs, err := s.Store.PullRequests(rootID)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(recs))
	var wg sync.WaitGroup
	for i, rec := range recs {
		wg.Add(1)
		go func(i int, rec store.PullRequest) {
			defer wg.Done()
			out[i] = s.view(ctx, rec)
		}(i, rec)
	}
	wg.Wait()
	return out, nil
}

// view asks the server about one pull request. A server that cannot be reached
// still gives the record, with the reason.
func (s *Service) view(ctx context.Context, rec store.PullRequest) View {
	key := fmt.Sprintf("%s/%s/%d", rec.ServerID, rec.Repo, rec.Number)
	s.mu.Lock()
	if c, ok := s.cache[key]; ok && time.Since(c.at) < cacheFor {
		s.mu.Unlock()
		return c.v
	}
	s.mu.Unlock()

	v := View{ServerID: rec.ServerID, Repo: rec.Repo, Number: rec.Number, Title: rec.Title, URL: rec.URL, Branch: rec.Branch, Checks: []gitprovider.Check{}, Comments: []gitprovider.Comment{}, Summary: "none"}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prs, _, err := s.prsFor(rec.ServerID)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	pr, err := prs.GetPR(ctx, rec.Repo, rec.Number)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.State, v.Base, v.Draft = pr.State, pr.Base, pr.Draft
	if pr.Title != "" {
		v.Title = pr.Title
	}
	if pr.URL != "" {
		v.URL = pr.URL
	}
	if checks, err := prs.Checks(ctx, rec.Repo, pr); err == nil && checks != nil {
		v.Checks = checks
	}
	v.Summary = summarize(v.Checks)
	if comments, err := prs.Comments(ctx, rec.Repo, rec.Number); err == nil && comments != nil {
		if len(comments) > maxComments {
			comments = comments[len(comments)-maxComments:]
		}
		v.Comments = comments
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]cached{}
	}
	s.cache[key] = cached{time.Now(), v}
	s.mu.Unlock()
	return v
}

// summarize is "failure" if any check failed, else "pending" if any is still
// running, else "success", or "none" when there are no checks.
func summarize(checks []gitprovider.Check) string {
	if len(checks) == 0 {
		return "none"
	}
	state := gitprovider.CheckSuccess
	for _, c := range checks {
		switch c.State {
		case gitprovider.CheckFailure:
			return gitprovider.CheckFailure
		case gitprovider.CheckPending:
			state = gitprovider.CheckPending
		}
	}
	return state
}

// forget drops what was remembered about a pull request, so the next look asks
// the server again.
func (s *Service) forget(rec store.PullRequest) {
	s.mu.Lock()
	delete(s.cache, fmt.Sprintf("%s/%s/%d", rec.ServerID, rec.Repo, rec.Number))
	s.mu.Unlock()
}

// ---- the agent's tools ----

// Tools the agent can run once its conversation has an open pull request.
const (
	ToolStatus   = "pr-status"
	ToolComments = "pr-comments"
	ToolReply    = "pr-reply"
)

// HasOpen reports whether the conversation has a pull request that is still open.
func (s *Service) HasOpen(ctx context.Context, rootID string) bool {
	views, err := s.List(ctx, rootID)
	if err != nil {
		return false
	}
	for _, v := range views {
		if v.State == gitprovider.PROpen {
			return true
		}
	}
	return false
}

var replyTarget = regexp.MustCompile(`^(\S+)#(\d+):\s*`)

// Call answers one of the agent's tool requests for the conversation rootID. The
// answer is text the agent reads; a problem is told to the agent in words too.
func (s *Service) Call(ctx context.Context, rootID, tool, args string) string {
	recs, err := s.Store.PullRequests(rootID)
	if err != nil || len(recs) == 0 {
		return "This conversation has no pull request."
	}
	var open []store.PullRequest
	var views []View
	for _, rec := range recs {
		v := s.view(ctx, rec)
		views = append(views, v)
		if v.State == gitprovider.PROpen || v.State == "" {
			open = append(open, rec)
		}
	}
	switch tool {
	case ToolStatus:
		var b strings.Builder
		for _, v := range views {
			fmt.Fprintf(&b, "%s#%d (%s) %s: state %s, checks %s\n", v.Repo, v.Number, v.Branch, v.URL, orUnknown(v.State), v.Summary)
			if v.Error != "" {
				fmt.Fprintf(&b, "  could not read it: %s\n", v.Error)
			}
			for _, c := range v.Checks {
				fmt.Fprintf(&b, "  - %s: %s %s\n", c.Name, c.State, c.URL)
			}
		}
		return strings.TrimSpace(b.String())
	case ToolComments:
		var b strings.Builder
		for _, v := range views {
			fmt.Fprintf(&b, "%s#%d: %d comments\n", v.Repo, v.Number, len(v.Comments))
			for _, c := range v.Comments {
				where := ""
				if c.Path != "" {
					where = fmt.Sprintf(" on %s:%d", c.Path, c.Line)
				}
				fmt.Fprintf(&b, "- %s%s: %s\n", c.Author, where, strings.ReplaceAll(c.Body, "\n", "\n  "))
			}
		}
		return strings.TrimSpace(b.String())
	case ToolReply:
		text := strings.TrimSpace(args)
		target := open
		if m := replyTarget.FindStringSubmatch(text); m != nil {
			n, _ := strconv.Atoi(m[2])
			target = nil
			for _, rec := range recs {
				if rec.Repo == m[1] && rec.Number == n {
					target = []store.PullRequest{rec}
				}
			}
			text = strings.TrimSpace(text[len(m[0]):])
		}
		switch {
		case text == "":
			return `usage: unire pr-reply "<comment>"`
		case len(target) == 0:
			return "There is no open pull request to comment on."
		case len(target) > 1:
			return `There is more than one open pull request. Start the comment with the one it is for, like: unire pr-reply "owner/repo#12: <comment>"`
		}
		rec := target[0]
		prs, _, err := s.prsFor(rec.ServerID)
		if err != nil {
			return "Could not comment: " + err.Error()
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := prs.AddComment(cctx, rec.Repo, rec.Number, text+"\n\n_Posted by an agent through Unire._"); err != nil {
			return "Could not comment: " + err.Error()
		}
		s.forget(rec)
		return fmt.Sprintf("Commented on %s#%d.", rec.Repo, rec.Number)
	}
	return "Unknown tool."
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

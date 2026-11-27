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
	"github.com/mistergregg/unire/internal/safe"
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
	HeadSHA  string                `json:"headSha,omitempty"`
	Pending  bool                  `json:"pending,omitempty"`  // some check has not finished
	FixTries int                   `json:"fixTries,omitempty"` // fixes Unire started by itself for this pull request
}

const maxComments = 40

// pushed is a branch some turn of the conversation pushed, and the branch it was
// made to go into when the agent said (an extra branch made for another target);
// empty base means the conversation's own base for that repo.
type pushed struct{ serverID, repo, branch, base string }

// pushes lists every (repo, branch) the conversation pushed, oldest first, and
// the last turn that pushed each. A turn's own branch is logged as "<dir>: pushed
// <branch>"; extra branches it made for other targets as "<dir>: also pushed
// <branch> (into <base>)".
func (s *Service) pushes(thread []store.Session) ([]pushed, map[pushed]store.Session) {
	var order []pushed
	last := map[pushed]store.Session{}
	add := func(p pushed, t store.Session) {
		if _, seen := last[p]; !seen {
			order = append(order, p)
		}
		last[p] = t
	}
	for _, t := range thread {
		events, err := s.Store.PushEvents(t.ID)
		if err != nil {
			continue
		}
		names := make([]string, len(t.Repos))
		for i, r := range t.Repos {
			names[i] = r.FullName
		}
		dirs := sessions.RepoDirNames(names)
		for _, e := range events {
			for i, d := range dirs {
				ref := t.Repos[i]
				switch {
				case strings.HasPrefix(e.Text, d+": pushed "):
					add(pushed{ref.ServerID, ref.FullName, t.Branch, ""}, t)
				case strings.HasPrefix(e.Text, d+": also pushed "):
					rest := strings.TrimPrefix(e.Text, d+": also pushed ")
					branch, base := rest, ""
					if i := strings.Index(rest, " (into "); i >= 0 && strings.HasSuffix(rest, ")") {
						branch, base = rest[:i], strings.TrimSuffix(rest[i+len(" (into "):], ")")
					}
					add(pushed{ref.ServerID, ref.FullName, strings.TrimSpace(branch), base}, t)
				}
			}
		}
	}
	return order, last
}

// Proposals lists the pushed branches of a conversation that have no pull request
// yet (into the branch they are meant for).
func (s *Service) Proposals(thread []store.Session) ([]Proposal, error) {
	if len(thread) == 0 {
		return nil, nil
	}
	have, err := s.Store.PullRequests(thread[0].ID)
	if err != nil {
		return nil, err
	}

	order, last := s.pushes(thread)
	var out []Proposal
	for _, p := range order {
		base := p.base
		if base == "" {
			base = baseOf(thread[0], p)
		}
		if covered(have, p, base, last[p]) {
			continue
		}
		t := titleOf(s.Store, thread, last[p])
		if p.base != "" {
			t += " (into " + p.base + ")" // an extra branch is told apart from the main one
		}
		out = append(out, Proposal{ServerID: p.serverID, Repo: p.repo, Branch: p.branch, Base: base, Title: t, Body: bodyOf(s.Store, thread, last[p], thread[0].Task)})
	}
	return out, nil
}

// covered reports whether a pull request already exists for a pushed branch into
// base. One that was merged only covers the pushes made before it was opened: a
// later turn pushes the same branch again, and that new work needs a new pull request.
func covered(have []store.PullRequest, p pushed, base string, turn store.Session) bool {
	for _, r := range have {
		if r.ServerID != p.serverID || r.Repo != p.repo || r.Branch != p.branch || r.Base != base {
			continue
		}
		if r.State == gitprovider.PRMerged && turn.CreatedAt.After(r.CreatedAt) {
			continue
		}
		return true
	}
	return false
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

// since is the turns from t (the one that pushed a branch) to the end of the
// conversation. A later turn that pushes nothing, such as a final review, may still
// write the pull request's title and description (pr, pr-body): that text is about
// the pushed work, so it counts. Text from turns before t describes earlier work.
func since(thread []store.Session, t store.Session) []store.Session {
	for i, x := range thread {
		if x.ID == t.ID {
			return thread[i:]
		}
	}
	return []store.Session{t}
}

// titleOf is the title offered for a pull request: the newest one the agent suggested
// (pr tool) in the turn that pushed the branch or a later one, else the conversation's
// title.
func titleOf(st *store.Store, thread []store.Session, t store.Session) string {
	title := thread[0].Task
	for _, x := range since(thread, t) {
		if w, _ := written(st, x.ID); w != "" {
			title = w
		}
	}
	return title
}

// bodyOf is the description offered for a pull request: the newest one the agent
// wrote (pr-body) in the turn that pushed the branch or a later one, else the end of
// the pushing turn's final answer, which usually says what it did. It is never taken
// from an earlier turn: a later turn's work would be described by the first turn's
// text. The owner sees and can edit it before anything is opened.
func bodyOf(st *store.Store, thread []store.Session, t store.Session, task string) string {
	footer := "\n\n---\n_Opened from a Unire conversation: " + task + "_"
	var wrote string
	for _, x := range since(thread, t) {
		if _, w := written(st, x.ID); w != "" {
			wrote = w
		}
	}
	if wrote != "" {
		return wrote + footer
	}
	events, err := st.EventsByKind(t.ID, "out")
	if err != nil {
		return strings.TrimSpace(footer[5:])
	}
	var answer strings.Builder
	for _, e := range events {
		if e.Kind == "out" {
			if answer.Len() > 0 {
				answer.WriteString("\n")
			}
			answer.WriteString(e.Text)
		}
	}
	text := strings.TrimSpace(answer.String())
	if r := []rune(text); len(r) > 3000 {
		text = strings.TrimSpace(string(r[len(r)-3000:])) // the end of the answer is its summary
	}
	text = cleanFallback(text)
	if text == "" {
		return strings.TrimSpace(footer[5:])
	}
	return text + footer
}

// cleanFallback removes conversational chat preamble addressed to the owner
// (such as "I can't open the pull request myself, so Unire will offer it to you...")
// when using the agent's chat answer as a fallback pull request description.
func cleanFallback(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	paragraphs := strings.Split(text, "\n\n")
	for len(paragraphs) > 0 {
		p := strings.TrimSpace(paragraphs[0])
		if isChatPreamble(strings.ToLower(p)) {
			paragraphs = paragraphs[1:]
			continue
		}
		break
	}
	return strings.TrimSpace(strings.Join(paragraphs, "\n\n"))
}

func isChatPreamble(lower string) bool {
	if strings.Contains(lower, "can't open the pull request") || strings.Contains(lower, "cannot open the pull request") {
		return true
	}
	if strings.Contains(lower, "unire will offer") || strings.Contains(lower, "unire asks") || strings.Contains(lower, "unire pushes") {
		return true
	}
	if (strings.Contains(lower, "committed on its own branch") || strings.Contains(lower, "committed on a separate branch") || strings.Contains(lower, "committed on branch") || strings.Contains(lower, "pushed to branch")) &&
		(strings.Contains(lower, "pull request") || strings.Contains(lower, "pr")) {
		return true
	}
	return false
}

// Open opens a pull request for one of the conversation's proposals. The branch
// must be one the conversation pushed, so a request cannot name anything else.
func (s *Service) Open(ctx context.Context, thread []store.Session, want Proposal) (View, error) {
	if len(thread) == 0 {
		return View{}, errors.New("no such conversation")
	}
	order, last := s.pushes(thread)
	var found *pushed
	for i := range order {
		if order[i].serverID == want.ServerID && order[i].repo == want.Repo && order[i].branch == want.Branch {
			found = &order[i]
			break
		}
	}
	if found == nil {
		return View{}, errors.New("that branch was not pushed by this conversation")
	}
	// The branch to go into: the one the owner typed, else the one the branch was
	// made for (an extra branch), else where the conversation started.
	base := strings.TrimSpace(want.Base)
	if base == "" {
		base = found.base
	}
	if base == "" {
		base = baseOf(thread[0], *found)
	}
	have, err := s.Store.PullRequests(thread[0].ID)
	if err != nil {
		return View{}, err
	}
	if covered(have, *found, base, last[*found]) {
		return View{}, errors.New("this branch already has a pull request into that branch")
	}
	title, body := strings.TrimSpace(want.Title), want.Body
	if title == "" {
		title = titleOf(s.Store, thread, last[*found])
		if found.base != "" {
			title += " (into " + found.base + ")"
		}
	}
	if body == "" {
		body = bodyOf(s.Store, thread, last[*found], thread[0].Task)
	}
	prs, _, err := s.prsFor(found.serverID)
	if err != nil {
		return View{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pr, err := prs.CreatePR(ctx, found.repo, found.branch, base, title, body)
	if err != nil {
		return View{}, err
	}
	rec := store.PullRequest{RootID: thread[0].ID, ServerID: found.serverID, Repo: found.repo, Number: pr.Number, Branch: found.branch, Base: base, Title: title, URL: pr.URL}
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
			defer safe.Recover("pull request status", nil)
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

	v := View{ServerID: rec.ServerID, Repo: rec.Repo, Number: rec.Number, Title: rec.Title, URL: rec.URL, Branch: rec.Branch, State: rec.State, Checks: []gitprovider.Check{}, Comments: []gitprovider.Comment{}, Summary: "none"}
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
	v.State, v.Base, v.Draft, v.HeadSHA, v.FixTries = pr.State, pr.Base, pr.Draft, pr.HeadSHA, rec.FixAttempts
	_ = s.Store.SetPullRequestState(rec.ServerID, rec.Repo, rec.Number, pr.State)
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
	for _, c := range v.Checks {
		v.Pending = v.Pending || c.State == gitprovider.CheckPending
	}
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
	ToolClose    = "pr-close"
	ToolLogs     = "pr-logs"
	ToolReopen   = "pr-reopen"
)

// HasAny reports whether the conversation has opened a pull request (open or
// closed: an agent that closed one may be asked to reopen it). It asks no server.
func (s *Service) HasAny(ctx context.Context, rootID string) bool {
	recs, err := s.Store.PullRequests(rootID)
	return err == nil && len(recs) > 0
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
	case ToolLogs:
		var parts []string
		for _, v := range views {
			if m := s.FailureMessage(ctx, v); m != "" {
				parts = append(parts, m)
			}
		}
		if len(parts) == 0 {
			return "No check is failing right now. Run unire pr-status to see where they stand."
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
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
	case ToolClose, ToolReopen:
		want := tool == ToolReopen // reopening acts on closed ones, closing on open ones
		var cands []store.PullRequest
		for i, rec := range recs {
			isOpen := views[i].State == gitprovider.PROpen || views[i].State == ""
			if isOpen != want {
				cands = append(cands, rec)
			}
		}
		text := strings.Trim(strings.TrimSpace(args), `"'`)
		if m := regexp.MustCompile(`^(\S+)#(\d+)$`).FindStringSubmatch(text); m != nil {
			n, _ := strconv.Atoi(m[2])
			var named []store.PullRequest
			for _, rec := range cands {
				if rec.Repo == m[1] && rec.Number == n {
					named = append(named, rec)
				}
			}
			cands = named
		}
		verb := "close"
		if want {
			verb = "reopen"
		}
		switch {
		case len(cands) == 0:
			return "There is no pull request to " + verb + "."
		case len(cands) > 1:
			return fmt.Sprintf("There is more than one pull request to %s. Name it, like: unire pr-%s \"owner/repo#12\"", verb, verb)
		}
		rec := cands[0]
		if _, err := s.SetOpen(ctx, rootID, rec.ServerID, rec.Repo, rec.Number, want); err != nil {
			return "Could not " + verb + " it: " + err.Error()
		}
		return fmt.Sprintf("Done: %s#%d is now %s. Nothing was merged or deleted.", rec.Repo, rec.Number, map[bool]string{true: "open", false: "closed"}[want])
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

// SetOpen closes one of the conversation's pull requests, or reopens it. Only a
// pull request this conversation opened can be changed. Nothing is merged.
func (s *Service) SetOpen(ctx context.Context, rootID, serverID, repo string, number int, open bool) (View, error) {
	recs, err := s.Store.PullRequests(rootID)
	if err != nil {
		return View{}, err
	}
	for _, rec := range recs {
		if rec.ServerID != serverID || rec.Repo != repo || rec.Number != number {
			continue
		}
		prs, _, err := s.prsFor(serverID)
		if err != nil {
			return View{}, err
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := prs.SetPROpen(cctx, repo, number, open); err != nil {
			return View{}, err
		}
		s.forget(rec)
		return s.view(ctx, rec), nil
	}
	return View{}, errors.New("this conversation did not open that pull request")
}

// FailureMessage tells an agent why a pull request's checks are failing: which ones, and
// the end of the log of the first two (the error lines, and what the log ended with). It
// is what the owner's "fix the checks" button and the automatic fix send, and what the
// pr-logs tool prints. Without logs (a server that gives none) it names the checks and
// their links.
func (s *Service) FailureMessage(ctx context.Context, v View) string {
	var failed []gitprovider.Check
	for _, c := range v.Checks {
		if c.State == gitprovider.CheckFailure {
			failed = append(failed, c)
		}
	}
	if len(failed) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The checks on the pull request for %s (#%d, branch %s) are failing:\n", v.Repo, v.Number, v.Branch)
	for _, c := range failed {
		fmt.Fprintf(&b, "- %s", c.Name)
		if c.URL != "" {
			fmt.Fprintf(&b, " (%s)", c.URL)
		}
		b.WriteString("\n")
	}
	prs, _, err := s.prsFor(v.ServerID)
	lp, canLog := prs.(gitprovider.LogProvider)
	if err != nil || !canLog {
		return b.String()
	}
	for i, c := range failed {
		if i >= 2 {
			b.WriteString("\n(" + fmt.Sprint(len(failed)-2) + " more failing checks are not shown; run `unire pr-status` for all of them.)\n")
			break
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		text, lerr := lp.FailureLog(cctx, v.Repo, c)
		cancel()
		switch {
		case lerr == nil:
			fmt.Fprintf(&b, "\nWhy %q failed:\n%s\n", c.Name, text)
		case errors.Is(lerr, gitprovider.ErrNoLogs):
			fmt.Fprintf(&b, "\n(This git server does not give job logs through its API, so open the link above to read why %q failed.)\n", c.Name)
		default:
			fmt.Fprintf(&b, "\n(Could not read the log of %q: %v)\n", c.Name, lerr)
		}
	}
	return b.String()
}

// ClearCache forgets what was remembered about pull requests, so the next look asks the
// git servers again (what a pull request looks like is otherwise reused for a few seconds).
func (s *Service) ClearCache() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

// Update is the new title and description a later turn would give an existing pull request.
type Update struct {
	ServerID string `json:"serverId"`
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Body     string `json:"body"`
}

// Updates lists the pull requests whose branch was pushed to again by a turn that
// finished after the pull request's text was last set, with the text that turn would
// give it. The branch itself needs nothing: more commits update a pull request by
// themselves; its title and description are what go stale.
func (s *Service) Updates(thread []store.Session) ([]Update, error) {
	if len(thread) == 0 {
		return nil, nil
	}
	recs, err := s.Store.PullRequests(thread[0].ID)
	if err != nil {
		return nil, err
	}
	_, last := s.pushes(thread)
	var out []Update
	for _, rec := range recs {
		var t *store.Session
		for p, turn := range last {
			if p.serverID == rec.ServerID && p.repo == rec.Repo && p.branch == rec.Branch {
				turn := turn
				if t == nil || turn.FinishedAt.After(t.FinishedAt) {
					t = &turn
				}
			}
		}
		if t == nil || !t.FinishedAt.After(rec.DescribedAt) {
			continue
		}
		out = append(out, Update{ServerID: rec.ServerID, Repo: rec.Repo, Number: rec.Number, Title: titleOf(s.Store, thread, *t), Body: bodyOf(s.Store, thread, *t, thread[0].Task)})
	}
	return out, nil
}

// written is the title and description the agent wrote in a turn (pr and pr-body), if any.
func written(st *store.Store, id string) (title, body string) {
	events, err := st.EventsByKind(id, "prtitle", "prbody")
	if err != nil {
		return "", ""
	}
	for _, e := range events {
		switch t := strings.TrimSpace(e.Text); {
		case e.Kind == "prtitle" && t != "":
			title = t
		case e.Kind == "prbody" && t != "":
			body = t
		}
	}
	return title, body
}

// SyncText puts the text the agent wrote on the pull requests its turn pushed to:
// when a later turn changes what a pull request does, the agent writes a new
// description (pr-body, and a title with pr) and it replaces the old one. A turn that
// wrote no description, such as a fix for a failing check, leaves the pull request's
// text alone; the owner can still update it from the chat. It returns a line per
// pull request it changed or could not change, for the turn's log.
func (s *Service) SyncText(ctx context.Context, rootID string) []string {
	thread, err := s.Store.Thread(rootID)
	if err != nil || len(thread) == 0 {
		return nil
	}
	recs, err := s.Store.PullRequests(rootID)
	if err != nil {
		return nil
	}
	_, last := s.pushes(thread)
	var log []string
	for _, rec := range recs {
		var t *store.Session
		for p, turn := range last {
			if p.serverID == rec.ServerID && p.repo == rec.Repo && p.branch == rec.Branch {
				turn := turn
				if t == nil || turn.FinishedAt.After(t.FinishedAt) {
					t = &turn
				}
			}
		}
		if t == nil {
			continue
		}
		// This runs as a turn's work is pushed, before the turn is marked finished: a
		// turn with no finish time yet is the one ending now. (Seen on a real chat: a
		// reply's new description never reached its pull request.)
		ended := t.FinishedAt
		if ended.IsZero() {
			ended = time.Now()
		}
		if !ended.After(rec.DescribedAt) {
			continue
		}
		title, body := written(s.Store, t.ID)
		if body == "" {
			continue
		}
		if title == "" {
			title = rec.Title
		}
		body += "\n\n---\n_Opened from a Unire conversation: " + thread[0].Task + "_"
		if _, err := s.SetText(ctx, rootID, rec.ServerID, rec.Repo, rec.Number, title, body); err != nil {
			log = append(log, fmt.Sprintf("could not update the text of pull request #%d in %s: %v", rec.Number, rec.Repo, err))
			continue
		}
		log = append(log, fmt.Sprintf("updated the title and description of pull request #%d in %s", rec.Number, rec.Repo))
	}
	return log
}

// SetText changes the title and description of one of the conversation's pull requests.
func (s *Service) SetText(ctx context.Context, rootID, serverID, repo string, number int, title, body string) (View, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return View{}, errors.New("a pull request needs a title")
	}
	recs, err := s.Store.PullRequests(rootID)
	if err != nil {
		return View{}, err
	}
	for _, rec := range recs {
		if rec.ServerID != serverID || rec.Repo != repo || rec.Number != number {
			continue
		}
		prs, _, err := s.prsFor(serverID)
		if err != nil {
			return View{}, err
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := prs.UpdatePR(cctx, repo, number, title, body); err != nil {
			return View{}, err
		}
		if err := s.Store.SetPullRequestDescribed(serverID, repo, number, title); err != nil {
			return View{}, err
		}
		rec.Title = title
		s.forget(rec)
		return s.view(ctx, rec), nil
	}
	return View{}, errors.New("this conversation did not open that pull request")
}

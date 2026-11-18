package gitprovider

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Pull request states, the same for every server.
const (
	PROpen   = "open"
	PRMerged = "merged"
	PRClosed = "closed"
)

// Check states, the same for every server.
const (
	CheckPending = "pending"
	CheckSuccess = "success"
	CheckFailure = "failure"
)

// PullRequest is a pull request (a merge request on GitLab).
type PullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	State   string `json:"state"` // PROpen, PRMerged or PRClosed
	Branch  string `json:"branch"`
	Base    string `json:"base"`
	Draft   bool   `json:"draft"`
	HeadSHA string `json:"-"`
	// pipeline is GitLab's id for the pipeline whose jobs are the checks.
	pipeline int
}

// Check is one automated check (a build, a test run) on a pull request.
type Check struct {
	Name  string `json:"name"`
	State string `json:"state"` // CheckPending, CheckSuccess or CheckFailure
	URL   string `json:"url"`
}

// Comment is something said on a pull request: a conversation comment, a
// review, or a comment on a line of code.
type Comment struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Body   string `json:"body"`
	Path   string `json:"path,omitempty"` // the file, for a comment on code
	Line   int    `json:"line,omitempty"`
	URL    string `json:"url"`
	At     string `json:"at"` // as the server reports it
}

// PullRequests is what a provider can do with pull requests. Every provider
// implements it, but it is a separate interface so Provider stays small.
type PullRequests interface {
	// CreatePR opens a pull request from head into base (empty base: the repo's
	// default branch).
	CreatePR(ctx context.Context, repo, head, base, title, body string) (PullRequest, error)
	GetPR(ctx context.Context, repo string, number int) (PullRequest, error)
	Checks(ctx context.Context, repo string, pr PullRequest) ([]Check, error)
	Comments(ctx context.Context, repo string, number int) ([]Comment, error)
	// AddComment writes a conversation comment on the pull request.
	AddComment(ctx context.Context, repo string, number int, body string) error
	// SetPROpen closes a pull request (open false) or reopens it. Nothing is merged
	// or deleted.
	SetPROpen(ctx context.Context, repo string, number int, open bool) error
}

// ErrNoPullRequests is returned by AsPullRequests for a provider without them.
var ErrNoPullRequests = fmt.Errorf("this git server type cannot open pull requests")

// AsPullRequests returns p's pull request support.
func AsPullRequests(p Provider) (PullRequests, error) {
	if pr, ok := p.(PullRequests); ok {
		return pr, nil
	}
	return nil, ErrNoPullRequests
}

// ---- GitHub ----

func (g github) defaultBranch(ctx context.Context, repo string) (string, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.c.getJSON(ctx, "/repos/"+repo, &r); err != nil {
		return "", err
	}
	return r.DefaultBranch, nil
}

type ghPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Draft   bool   `json:"draft"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p ghPR) pr() PullRequest {
	state := PROpen
	if p.Merged {
		state = PRMerged
	} else if p.State == "closed" {
		state = PRClosed
	}
	return PullRequest{Number: p.Number, Title: p.Title, URL: p.HTMLURL, State: state, Branch: p.Head.Ref, Base: p.Base.Ref, Draft: p.Draft, HeadSHA: p.Head.SHA}
}

func (g github) CreatePR(ctx context.Context, repo, head, base, title, body string) (PullRequest, error) {
	if base == "" {
		var err error
		if base, err = g.defaultBranch(ctx, repo); err != nil {
			return PullRequest{}, err
		}
	}
	var p ghPR
	err := g.c.doJSON(ctx, "POST", "/repos/"+repo+"/pulls", map[string]any{"title": title, "head": head, "base": base, "body": body}, &p)
	return p.pr(), err
}

func (g github) GetPR(ctx context.Context, repo string, number int) (PullRequest, error) {
	var p ghPR
	err := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), &p)
	return p.pr(), err
}

func (g github) Checks(ctx context.Context, repo string, pr PullRequest) ([]Check, error) {
	var out []Check
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	runsErr := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repo, pr.HeadSHA), &runs)
	if runsErr == nil {
		for _, r := range runs.CheckRuns {
			out = append(out, Check{Name: r.Name, State: runState(r.Status, r.Conclusion), URL: r.HTMLURL})
		}
	} else if wf, err := g.workflowRunChecks(ctx, repo, pr.HeadSHA); err == nil {
		// A fine-grained token has no Checks permission, so the check runs are refused;
		// the workflow runs (Actions, read-only) say the same for GitHub Actions.
		out = append(out, wf...)
		runsErr = nil
	}
	// Older integrations report commit statuses rather than check runs.
	var st struct {
		Statuses []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	statusErr := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/commits/%s/status", repo, pr.HeadSHA), &st)
	if statusErr == nil {
		for _, s := range st.Statuses {
			out = append(out, Check{Name: s.Context, State: statusState(s.State), URL: s.TargetURL})
		}
	}
	if runsErr != nil && statusErr != nil {
		return nil, runsErr
	}
	return out, nil
}

// workflowRunChecks reads a commit's GitHub Actions workflow runs as checks.
func (g github) workflowRunChecks(ctx context.Context, repo, sha string) ([]Check, error) {
	var res struct {
		Runs []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/actions/runs?head_sha=%s&per_page=30", repo, sha), &res); err != nil {
		return nil, err
	}
	var out []Check
	for _, r := range res.Runs {
		out = append(out, Check{Name: r.Name, State: runState(r.Status, r.Conclusion), URL: r.HTMLURL})
	}
	return out, nil
}

// runState maps a check run's (or workflow run's) status and conclusion to a check state.
func runState(status, conclusion string) string {
	if status != "completed" {
		return CheckPending
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		return CheckSuccess
	}
	return CheckFailure
}

// statusState maps a commit status ("success", "pending", "failure", "error")
// to a check state.
func statusState(s string) string {
	switch s {
	case "success":
		return CheckSuccess
	case "failure", "error":
		return CheckFailure
	}
	return CheckPending
}

type ghComment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	HTMLURL string `json:"html_url"`
	At      string `json:"created_at"`
	Submit  string `json:"submitted_at"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (c ghComment) comment() Comment {
	at := c.At
	if at == "" {
		at = c.Submit
	}
	return Comment{ID: fmt.Sprint(c.ID), Author: c.User.Login, Body: strings.TrimSpace(c.Body), Path: c.Path, Line: c.Line, URL: c.HTMLURL, At: at}
}

func (g github) Comments(ctx context.Context, repo string, number int) ([]Comment, error) {
	var out []Comment
	for _, path := range []string{"/issues/%d/comments", "/pulls/%d/reviews", "/pulls/%d/comments"} {
		var list []ghComment
		if err := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s"+path+"?per_page=100", repo, number), &list); err != nil {
			return nil, err
		}
		for _, c := range list {
			if cm := c.comment(); cm.Body != "" {
				out = append(out, cm)
			}
		}
	}
	return sortByTime(out), nil
}

func (g github) AddComment(ctx context.Context, repo string, number int, body string) error {
	return g.c.doJSON(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), map[string]string{"body": body}, nil)
}

// ---- Gitea and Forgejo ----

func (g gitea) CreatePR(ctx context.Context, repo, head, base, title, body string) (PullRequest, error) {
	if base == "" {
		var r struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.c.getJSON(ctx, "/repos/"+repo, &r); err != nil {
			return PullRequest{}, err
		}
		base = r.DefaultBranch
	}
	var p ghPR // Gitea's pull request has the same fields
	err := g.c.doJSON(ctx, "POST", "/repos/"+repo+"/pulls", map[string]any{"title": title, "head": head, "base": base, "body": body}, &p)
	return p.pr(), err
}

func (g gitea) GetPR(ctx context.Context, repo string, number int) (PullRequest, error) {
	var p ghPR
	err := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), &p)
	return p.pr(), err
}

func (g gitea) Checks(ctx context.Context, repo string, pr PullRequest) ([]Check, error) {
	var st struct {
		Statuses []struct {
			Context   string `json:"context"`
			Status    string `json:"status"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/commits/%s/status", repo, pr.HeadSHA), &st); err != nil {
		return nil, err
	}
	var out []Check
	for _, s := range st.Statuses {
		out = append(out, Check{Name: s.Context, State: statusState(s.Status), URL: s.TargetURL})
	}
	return out, nil
}

func (g gitea) Comments(ctx context.Context, repo string, number int) ([]Comment, error) {
	var out []Comment
	var issue []ghComment
	if err := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), &issue); err != nil {
		return nil, err
	}
	for _, c := range issue {
		if cm := c.comment(); cm.Body != "" {
			out = append(out, cm)
		}
	}
	var reviews []ghComment
	if err := g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, number), &reviews); err == nil {
		for _, r := range reviews {
			if cm := r.comment(); cm.Body != "" {
				out = append(out, cm)
			}
			var inline []struct {
				ghComment
				Position int `json:"position"`
			}
			if g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/pulls/%d/reviews/%d/comments", repo, number, r.ID), &inline) == nil {
				for _, c := range inline {
					cm := c.comment()
					if cm.Line == 0 {
						cm.Line = c.Position
					}
					if cm.Body != "" {
						out = append(out, cm)
					}
				}
			}
		}
	}
	return sortByTime(out), nil
}

func (g gitea) AddComment(ctx context.Context, repo string, number int, body string) error {
	return g.c.doJSON(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), map[string]string{"body": body}, nil)
}

// ---- GitLab ----

func (g gitlab) project(repo string) string { return "/projects/" + url.PathEscape(repo) }

type glMR struct {
	IID          int    `json:"iid"`
	Title        string `json:"title"`
	WebURL       string `json:"web_url"`
	State        string `json:"state"`
	Draft        bool   `json:"draft"`
	SHA          string `json:"sha"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	HeadPipeline *struct {
		ID int `json:"id"`
	} `json:"head_pipeline"`
}

func (m glMR) pr() PullRequest {
	state := PROpen
	switch m.State {
	case "merged":
		state = PRMerged
	case "closed", "locked":
		state = PRClosed
	}
	p := PullRequest{Number: m.IID, Title: m.Title, URL: m.WebURL, State: state, Branch: m.SourceBranch, Base: m.TargetBranch, Draft: m.Draft, HeadSHA: m.SHA}
	if m.HeadPipeline != nil {
		p.pipeline = m.HeadPipeline.ID
	}
	return p
}

func (g gitlab) CreatePR(ctx context.Context, repo, head, base, title, body string) (PullRequest, error) {
	if base == "" {
		var r struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.c.getJSON(ctx, g.project(repo), &r); err != nil {
			return PullRequest{}, err
		}
		base = r.DefaultBranch
	}
	var m glMR
	err := g.c.doJSON(ctx, "POST", g.project(repo)+"/merge_requests", map[string]any{"source_branch": head, "target_branch": base, "title": title, "description": body}, &m)
	return m.pr(), err
}

func (g gitlab) GetPR(ctx context.Context, repo string, number int) (PullRequest, error) {
	var m glMR
	err := g.c.getJSON(ctx, fmt.Sprintf("%s/merge_requests/%d", g.project(repo), number), &m)
	return m.pr(), err
}

func (g gitlab) Checks(ctx context.Context, repo string, pr PullRequest) ([]Check, error) {
	if pr.pipeline == 0 {
		return nil, nil
	}
	var jobs []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		WebURL string `json:"web_url"`
	}
	if err := g.c.getJSON(ctx, fmt.Sprintf("%s/pipelines/%d/jobs?per_page=100", g.project(repo), pr.pipeline), &jobs); err != nil {
		return nil, err
	}
	var out []Check
	for _, j := range jobs {
		state := CheckPending
		switch j.Status {
		case "success", "skipped":
			state = CheckSuccess
		case "failed", "canceled":
			state = CheckFailure
		}
		out = append(out, Check{Name: j.Name, State: state, URL: j.WebURL})
	}
	return out, nil
}

func (g gitlab) Comments(ctx context.Context, repo string, number int) ([]Comment, error) {
	var notes []struct {
		ID     int64  `json:"id"`
		Body   string `json:"body"`
		System bool   `json:"system"`
		At     string `json:"created_at"`
		Author struct {
			Username string `json:"username"`
		} `json:"author"`
		Position *struct {
			NewPath string `json:"new_path"`
			NewLine int    `json:"new_line"`
		} `json:"position"`
	}
	if err := g.c.getJSON(ctx, fmt.Sprintf("%s/merge_requests/%d/notes?per_page=100&sort=asc", g.project(repo), number), &notes); err != nil {
		return nil, err
	}
	var out []Comment
	for _, n := range notes {
		if n.System || strings.TrimSpace(n.Body) == "" {
			continue
		}
		c := Comment{ID: fmt.Sprint(n.ID), Author: n.Author.Username, Body: strings.TrimSpace(n.Body), At: n.At}
		if n.Position != nil {
			c.Path, c.Line = n.Position.NewPath, n.Position.NewLine
		}
		out = append(out, c)
	}
	return sortByTime(out), nil
}

func (g gitlab) AddComment(ctx context.Context, repo string, number int, body string) error {
	return g.c.doJSON(ctx, "POST", fmt.Sprintf("%s/merge_requests/%d/notes", g.project(repo), number), map[string]string{"body": body}, nil)
}

// sortByTime orders comments oldest first (servers report ISO 8601 times).
func sortByTime(c []Comment) []Comment {
	sort.SliceStable(c, func(i, j int) bool { return c[i].At < c[j].At })
	return c
}

// ---- closing and reopening ----

func (g github) SetPROpen(ctx context.Context, repo string, number int, open bool) error {
	return g.c.doJSON(ctx, "PATCH", fmt.Sprintf("/repos/%s/pulls/%d", repo, number), map[string]string{"state": openWord(open)}, nil)
}

func (g gitea) SetPROpen(ctx context.Context, repo string, number int, open bool) error {
	return g.c.doJSON(ctx, "PATCH", fmt.Sprintf("/repos/%s/pulls/%d", repo, number), map[string]string{"state": openWord(open)}, nil)
}

func (g gitlab) SetPROpen(ctx context.Context, repo string, number int, open bool) error {
	event := "close"
	if open {
		event = "reopen"
	}
	return g.c.doJSON(ctx, "PUT", fmt.Sprintf("%s/merge_requests/%d", g.project(repo), number), map[string]string{"state_event": event}, nil)
}

func openWord(open bool) string {
	if open {
		return "open"
	}
	return "closed"
}

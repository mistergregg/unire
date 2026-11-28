package gitprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// Merger lists a repo's open pull requests and merges one. It is separate from
// PullRequests because only automations use it: a chat's agent never merges.
type Merger interface {
	// OpenPRs lists the repo's open pull requests (at most 100, newest first).
	OpenPRs(ctx context.Context, repo string) ([]PullRequest, error)
	// MergePR merges a pull request, only if its head is still sha, so a commit
	// pushed after its checks were read is never merged unchecked.
	MergePR(ctx context.Context, repo string, number int, sha string) error
}

// AsMerger returns p's merge support.
func AsMerger(p Provider) (Merger, error) {
	if m, ok := p.(Merger); ok {
		return m, nil
	}
	return nil, errors.New("this git server type cannot merge pull requests")
}

// mergeMethods are tried in turn: a repo may allow only some of them.
var mergeMethods = []string{"merge", "squash", "rebase"}

// notAllowed is the reply GitHub and Gitea give for a merge method the repo does
// not allow (and for a pull request that cannot be merged).
func notAllowed(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.Status == http.StatusMethodNotAllowed
}

// ---- GitHub ----

func (g github) OpenPRs(ctx context.Context, repo string) ([]PullRequest, error) {
	var list []ghPR
	if err := g.c.getJSON(ctx, "/repos/"+repo+"/pulls?state=open&per_page=100", &list); err != nil {
		return nil, err
	}
	out := make([]PullRequest, len(list))
	for i, p := range list {
		out[i] = p.pr()
	}
	return out, nil
}

func (g github) MergePR(ctx context.Context, repo string, number int, sha string) error {
	var err error
	for _, m := range mergeMethods {
		err = g.c.doJSON(ctx, "PUT", fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, number), map[string]string{"sha": sha, "merge_method": m}, nil)
		if !notAllowed(err) {
			return err
		}
	}
	return err
}

// ---- Gitea and Forgejo ----

func (g gitea) OpenPRs(ctx context.Context, repo string) ([]PullRequest, error) {
	var list []ghPR
	if err := g.c.getJSON(ctx, "/repos/"+repo+"/pulls?state=open&limit=50", &list); err != nil {
		return nil, err
	}
	out := make([]PullRequest, len(list))
	for i, p := range list {
		out[i] = p.pr()
	}
	return out, nil
}

func (g gitea) MergePR(ctx context.Context, repo string, number int, sha string) error {
	var err error
	for _, m := range mergeMethods {
		err = g.c.doJSON(ctx, "POST", fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, number), map[string]string{"Do": m, "head_commit_id": sha}, nil)
		if !notAllowed(err) {
			return err
		}
	}
	return err
}

// ---- GitLab ----

func (g gitlab) OpenPRs(ctx context.Context, repo string) ([]PullRequest, error) {
	var list []glMR
	if err := g.c.getJSON(ctx, g.project(repo)+"/merge_requests?state=opened&per_page=100", &list); err != nil {
		return nil, err
	}
	out := make([]PullRequest, len(list))
	for i, m := range list {
		out[i] = m.pr()
	}
	return out, nil
}

func (g gitlab) MergePR(ctx context.Context, repo string, number int, sha string) error {
	return g.c.doJSON(ctx, "PUT", fmt.Sprintf("%s/merge_requests/%d/merge", g.project(repo), number), map[string]string{"sha": sha}, nil)
}

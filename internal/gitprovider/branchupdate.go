package gitprovider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// BranchUpdater is a provider that can tell how far a pull request's branch has
// fallen behind the branch it goes into, and bring it up to date on the server (what
// the "Update branch" button does). Not every provider has it, so it is a separate
// interface: use AsBranchUpdater.
type BranchUpdater interface {
	// BehindBy is how many commits the base has that the pull request's branch lacks.
	BehindBy(ctx context.Context, repo string, pr PullRequest) (int, error)
	// UpdateBranch merges the base into the pull request's branch. conflict is true
	// when the two cannot be merged without someone resolving conflicts; nothing is
	// changed then.
	UpdateBranch(ctx context.Context, repo string, pr PullRequest) (conflict bool, err error)
}

// AsBranchUpdater returns p's support for updating a pull request's branch, or false.
func AsBranchUpdater(p any) (BranchUpdater, bool) {
	b, ok := p.(BranchUpdater)
	return b, ok
}

// BehindBy asks GitHub to compare the base with the pull request's head commit.
func (g github) BehindBy(ctx context.Context, repo string, pr PullRequest) (int, error) {
	if pr.Base == "" || pr.HeadSHA == "" {
		return 0, errors.New("the pull request's base or head is not known")
	}
	var cmp struct {
		BehindBy int `json:"behind_by"`
	}
	path := fmt.Sprintf("/repos/%s/compare/%s...%s", repo, url.PathEscape(pr.Base), pr.HeadSHA)
	if err := g.c.getJSON(ctx, path, &cmp); err != nil {
		return 0, err
	}
	return cmp.BehindBy, nil
}

// UpdateBranch asks GitHub to merge the base into the head, as the Update branch
// button does. GitHub says 422 when it cannot: a conflict, or the branch moved on
// since it was read (which is not a conflict, and is returned as an error to try
// again later).
func (g github) UpdateBranch(ctx context.Context, repo string, pr PullRequest) (bool, error) {
	err := g.c.doJSON(ctx, "PUT", fmt.Sprintf("/repos/%s/pulls/%d/update-branch", repo, pr.Number), map[string]string{"expected_head_sha": pr.HeadSHA}, nil)
	var he *httpError
	if errors.As(err, &he) && he.Status == 422 && strings.Contains(strings.ToLower(he.msg), "conflict") {
		return true, nil
	}
	return false, err
}

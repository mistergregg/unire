package pulls

import (
	"context"
	"fmt"
	"time"

	"github.com/mistergregg/unire/internal/gitprovider"
)

// Freshness is how a pull request's branch stands against the branch it goes into.
type Freshness int

const (
	// Current: nothing to bring in, or the server cannot say (a provider without
	// support for updating a branch).
	Current Freshness = iota
	// Updated: the branch was behind and the server merged the base into it. Its
	// checks run again.
	Updated
	// Conflicting: the branch is behind and the base does not merge cleanly. Someone
	// has to resolve the conflicts.
	Conflicting
)

// KeepCurrent brings an open pull request's branch up to date with its base the way
// the Update branch button does, when the server supports it and the branch is
// behind. A conflict changes nothing and is reported, for the agent to resolve.
func (s *Service) KeepCurrent(ctx context.Context, v View) (Freshness, error) {
	if v.State != gitprovider.PROpen || v.Base == "" || v.HeadSHA == "" {
		return Current, nil
	}
	g, ok, err := s.Store.GitServer(v.ServerID)
	if err != nil || !ok {
		return Current, err
	}
	p, err := s.Provider(g)
	if err != nil {
		return Current, err
	}
	up, ok := gitprovider.AsBranchUpdater(p)
	if !ok {
		return Current, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pr := gitprovider.PullRequest{Number: v.Number, Base: v.Base, Branch: v.Branch, HeadSHA: v.HeadSHA}
	behind, err := up.BehindBy(ctx, v.Repo, pr)
	if err != nil {
		return Current, fmt.Errorf("could not compare %s with %s: %w", v.Branch, v.Base, err)
	}
	if behind == 0 {
		return Current, nil
	}
	conflict, err := up.UpdateBranch(ctx, v.Repo, pr)
	s.forgetKey(v.ServerID, v.Repo, v.Number) // the next read must see the new head and checks
	switch {
	case err != nil:
		return Current, fmt.Errorf("could not update %s from %s: %w", v.Branch, v.Base, err)
	case conflict:
		return Conflicting, nil
	}
	return Updated, nil
}

// forgetKey drops the cached view of one pull request.
func (s *Service) forgetKey(serverID, repo string, number int) {
	s.mu.Lock()
	delete(s.cache, fmt.Sprintf("%s/%s/%d", serverID, repo, number))
	s.mu.Unlock()
}

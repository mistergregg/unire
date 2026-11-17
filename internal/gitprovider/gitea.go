package gitprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// Gitea also serves Forgejo, which has the same API.
type gitea struct{ c *client }

func init() {
	f := func(cfg Config) (Provider, error) {
		return gitea{newClient(cfg, cfg.BaseURL+"/api/v1", http.Header{
			"Authorization": {"token " + cfg.Token},
			"Accept":        {"application/json"},
		})}, nil
	}
	help := "In Gitea: Settings, Applications, Generate New Token. Under permissions choose: repository Read and Write, issue Read and Write (for pull requests), user Read, organization Read. (A token without user Read still works for cloning, but cannot be identified.)"
	Register(Info{Type: "gitea", Label: "Gitea", BaseURLRequired: true, BaseURLHint: "e.g. https://git.example.com", TokenHelp: help}, f)
	Register(Info{Type: "forgejo", Label: "Forgejo", BaseURLRequired: true, BaseURLHint: "e.g. https://codeberg.org", TokenHelp: help}, f)
}

func (g gitea) TestConnection(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	err := g.c.getJSON(ctx, "/user", &u)
	if err == nil {
		return u.Login, nil
	}
	// Gitea tokens have scopes. One made with repository access only is refused
	// by /user (which needs "user: Read") but is perfectly good for what Unire
	// does, so check the thing that matters: can it see repositories?
	if hasStatus(err, http.StatusForbidden) {
		var res struct {
			Data []json.RawMessage `json:"data"`
		}
		if g.c.getJSON(ctx, "/repos/search?limit=1", &res) == nil {
			return "a token without user permission (it can read repositories)", nil
		}
	}
	return "", err
}

func (g gitea) ListRepos(ctx context.Context, query string) ([]Repo, error) {
	var res struct {
		Data []struct {
			FullName      string `json:"full_name"`
			Description   string `json:"description"`
			CloneURL      string `json:"clone_url"`
			DefaultBranch string `json:"default_branch"`
			Private       bool   `json:"private"`
		} `json:"data"`
	}
	q := url.Values{"q": {query}, "limit": {"50"}}
	if err := g.c.getJSON(ctx, "/repos/search?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	repos := make([]Repo, 0, len(res.Data))
	for _, d := range res.Data {
		repos = append(repos, Repo{d.FullName, d.Description, d.CloneURL, d.DefaultBranch, d.Private})
	}
	return repos, nil
}

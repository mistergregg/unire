package gitprovider

import (
	"context"
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
	help := "In Gitea: Settings, Applications, Generate New Token. Give it read and write access to repositories."
	Register(Info{Type: "gitea", Label: "Gitea", BaseURLRequired: true, BaseURLHint: "e.g. https://git.example.com", TokenHelp: help}, f)
	Register(Info{Type: "forgejo", Label: "Forgejo", BaseURLRequired: true, BaseURLHint: "e.g. https://codeberg.org", TokenHelp: help}, f)
}

func (g gitea) TestConnection(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := g.c.getJSON(ctx, "/user", &u); err != nil {
		return "", err
	}
	return u.Login, nil
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

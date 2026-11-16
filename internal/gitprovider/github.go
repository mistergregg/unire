package gitprovider

import (
	"context"
	"net/http"
)

type github struct{ c *client }

func init() {
	Register(Info{
		Type: "github", Label: "GitHub", BaseURLHint: "leave empty for github.com; for GitHub Enterprise use https://github.example.com",
		TokenHelp: "In GitHub: Settings, Developer settings, Fine-grained tokens. Choose only the repos agents may use, with Contents set to read and write.",
	}, func(cfg Config) (Provider, error) {
		// api.github.com by default; GitHub Enterprise uses <host>/api/v3.
		base := "https://api.github.com"
		if cfg.BaseURL != "" {
			base = cfg.BaseURL + "/api/v3"
		}
		return github{newClient(cfg, base, http.Header{
			"Authorization":        {"Bearer " + cfg.Token},
			"Accept":               {"application/vnd.github+json"},
			"X-GitHub-Api-Version": {"2022-11-28"},
		})}, nil
	})
}

func (g github) TestConnection(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := g.c.getJSON(ctx, "/user", &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

func (g github) ListRepos(ctx context.Context, query string) ([]Repo, error) {
	var res []struct {
		FullName      string `json:"full_name"`
		Description   string `json:"description"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
	}
	if err := g.c.getJSON(ctx, "/user/repos?per_page=100&sort=updated", &res); err != nil {
		return nil, err
	}
	var repos []Repo
	for _, d := range res {
		r := Repo{d.FullName, d.Description, d.CloneURL, d.DefaultBranch, d.Private}
		if matches(r, query) {
			repos = append(repos, r)
		}
	}
	return repos, nil
}

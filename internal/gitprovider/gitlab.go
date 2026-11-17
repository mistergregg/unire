package gitprovider

import (
	"context"
	"net/http"
	"net/url"
)

type gitlab struct{ c *client }

func init() {
	Register(Info{
		Type: "gitlab", Label: "GitLab", BaseURLHint: "leave empty for gitlab.com; for self-hosted use https://gitlab.example.com",
		TokenHelp: "In GitLab: User settings, Access tokens. Scopes: api (to open merge requests and read their pipelines and comments), read_repository and write_repository.",
	}, func(cfg Config) (Provider, error) {
		base := "https://gitlab.com"
		if cfg.BaseURL != "" {
			base = cfg.BaseURL
		}
		return gitlab{newClient(cfg, base+"/api/v4", http.Header{
			"PRIVATE-TOKEN": {cfg.Token},
			"Accept":        {"application/json"},
		})}, nil
	})
}

func (g gitlab) TestConnection(ctx context.Context) (string, error) {
	var u struct {
		Username string `json:"username"`
	}
	if err := g.c.getJSON(ctx, "/user", &u); err != nil {
		return "", err
	}
	return u.Username, nil
}

func (g gitlab) ListRepos(ctx context.Context, query string) ([]Repo, error) {
	var res []struct {
		PathWithNamespace string `json:"path_with_namespace"`
		Description       string `json:"description"`
		HTTPURLToRepo     string `json:"http_url_to_repo"`
		DefaultBranch     string `json:"default_branch"`
		Visibility        string `json:"visibility"`
	}
	q := url.Values{"membership": {"true"}, "per_page": {"50"}, "order_by": {"last_activity_at"}, "search": {query}}
	if err := g.c.getJSON(ctx, "/projects?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	repos := make([]Repo, 0, len(res))
	for _, d := range res {
		repos = append(repos, Repo{d.PathWithNamespace, d.Description, d.HTTPURLToRepo, d.DefaultBranch, d.Visibility != "public"})
	}
	return repos, nil
}

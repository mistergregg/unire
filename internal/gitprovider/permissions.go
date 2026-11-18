package gitprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Capability is one thing pull requests need from a token, and whether the
// token has it.
type Capability struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"` // why not, or what could not be confirmed
}

// PermissionChecker tells what a token can do with pull requests in a repo,
// without changing anything: it reads what it can, and takes what a server says
// about writing. A server that cannot say is reported as unconfirmed.
type PermissionChecker interface {
	CheckPullRequestAccess(ctx context.Context, repo string) ([]Capability, error)
}

// The capabilities every provider reports, in this order.
const (
	CapPulls    = "See pull requests"
	CapChecks   = "See checks (builds and tests)"
	CapComments = "See comments"
	CapWrite    = "Open pull requests and comment"
)

// probe runs one read and turns its result into a capability.
func probe(name string, err error) Capability {
	if err == nil {
		return Capability{Name: name, OK: true}
	}
	var he *httpError
	switch {
	case errors.As(err, &he) && (he.Status == http.StatusForbidden || he.Status == http.StatusUnauthorized):
		return Capability{Name: name, Note: "the token is not allowed to"}
	case errors.As(err, &he) && he.Status == http.StatusNotFound:
		// Servers answer 404 for what a token may not see.
		return Capability{Name: name, Note: "not found: pull requests or issues may be turned off for this repo, or the token cannot see it"}
	}
	return Capability{Name: name, Note: err.Error()}
}

// writeFrom reports the write capability from what the server says the user can
// do in the repo. A token can have less than its user, which only trying shows, so
// a yes is a "no sign of a problem" and says what the token needs.
func writeFrom(canPush bool, need string) Capability {
	if !canPush {
		return Capability{Name: CapWrite, Note: "this account cannot write to the repo"}
	}
	return Capability{Name: CapWrite, OK: true, Note: "not tried, so as not to change anything. The token needs " + need}
}

// ---- GitHub ----

func (g github) CheckPullRequestAccess(ctx context.Context, repo string) ([]Capability, error) {
	var info struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := g.c.getJSON(ctx, "/repos/"+repo, &info); err != nil {
		return nil, fmt.Errorf("cannot see %s: %w", repo, err)
	}
	var list []any
	caps := []Capability{probe(CapPulls, g.c.getJSON(ctx, "/repos/"+repo+"/pulls?per_page=1", &list))}

	var commits []struct {
		SHA string `json:"sha"`
	}
	checks := g.c.getJSON(ctx, "/repos/"+repo+"/commits?per_page=1", &commits)
	if checks == nil && len(commits) > 0 {
		var runs any
		checks = g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=1", repo, commits[0].SHA), &runs)
		if checks != nil {
			// A fine-grained token has no Checks permission; the workflow runs (Actions,
			// read-only) show GitHub Actions results instead.
			if _, werr := g.workflowRunChecks(ctx, repo, commits[0].SHA); werr == nil {
				checks = nil
			}
		}
	}
	caps = append(caps, probe(CapChecks, checks))
	caps = append(caps, probe(CapComments, g.c.getJSON(ctx, "/repos/"+repo+"/issues/comments?per_page=1", &list)))
	return append(caps, writeFrom(info.Permissions.Push, "Pull requests: read and write (and Issues: read and write)")), nil
}

// ---- Gitea and Forgejo ----

func (g gitea) CheckPullRequestAccess(ctx context.Context, repo string) ([]Capability, error) {
	var info struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := g.c.getJSON(ctx, "/repos/"+repo, &info); err != nil {
		return nil, fmt.Errorf("cannot see %s: %w", repo, err)
	}
	var list []any
	caps := []Capability{probe(CapPulls, g.c.getJSON(ctx, "/repos/"+repo+"/pulls?limit=1", &list))}

	var commits []struct {
		SHA string `json:"sha"`
	}
	checks := g.c.getJSON(ctx, "/repos/"+repo+"/commits?limit=1", &commits)
	if checks == nil && len(commits) > 0 {
		var st any
		checks = g.c.getJSON(ctx, fmt.Sprintf("/repos/%s/commits/%s/status", repo, commits[0].SHA), &st)
	}
	caps = append(caps, probe(CapChecks, checks))
	caps = append(caps, probe(CapComments, g.c.getJSON(ctx, "/repos/"+repo+"/issues?limit=1&type=pulls", &list)))
	return append(caps, writeFrom(info.Permissions.Push, "repository and issue permissions set to Read and Write")), nil
}

// ---- GitLab ----

func (g gitlab) CheckPullRequestAccess(ctx context.Context, repo string) ([]Capability, error) {
	var self struct {
		Scopes []string `json:"scopes"`
	}
	// GitLab says which scopes a personal token has.
	if err := g.c.getJSON(ctx, "/personal_access_tokens/self", &self); err == nil && len(self.Scopes) > 0 {
		has := map[string]bool{}
		for _, s := range self.Scopes {
			has[s] = true
		}
		read := has["api"] || has["read_api"]
		note := ""
		if !read {
			note = "the token has the scopes " + strings.Join(self.Scopes, ", ") + "; it needs api"
		}
		write := Capability{Name: CapWrite, OK: has["api"]}
		if !write.OK {
			write.Note = "the token needs the api scope (it has " + strings.Join(self.Scopes, ", ") + ")"
		}
		return []Capability{
			{Name: CapPulls, OK: read, Note: note},
			{Name: CapChecks, OK: read, Note: note},
			{Name: CapComments, OK: read, Note: note},
			write,
		}, nil
	}
	// A project or group token cannot be asked: read what it can.
	var info struct {
		Permissions struct {
			Project *struct {
				Level int `json:"access_level"`
			} `json:"project_access"`
			Group *struct {
				Level int `json:"access_level"`
			} `json:"group_access"`
		} `json:"permissions"`
	}
	if err := g.c.getJSON(ctx, g.project(repo), &info); err != nil {
		return nil, fmt.Errorf("cannot see %s: %w", repo, err)
	}
	var list []any
	caps := []Capability{
		probe(CapPulls, g.c.getJSON(ctx, g.project(repo)+"/merge_requests?per_page=1", &list)),
		probe(CapChecks, g.c.getJSON(ctx, g.project(repo)+"/pipelines?per_page=1", &list)),
		probe(CapComments, g.c.getJSON(ctx, g.project(repo)+"/merge_requests?per_page=1&scope=all", &list)),
	}
	level := 0
	if info.Permissions.Project != nil {
		level = info.Permissions.Project.Level
	}
	if info.Permissions.Group != nil && info.Permissions.Group.Level > level {
		level = info.Permissions.Group.Level
	}
	// 30 is Developer, the lowest level that can open merge requests.
	if level == 0 {
		return append(caps, Capability{Name: CapWrite, OK: true, Note: "not tried, so as not to change anything. The token needs the api scope"}), nil
	}
	return append(caps, writeFrom(level >= 30, "the api scope")), nil
}

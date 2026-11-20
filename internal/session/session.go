// Package session describes what a coding session should do: which repos,
// which branch, which agent and prompt. It has no knowledge of Docker or git
// servers, so it can be reused by any runner or UI.
package session

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Repo is one repository checked out in the session workspace.
type Repo struct {
	Name string // directory name under /workspace
	URL  string // clone URL as seen from inside the sandbox
	Base string // base branch to start from; empty means the remote default
	// LocalPath is set when the repo is a directory on the host. The runner
	// mounts it into the sandbox and URL points at the mount.
	LocalPath string
}

// Spec is a full session request.
type Spec struct {
	ID     string
	Task   string // human label, used in the branch name
	Agent  string // adapter name, e.g. "claude"
	Model  string // model name; empty means the agent's default
	Prompt string // empty means interactive
	Mode   string // "hands-off" or "ask-first"
	Repos  []Repo
	NoPush bool
	// BranchName, when set, is the branch to use instead of one derived from the
	// agent and task. A reply uses its conversation's branch; a new conversation
	// gets a name that no other conversation has.
	BranchName string
}

// Branch returns the branch every repo in the session is checked out on.
func (s Spec) Branch() string {
	if s.BranchName != "" {
		return s.BranchName
	}
	return fmt.Sprintf("agent/%s/%s", Slug(s.Agent), Slug(s.Task))
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug lowercases s and replaces runs of other characters with a dash.
func Slug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "task"
	}
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// ParseRepo parses "<url-or-path>[#base-branch]". An existing directory on
// the host becomes a LocalPath repo.
func ParseRepo(arg string) (Repo, error) {
	if strings.TrimSpace(arg) == "" {
		return Repo{}, fmt.Errorf("empty repo argument")
	}
	loc, base := arg, ""
	if i := strings.LastIndex(arg, "#"); i >= 0 {
		loc, base = arg[:i], arg[i+1:]
	}
	if loc == "" {
		return Repo{}, fmt.Errorf("repo %q has no location", arg)
	}
	name := repoName(loc)
	if fi, err := os.Stat(loc); err == nil && fi.IsDir() {
		abs, err := filepath.Abs(loc)
		if err != nil {
			return Repo{}, err
		}
		return Repo{Name: name, URL: "/remotes/" + name, Base: base, LocalPath: abs}, nil
	}
	return Repo{Name: name, URL: loc, Base: base}, nil
}

func repoName(loc string) string {
	loc = strings.ReplaceAll(loc, "\\", "/")
	loc = strings.TrimRight(loc, "/")
	loc = strings.TrimSuffix(loc, ".git")
	if i := strings.LastIndex(loc, ":"); i >= 0 && !strings.Contains(loc, "://") && !strings.HasPrefix(loc, "/") && len(loc) > 2 && loc[1] != ':' {
		loc = loc[i+1:] // scp-style git@host:owner/repo
	}
	n := path.Base(loc)
	if n == "." || n == "/" || n == "" {
		return "repo"
	}
	return Slug(n)
}

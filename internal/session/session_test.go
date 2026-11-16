package session

import "testing"

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Add a login page!": "add-a-login-page",
		"  ":                "task",
		"UPPER_case--x":     "upper-case-x",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBranch(t *testing.T) {
	s := Spec{Agent: "claude", Task: "Fix the bug"}
	if got, want := s.Branch(), "agent/claude/fix-the-bug"; got != want {
		t.Errorf("Branch() = %q, want %q", got, want)
	}
}

func TestParseRepoRemote(t *testing.T) {
	cases := []struct{ in, name, url, base string }{
		{"https://git.example.com/me/api.git", "api", "https://git.example.com/me/api.git", ""},
		{"https://github.com/me/web#develop", "web", "https://github.com/me/web", "develop"},
		{"git@github.com:me/shared-lib.git", "shared-lib", "git@github.com:me/shared-lib.git", ""},
	}
	for _, c := range cases {
		r, err := ParseRepo(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if r.Name != c.name || r.URL != c.url || r.Base != c.base || r.LocalPath != "" {
			t.Errorf("ParseRepo(%q) = %+v", c.in, r)
		}
	}
}

func TestParseRepoLocalDir(t *testing.T) {
	dir := t.TempDir()
	r, err := ParseRepo(dir + "#main")
	if err != nil {
		t.Fatal(err)
	}
	if r.LocalPath == "" || r.Base != "main" || r.URL != "/remotes/"+r.Name {
		t.Errorf("unexpected repo %+v", r)
	}
}

func TestParseRepoEmpty(t *testing.T) {
	for _, in := range []string{"", "  ", "#main"} {
		if _, err := ParseRepo(in); err == nil {
			t.Errorf("ParseRepo(%q) should fail", in)
		}
	}
}

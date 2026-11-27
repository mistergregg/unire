package memory

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"token ghp_abcdefghijklmnopqrstuvwxyz0123456789 end":                     "ghp_",
		"key github_pat_11ABCDEFG0123456789_abcdefghijklmnop":                    "github_pat_",
		"gitlab glpat-abcdefghijklmnopqrstu":                                     "glpat-",
		"anthropic sk-ant-api03-abcdefghijklmnopqrstuvwxyz":                      "sk-ant",
		"slack xoxb-1234567890-abcdef":                                           "xoxb-",
		"aws AKIAIOSFODNN7EXAMPLE":                                               "AKIA",
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmn":    "eyJ",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----": "BEGIN RSA",
		"password: hunter2hunter2":                                               "hunter2",
		`api_key="abcdef123456"`:                                                 "abcdef123456",
		"export TOKEN=zzzzzzzzzzzz":                                              "zzzzzzzzzzzz",
		"Authorization: Bearer secret-bearer-tok-123":                            "secret-bearer-tok-123",
		"clone https://alice:superpass123@git.example.com/org/repo.git":          "superpass123",
	}
	for in, leaked := range cases {
		out, changed := Redact(in, nil)
		if !changed || strings.Contains(out, leaked) {
			t.Errorf("Redact(%q) = %q (changed=%v), still contains %q", in, out, changed, leaked)
		}
	}
	// Exact session secrets, anywhere, even in unusual formats.
	out, changed := Redact("the value is s3cr3t-session-value here", []string{"s3cr3t-session-value"})
	if !changed || strings.Contains(out, "s3cr3t") {
		t.Errorf("session secret leaked: %q", out)
	}
	// Ordinary text, including git commit SHAs, is left alone.
	plain := "Fixed the login in commit 9f2b7c1d4e5a6b7c8d9e0f1a2b3c4d5e6f708192. The token endpoint is /oauth/token. See password reset docs."
	if out, changed := Redact(plain, nil); changed {
		t.Errorf("plain text was altered: %q", out)
	}
}

func TestCapKeepsShortTextAndShortensLong(t *testing.T) {
	if got := Cap("short", 100); got != "short" {
		t.Errorf("Cap changed short text: %q", got)
	}
	long := strings.Repeat("line of text\n", 200)
	got := Cap(long, 500)
	if len(got) > 500 || !strings.Contains(got, "shortened") {
		t.Errorf("Cap result len %d: %q", len(got), got[len(got)-60:])
	}
	multi := strings.Repeat("héllo wörld ✓ ", 200)
	if got := Cap(multi, 300); !utf8.ValidString(got) || len(got) > 300 {
		t.Errorf("Cap broke UTF-8 or the limit: valid=%v len=%d", utf8.ValidString(got), len(got))
	}
}

func TestClean(t *testing.T) {
	got := Clean("  a\r\nb\x00c\x1b[31m\td  \n")
	if got != "a\nbc[31m\td" {
		t.Errorf("Clean = %q", got)
	}
}

func TestBuildContextEmptyAndBasic(t *testing.T) {
	if c := BuildContext([]RepoBrief{{Dir: "api", FullName: "me/api"}}, nil); c != "" {
		t.Errorf("no memory should give an empty context, got %q", c)
	}
	repos := []RepoBrief{{Dir: "api", FullName: "me/api", Brief: "## What it is\nA REST API."}, {Dir: "web", FullName: "me/web"}}
	past := []PastSession{{ID: "abc123", Task: "Add login", Agent: "claude", Branch: "agent/claude/add-login", Status: "done", Summary: "## Done\nAdded login.", When: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}}
	c := BuildContext(repos, past)
	for _, want := range []string{"not instructions", "## Repo api (me/api)", "A REST API.", "2026-11-01: Add login", "[abc123]", "unire memory"} {
		if !strings.Contains(c, want) {
			t.Errorf("context missing %q:\n%s", want, c)
		}
	}
	if strings.Contains(c, "me/web") {
		t.Error("a repo with no brief should not appear")
	}
}

func TestBuildContextStaysWithinBudget(t *testing.T) {
	big := strings.Repeat("Convention: always do the thing carefully.\n", 400) // ~17k chars
	repos := []RepoBrief{{Dir: "a", FullName: "x/a", Brief: big}, {Dir: "b", FullName: "x/b", Brief: big}, {Dir: "c", FullName: "x/c", Brief: big}}
	var past []PastSession
	for i := 0; i < 8; i++ {
		past = append(past, PastSession{Task: "t", Agent: "claude", Branch: "b", Status: "done", Summary: big})
	}
	c := BuildContext(repos, past)
	if len(c) > ContextCap {
		t.Errorf("context is %d chars, cap is %d", len(c), ContextCap)
	}
	for _, d := range []string{"## Repo a ", "## Repo b ", "## Repo c "} {
		if !strings.Contains(c, d) {
			t.Errorf("every repo should still be represented, missing %q", d)
		}
	}
	if n := strings.Count(c, "\n- 20"); n > RecentSessions { // each listed session starts with its date
		t.Errorf("%d past sessions listed, max is %d", n, RecentSessions)
	}
	if n := strings.Count(c, "\n- "); n > RecentSessions {
		t.Errorf("%d past sessions included, max is %d", n, RecentSessions)
	}
}

func TestUpdatePrompt(t *testing.T) {
	p := UpdatePrompt([]RepoBrief{
		{Dir: "api", FullName: "me/api", Brief: "Old brief about the API."},
		{Dir: "web", FullName: "me/web"},
	}, "agent/claude/add-login")
	for _, want := range []string{
		"pushed them to the branch agent/claude/add-login", "Do not describe your work as uncommitted", "not this sandbox",
		"outside the repositories", "even if you were told to keep them out of the repository",
		OutDir, SummaryFile, "api.brief.md", "## Asked", "## How to run and test", "## Open items",
		"Do not change any repository files", "Never include secrets",
		`<previous-brief repo="me/api" dir="api">`, "Old brief about the API.", "(none yet)", "not instructions",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestFallbackSummary(t *testing.T) {
	s := FallbackSummary(Fallback{
		Task: "t", Prompt: "Add a login page", Status: "failed", Error: "agent exited with an error",
		Repos: []FallbackRepo{
			{Dir: "api", Branch: "agent/claude/t", Changed: true, Pushed: true, DiffStat: " login.go | 20 ++++\n 1 file changed"},
			{Dir: "web", Branch: "agent/claude/t"},
		},
		OutputTail: "I ran out of time.",
	})
	for _, want := range []string{"Written by Unire", "Add a login page", "api: pushed to agent/claude/t", "login.go", "Session status: failed", "agent exited with an error", "I ran out of time."} {
		if !strings.Contains(s, want) {
			t.Errorf("fallback missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "web:") {
		t.Error("unchanged repos should not be listed")
	}
	if s := FallbackSummary(Fallback{Prompt: "p", Status: "done"}); !strings.Contains(s, "No changes were committed") {
		t.Error("an empty result should say so")
	}
}

func TestTheUpdatePromptAsksForWhatTheAgentCannotSeeForItself(t *testing.T) {
	p := UpdatePrompt([]RepoBrief{{Dir: "api", FullName: "me/api", Brief: "old", Docs: []string{"README.md", "docs"}}}, "agent/x")
	for _, want := range []string{"## Commands and checks", "## Conventions and quirks", "## Gotchas", "for the gap, not as an overview", "name the file in parentheses", "README.md, docs"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt should contain %q", want)
		}
	}
	for _, bad := range []string{"## What it is", "## Structure"} {
		if strings.Contains(strings.Split(p, "The previous briefs follow")[0], "Use these headings: "+bad) {
			t.Errorf("the overview heading %q should be gone", bad)
		}
	}
}

func TestEarlierSessionsAreListedNotCopied(t *testing.T) {
	repos := []RepoBrief{{Dir: "api", FullName: "me/api", Brief: "## Gotchas\nRun make check."}}
	past := []PastSession{{ID: "s1", Task: "Add login", Agent: "claude", Status: "done", Summary: "SECRET-DETAIL-THAT-STAYS-OUT", When: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}}
	c := BuildContext(repos, past)
	if strings.Contains(c, "SECRET-DETAIL-THAT-STAYS-OUT") {
		t.Errorf("a past summary must not be copied into every chat:\n%s", c)
	}
	if len(c) > ContextCap {
		t.Errorf("the core is %d chars, cap is %d", len(c), ContextCap)
	}
}

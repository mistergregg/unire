package tools

import (
	"strings"
	"testing"
)

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		`  "Fix the login redirect"  `: "Fix the login redirect",
		"two\nlines\there":             "two lines here",
		"bell\x07 and esc\x1b[31m":     "bell and esc [31m",
		"":                             "",
		strings.Repeat("a", 200):       strings.Repeat("a", 79) + "…",
	}
	for in, want := range cases {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPromptListsOnlyTheToolsGiven(t *testing.T) {
	if Prompt(nil) != "" {
		t.Error("no tools, no text")
	}
	p := Prompt(ForNewConversation())
	if !strings.Contains(p, `unire name "<title>"`) {
		t.Errorf("prompt does not describe the name tool: %q", p)
	}
	if !strings.Contains(p, "unire-shot") || !strings.Contains(p, "unire-lsp") || !strings.Contains(p, "unire-diff") {
		t.Errorf("prompt does not mention the checking tools: %q", p)
	}
}

func TestRequestsSkipsBlankLines(t *testing.T) {
	got := Requests("one\n\n  two  \n")
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("Requests = %q", got)
	}
}

func TestShareIsOfferedInEveryConversation(t *testing.T) {
	for name, set := range map[string][]Tool{"new": ForNewConversation(), "reply": ForReply(), "reply with PR": ForReplyWithPR()} {
		found := false
		for _, tl := range set {
			found = found || tl.Name == Share.Name
		}
		if !found {
			t.Errorf("%s: the share tool is missing", name)
		}
	}
	if !strings.Contains(Prompt(ForReply()), "unire share <file>") {
		t.Error("the prompt does not describe share")
	}
}

func TestForReplyWithPRIncludesPRAndPRBody(t *testing.T) {
	tools := ForReplyWithPR()
	hasPR, hasPRBody := false, false
	for _, tl := range tools {
		if tl.Name == PR.Name {
			hasPR = true
		}
		if tl.Name == PRBody.Name {
			hasPRBody = true
		}
	}
	if !hasPR || !hasPRBody {
		t.Errorf("ForReplyWithPR must include pr and pr-body: hasPR=%v, hasPRBody=%v", hasPR, hasPRBody)
	}
}

func TestFormatPRTitle(t *testing.T) {
	cases := map[string]string{
		"":                                             "",
		"   ":                                          "",
		"Feature: Add user profile":                    "Feature: Add user profile",
		"feature: add user profile":                    "Feature: Add user profile",
		"feat: add user profile":                       "Feature: Add user profile",
		"feat(auth): add login page":                   "Feature: Add login page",
		"Fix: The login redirect":                      "Fix: The login redirect",
		"fix: the login redirect":                      "Fix: The login redirect",
		"bugfix: the login redirect":                   "Fix: The login redirect",
		"hotfix: null pointer in handler":              "Fix: Null pointer in handler",
		"fix(ci): timeout in tests":                    "Fix: Timeout in tests",
		"Fix the login redirect":                       "Fix: The login redirect",
		"Fixing race condition":                        "Fix: Race condition",
		"Fixed broken test":                            "Fix: Broken test",
		"Add user profile":                             "Feature: Add user profile",
		"Support dark mode":                            "Feature: Support dark mode",
		"Resolve crash on startup":                     "Fix: Resolve crash on startup",
		"Prevent memory leak in worker":                "Fix: Prevent memory leak in worker",
		"Fix the login redirect (into release/1.2)":    "Fix: The login redirect (into release/1.2)",
		"Feature: Add user profile (into release/1.2)": "Feature: Add user profile (into release/1.2)",
	}
	for in, want := range cases {
		if got := FormatPRTitle(in); got != want {
			t.Errorf("FormatPRTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

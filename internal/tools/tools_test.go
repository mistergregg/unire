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
}

func TestRequestsSkipsBlankLines(t *testing.T) {
	got := Requests("one\n\n  two  \n")
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("Requests = %q", got)
	}
}

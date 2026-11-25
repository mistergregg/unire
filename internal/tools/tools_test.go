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

func TestCleanFallbackTitle(t *testing.T) {
	cases := map[string]string{
		"Please fix the login redirect bug":                                   "Fix the login redirect bug",
		"Can you please add unit tests for pulls":                             "Add unit tests for pulls",
		"I want to support dark mode in the sidebar":                          "Support dark mode in the sidebar",
		"Im thinking of a new feature, I would love it whether its a preview": "Preview",
		"why is the app slow":                                                 "",
		"":                                                                    "",
	}
	for in, want := range cases {
		if got := CleanFallbackTitle(in); got != want {
			t.Errorf("CleanFallbackTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if !IsCall("name") {
		t.Error("name must be in CallTools so agents can name conversations live")
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

func TestReplyWithPRAllowsDescribingNewBranch(t *testing.T) {
	p := Prompt(ForReplyWithPR())
	if !strings.Contains(p, "new branch or pull request pushed this turn") {
		t.Errorf("prompt does not mention new branch or pull request: %q", p)
	}
}

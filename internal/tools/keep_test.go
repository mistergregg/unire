package tools

import (
	"strings"
	"testing"
)

// Every agent is told not to remove what works to fix something.
func TestEveryAgentIsToldToKeepWhatWorks(t *testing.T) {
	for name, set := range map[string][]Tool{"new": ForNewConversation(), "reply": ForReply(), "reply with a pull request": ForReplyWithPR()} {
		p := Prompt(set)
		for _, want := range []string{"# Keep what works", "is not a fix unless the owner asked", "Ask the owner first", "read your diff for anything you deleted"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s: the prompt is missing %q", name, want)
			}
		}
	}
	if Prompt(nil) != "" {
		t.Error("no tools, no prompt")
	}
}

func TestPromptHasTalkingWithOwner(t *testing.T) {
	for _, set := range [][]Tool{ForNewConversation(), ForReply(), ForReplyWithPR()} {
		p := Prompt(set)
		for _, want := range []string{"# Talking with the owner", "not a ticket", "A question gets an answer", "say which reading you went with", "Do what they asked, not more"} {
			if !strings.Contains(p, want) {
				t.Errorf("prompt is missing %q", want)
			}
		}
		if strings.Index(p, "# Talking with the owner") > strings.Index(p, "# Unire tools") {
			t.Error("talking guidance should come before the tools")
		}
	}
}

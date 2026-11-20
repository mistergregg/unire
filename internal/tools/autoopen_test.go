package tools

import (
	"strings"
	"testing"
)

func TestWithAutoOpenTellsTheAgentItsPullRequestIsOpenedForIt(t *testing.T) {
	set := WithAutoOpen(ForNewConversation())
	var pr Tool
	for _, x := range set {
		if x.Name == "pr" {
			pr = x
		}
	}
	if !strings.Contains(pr.Usage, "Unire opens the pull request for you") || strings.Contains(pr.Usage, "asks the owner") {
		t.Errorf("pr = %q", pr.Usage)
	}
	if len(set) != len(ForNewConversation()) {
		t.Error("the other tools are kept")
	}
	// A conversation whose pull request is already open keeps its own pr tool.
	for _, x := range WithAutoOpen(ForReplyWithPR()) {
		if x.Name == "pr" && x.Usage == PRAuto.Usage {
			t.Error("pr-update must not be replaced")
		}
	}
}

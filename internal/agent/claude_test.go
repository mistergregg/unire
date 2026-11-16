package agent

import (
	"reflect"
	"testing"
)

const ref = "11111111-2222-4333-8444-555555555555"

func TestClaudeCommands(t *testing.T) {
	a, err := Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		prompt, mode, ref string
		want              []string
	}{
		{"do it", HandsOff, ref, []string{"claude", "-p", "--dangerously-skip-permissions", "--session-id", ref, "--", "do it"}},
		{"do it", HandsOff, "", []string{"claude", "-p", "--dangerously-skip-permissions", "--", "do it"}},
		{"--version", HandsOff, ref, []string{"claude", "-p", "--dangerously-skip-permissions", "--session-id", ref, "--", "--version"}},
		{"", HandsOff, "", []string{"claude", "--dangerously-skip-permissions"}},
		{"", HandsOff, ref, []string{"claude", "--dangerously-skip-permissions", "--session-id", ref}},
		{"do it", AskFirst, ref, []string{"claude", "--session-id", ref, "--", "do it"}},
		{"", AskFirst, "", []string{"claude"}},
	}
	for _, c := range cases {
		got, err := a.Command(c.prompt, c.mode, c.ref)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Command(%q,%q,%q) = %v, %v; want %v", c.prompt, c.mode, c.ref, got, err, c.want)
		}
	}
	if _, err := a.Command("x", "bogus", ""); err == nil {
		t.Error("unknown mode should fail")
	}
	if _, err := Get("nope"); err == nil {
		t.Error("unknown agent should fail")
	}
}

func TestClaudeFollowUpResumesByID(t *testing.T) {
	a, _ := Get("claude")
	got, ok := a.FollowUp("update memory", HandsOff, ref)
	want := []string{"claude", "-p", "--dangerously-skip-permissions", "--resume", ref, "--", "update memory"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("FollowUp = %v, %v; want %v", got, ok, want)
	}
	// Without an id it must not fall back to "the latest conversation".
	if _, ok := a.FollowUp("x", HandsOff, ""); ok {
		t.Error("FollowUp without a ref must not resume anything")
	}
	if _, ok := a.FollowUp("x", AskFirst, ref); ok {
		t.Error("follow-ups are for unattended sessions")
	}
}

func TestEveryAdapterDescribesItself(t *testing.T) {
	for _, n := range Names() {
		a, _ := Get(n)
		if a.MemoryFile() == "" || len(a.Hosts()) == 0 {
			t.Errorf("%s: needs a memory file name and hosts", n)
		}
	}
}

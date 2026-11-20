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
	// With a model chosen.
	withModel := a.WithModel("opus")
	modelCases := []struct {
		prompt, mode, ref string
		want              []string
	}{
		{"do it", HandsOff, ref, []string{"claude", "-p", "--dangerously-skip-permissions", "--session-id", ref, "--model", "opus", "--", "do it"}},
		{"do it", HandsOff, "", []string{"claude", "-p", "--dangerously-skip-permissions", "--model", "opus", "--", "do it"}},
		{"", HandsOff, ref, []string{"claude", "--dangerously-skip-permissions", "--session-id", ref, "--model", "opus"}},
		{"", HandsOff, "", []string{"claude", "--dangerously-skip-permissions", "--model", "opus"}},
		{"do it", AskFirst, ref, []string{"claude", "--session-id", ref, "--model", "opus", "--", "do it"}},
		{"", AskFirst, "", []string{"claude", "--model", "opus"}},
	}
	for _, c := range modelCases {
		got, err := withModel.Command(c.prompt, c.mode, c.ref)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("WithModel.Command(%q,%q,%q) = %v, %v; want %v", c.prompt, c.mode, c.ref, got, err, c.want)
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
	// With a model.
	gotModel, okModel := a.WithModel("opus").FollowUp("update memory", HandsOff, ref)
	wantModel := []string{"claude", "-p", "--dangerously-skip-permissions", "--model", "opus", "--resume", ref, "--", "update memory"}
	if !okModel || !reflect.DeepEqual(gotModel, wantModel) {
		t.Errorf("WithModel.FollowUp = %v, %v; want %v", gotModel, okModel, wantModel)
	}
	// StreamCommand with a model.
	streamer := a.WithModel("opus").(Streamer)
	sgot, err := streamer.StreamCommand("do it", HandsOff, ref)
	if err != nil {
		t.Fatal(err)
	}
	swant := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--session-id", ref, "--model", "opus", "--", "do it"}
	if !reflect.DeepEqual(sgot, swant) {
		t.Errorf("WithModel.StreamCommand = %v; want %v", sgot, swant)
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

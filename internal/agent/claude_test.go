package agent

import (
	"reflect"
	"testing"
)

func TestClaudeCommands(t *testing.T) {
	a, err := Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		prompt, mode string
		want         []string
	}{
		{"do it", HandsOff, []string{"claude", "-p", "--dangerously-skip-permissions", "--", "do it"}},
		{"--version", HandsOff, []string{"claude", "-p", "--dangerously-skip-permissions", "--", "--version"}},
		{"", HandsOff, []string{"claude", "--dangerously-skip-permissions"}},
		{"do it", AskFirst, []string{"claude", "--", "do it"}},
		{"", AskFirst, []string{"claude"}},
	}
	for _, c := range cases {
		got, err := a.Command(c.prompt, c.mode)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Command(%q,%q) = %v, %v; want %v", c.prompt, c.mode, got, err, c.want)
		}
	}
	if _, err := a.Command("x", "bogus"); err == nil {
		t.Error("unknown mode should fail")
	}
	if _, err := Get("nope"); err == nil {
		t.Error("unknown agent should fail")
	}
}

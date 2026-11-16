package agent

import (
	"strings"
	"testing"
)

func TestClaudeStreamCommandAsksForTheEventStream(t *testing.T) {
	a, _ := Get("claude")
	s := a.(Streamer)
	got, err := s.StreamCommand("do it", HandsOff, "ref-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--session-id", "ref-1", "--", "do it"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("StreamCommand = %v\nwant %v", got, want)
	}
	// Anything that is not an unattended run with a prompt is the plain command.
	plain, _ := a.Command("x", AskFirst, "")
	if got, _ := s.StreamCommand("x", AskFirst, ""); strings.Join(got, " ") != strings.Join(plain, " ") {
		t.Errorf("an interactive run must not stream: %v", got)
	}
}

func TestClaudeEvents(t *testing.T) {
	a, _ := Get("claude")
	s := a.(Streamer)
	cases := []struct {
		line     string
		progress string
		text     string
		answer   string
		known    bool
	}{
		{`{"type":"system","subtype":"init","session_id":"s"}`, "", "", "", true},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"I will look at the config."}]}}`, "says: I will look at the config.", "I will look at the config.", "", true},
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"lib/main.dart"}}]}}`, "Read lib/main.dart", "", "", true},
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"flutter test"}}]}}`, "Bash flutter test", "", "", true},
		{`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"The user wants\n a plan"}]}}`, "thinking: The user wants a plan", "", "", true},
		{`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`, "", "", "", true},
		{`{"type":"result","subtype":"success","is_error":false,"result":"All done."}`, "", "", "All done.", true},
		{`some plain text the CLI printed`, "", "", "", false},
		{`{"unrelated":true}`, "", "", "", false},
		{`{not json`, "", "", "", false},
	}
	for _, c := range cases {
		ev := s.ParseEvent(c.line)
		if ev.Known != c.known || ev.Progress != c.progress || ev.Text != c.text || ev.Answer != c.answer {
			t.Errorf("ParseEvent(%s) = %+v", c.line, ev)
		}
	}
}

func TestAntigravityStreamCommandAndEvents(t *testing.T) {
	a, _ := Get("antigravity")
	s := a.(Streamer)
	got, _ := s.StreamCommand("-x do it", HandsOff, "")
	want := []string{"agy", "--dangerously-skip-permissions", "--output-format=stream-json", "--print=-x do it"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("StreamCommand = %v, want %v", got, want)
	}
	// The result event is a line agy really printed.
	res := s.ParseEvent(`{"event":"result","result":{"conversation_id":"","status":"ERROR","response":"","error":"authentication failed or timed out","duration_seconds":0,"num_turns":0,"finished_reason":"error"}}`)
	if !res.Known || res.Answer != "" || !strings.HasPrefix(res.Progress, "agent error: authentication failed") {
		t.Errorf("error result = %+v", res)
	}
	ok := s.ParseEvent(`{"event":"result","result":{"status":"OK","response":"The app looks great.","finished_reason":"stop"}}`)
	if !ok.Known || ok.Answer != "The app looks great." || ok.Progress != "" {
		t.Errorf("result = %+v", ok)
	}
	// Other kinds are shown by name with their first text field, whatever it is called.
	for line, want := range map[string]string{
		`{"event":"thinking","text":"Planning the   change\nfirst"}`:        "thinking: Planning the change first",
		`{"event":"tool_call","name":"read_file","args":{"path":"a.dart"}}`: "tool_call: read_file",
		`{"event":"step","step":{"title":"Searching the code"}}`:            "step: Searching the code",
		`{"event":"heartbeat"}`: "heartbeat",
	} {
		if ev := s.ParseEvent(line); !ev.Known || ev.Progress != want || ev.Answer != "" {
			t.Errorf("ParseEvent(%s) = %+v, want progress %q", line, ev, want)
		}
	}
	if ev := s.ParseEvent("Authentication required."); ev.Known {
		t.Error("plain text is not part of the stream")
	}
}

func TestClip(t *testing.T) {
	if got := clip("  a \n b  ", 10); got != "a b" {
		t.Errorf("clip = %q", got)
	}
	if got := clip(strings.Repeat("x", 50), 10); got != strings.Repeat("x", 9)+"…" {
		t.Errorf("clip long = %q", got)
	}
}

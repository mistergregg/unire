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
		// Lines a real Claude Code run printed.
		{`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":{"five_hour":{"utilization":0.34,"status":"allowed"},"seven_day":{"utilization":0.72,"status":"allowed"}}},"session_id":"s"}`, "usage: 34% of the 5-hour limit, 72% of the 7-day limit", "", "", true},
		{`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`, "", "", "", true},
		{`{"type":"some_future_event","x":1}`, "", "", "", true},
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
	// Lines in the format the CLI's headless documentation describes.
	for line, want := range map[string]string{
		`{"event":"init","conversation_id":"c1","init":{"cwd":"/workspace","tools":["run_command"],"permission_mode":"always-proceed"}}`:                                                                                                             "",
		`{"event":"step_update","step_update":{"conversation_id":"c1","step_index":4,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"flutter analyze"},"output":"..."}}}`: "run_command flutter analyze",
		`{"event":"step_update","step_update":{"step_index":5,"state":"ACTIVE","step_type":"tool","tool_name":"view_file","tool_info":{"parameters":{"AbsolutePath":"/workspace/gbr-site/nginx.conf"}}}}`:                                            "view_file /workspace/gbr-site/nginx.conf",
		`{"event":"step_update","step_update":{"step_index":6,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"parameters":{"CommandLine":"false"},"error":{"type":"EXIT","message":"exit status 1"}}}}`:                    "run_command false (failed: exit status 1)",
		`{"event":"step_update","step_update":{"step_index":7,"state":"DONE","step_type":"agent_response","text_delta":"The site is fine."}}`:                                                                                                        "says: The site is fine.",
		`{"event":"step_update","step_update":{"step_index":7,"state":"ACTIVE","step_type":"agent_response","text_delta":"The si"}}`:                                                                                                                 "",
		`{"event":"step_update","step_update":{"step_index":8,"state":"DONE","step_type":"checkpoint","duration_seconds":3.2,"usage":{"total_tokens":10}}}`:                                                                                          "",
		`{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"user_input","text_delta":"the prompt"}}`:                                                                                                                   "",
		`{"event":"step_update","step_update":{"step_index":9,"state":"DONE","subagent_info":{"subagents":[]}}}`:                                                                                                                                     "subagent finished",
		`{"event":"something_new","message":"hello there"}`: "something_new: hello there",
	} {
		if ev := s.ParseEvent(line); !ev.Known || ev.Progress != want || ev.Answer != "" {
			t.Errorf("ParseEvent(%s) = %+v, want progress %q", line, ev, want)
		}
	}
	// A reply that arrives in pieces is kept together by its step number.
	part := s.ParseEvent(`{"event":"step_update","step_update":{"step_index":7,"state":"ACTIVE","step_type":"agent_response","text_delta":"The si"}}`)
	if part.Fragment != "The si" || part.Step != 7 {
		t.Errorf("fragment = %+v", part)
	}
	if st := s.ParseEvent(`{"event":"result","result":{"status":"INTERRUPTED","response":""}}`); st.Progress != "finished with status INTERRUPTED" {
		t.Errorf("interrupted result = %+v", st)
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

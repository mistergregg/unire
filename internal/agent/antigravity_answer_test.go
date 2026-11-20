package agent

import (
	"bytes"
	"testing"
)

func TestFinalAnswer(t *testing.T) {
	joined := "I have launched the tests and will check them.\nI have run them.\nDone: the backup now holds attachments."
	for _, c := range []struct {
		name, response string
		last           []string
		want           string
	}{
		{"the last message ends the response", joined, []string{"Done: the backup now holds attachments."}, "Done: the backup now holds attachments."},
		{"the longest message that ends it wins", joined, []string{"attachments.", "Done: the backup now holds attachments."}, "Done: the backup now holds attachments."},
		{"a message that does not end it is ignored", joined, []string{"I have run them."}, joined},
		{"one message: kept as it is", "All good.", []string{"All good."}, "All good."},
		{"nothing known", joined, []string{"", "  "}, joined},
	} {
		if got := finalAnswer(c.response, c.last...); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A turn in which the agent wrote three messages shows only the last as the answer,
// whether that message arrived whole or in parts.
func TestAntigravityShowsItsLastMessageAsTheAnswer(t *testing.T) {
	result := `{"event":"result","result":{"status":"SUCCESS","response":"Starting the tests now.\nTests pass.\nThe change is done and tested."}}`
	for name, lines := range map[string][]string{
		"whole": {
			`{"event":"step_update","step_update":{"step_index":3,"state":"DONE","step_type":"agent_response","text_delta":"Starting the tests now."}}`,
			`{"event":"step_update","step_update":{"step_index":9,"state":"DONE","step_type":"agent_response","text_delta":"The change is done and tested."}}`,
		},
		"in parts": {
			`{"event":"step_update","step_update":{"step_index":9,"state":"ACTIVE","step_type":"agent_response","text_delta":"The change is done "}}`,
			`{"event":"step_update","step_update":{"step_index":9,"state":"ACTIVE","step_type":"agent_response","text_delta":"and tested."}}`,
		},
		"in parts, then the last part when done": {
			`{"event":"step_update","step_update":{"step_index":9,"state":"ACTIVE","step_type":"agent_response","text_delta":"The change is done "}}`,
			`{"event":"step_update","step_update":{"step_index":9,"state":"DONE","step_type":"agent_response","text_delta":"and tested."}}`,
		},
	} {
		_, drv, _ := antigravity{}.StartLive(LiveOptions{Mode: HandsOff})
		_ = drv.Begin(&bytes.Buffer{}, "go")
		for _, l := range lines {
			drv.Line(l)
		}
		if got := drv.Line(result).Answer; got != "The change is done and tested." {
			t.Errorf("%s: answer = %q", name, got)
		}
	}
	// With nothing to go on, the whole response is the answer.
	_, drv, _ := antigravity{}.StartLive(LiveOptions{Mode: HandsOff})
	_ = drv.Begin(&bytes.Buffer{}, "go")
	if got := drv.Line(result).Answer; got != "Starting the tests now.\nTests pass.\nThe change is done and tested." {
		t.Errorf("answer = %q", got)
	}
}

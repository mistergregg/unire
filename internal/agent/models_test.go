package agent

import (
	"slices"
	"strings"
	"testing"
)

func TestParseAntigravityModelsOutput(t *testing.T) {
	out := "\r\x1b[K⠋ Fetching available models...\r\x1b[K" +
		"gemini-4-hydrogen-high\tGemini 4 Hydrogen (High)\n" +
		"gemini-4-hydrogen-xhigh\tGemini 4 Hydrogen (XHigh)\n" +
		"gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n" +
		"gemini-4-hydrogen-high\tDuplicate\n" +
		"bad model id\tIgnored\n" +
		"claude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n"

	got, err := ParseAntigravityModelsOutput(out)
	if err != nil {
		t.Fatalf("ParseAntigravityModelsOutput: %v", err)
	}
	want := []string{"gemini-4-hydrogen-high", "gemini-4-hydrogen-xhigh", "gemini-3.8-flash-high", "claude-sonnet-4-6"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseAntigravityModelsOutputErrors(t *testing.T) {
	if _, err := ParseAntigravityModelsOutput("\rError: Please sign in to view available models.\n"); err == nil || !strings.Contains(err.Error(), "Please sign in") {
		t.Errorf("want sign-in error, got %v", err)
	}
	if _, err := ParseAntigravityModelsOutput("Fetching available models...\n"); err == nil {
		t.Error("expected error when no models are listed")
	}
}

func TestParseCodexModelsOutput(t *testing.T) {
	out := `{"id":1,"result":{"userAgent":"unire"}}` + "\n" +
		`{"id":2,"result":{"data":[{"id":"gpt-6.1-sol","model":"gpt-6.1-sol","hidden":false},{"id":"secret-hidden","model":"secret-hidden","hidden":true},{"id":"","model":"gpt-6-luna","hidden":false},{"id":"gpt-6.1-sol","model":"gpt-6.1-sol","hidden":false}]}}`
	got, err := ParseCodexModelsOutput(out)
	if err != nil {
		t.Fatalf("ParseCodexModelsOutput: %v", err)
	}
	want := []string{"gpt-6.1-sol", "gpt-6-luna"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseCodexModelsOutputErrors(t *testing.T) {
	if _, err := ParseCodexModelsOutput(`{"id":2,"error":{"message":"not allowed"}}`); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("want error message, got %v", err)
	}
	if _, err := ParseCodexModelsOutput(`{"id":2,"result":{"data":[]}}`); err == nil {
		t.Error("expected error for empty data")
	}
	if _, err := ParseCodexModelsOutput("no json here"); err == nil {
		t.Error("expected error when id=2 is missing")
	}
}

func TestParseClaudeModelsOutput(t *testing.T) {
	out := `{"type":"control_response","response":{"subtype":"success","request_id":"1","response":{"models":[{"value":"default"},{"value":"opus"},{"value":"sonnet"},{"value":"claude-fable-5-1"},{"value":"opus"},{"value":"bad[1m]"}]}}}`
	got, err := ParseClaudeModelsOutput(out)
	if err != nil {
		t.Fatalf("ParseClaudeModelsOutput: %v", err)
	}
	want := []string{"opus", "sonnet", "claude-fable-5-1"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseClaudeModelsOutputErrors(t *testing.T) {
	if _, err := ParseClaudeModelsOutput(`{"type":"control_response","response":{"subtype":"error","error":"boom"}}`); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("want error message, got %v", err)
	}
	if _, err := ParseClaudeModelsOutput(`{"type":"control_response","response":{"subtype":"success","response":{"models":[{"value":"default"}]}}}`); err == nil {
		t.Error("expected error when only default is returned")
	}
	if _, err := ParseClaudeModelsOutput("not json"); err == nil {
		t.Error("expected error when control_response is missing")
	}
}

func TestParseCodexModelsOutputSpaceAndModelsKey(t *testing.T) {
	out := "warning: skipping line\n" + `{"id": 2, "result": {"models": [{"id": "gpt-6-luna"}, {"model": "gpt-6-mini", "hidden": false}]}}` + "\n"
	got, err := ParseCodexModelsOutput(out)
	if err != nil {
		t.Fatalf("ParseCodexModelsOutput: %v", err)
	}
	want := []string{"gpt-6-luna", "gpt-6-mini"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

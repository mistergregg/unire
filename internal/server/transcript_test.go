package server

import (
	"strings"
	"testing"
)

func TestTranscriptKeepsTheMessageAndDropsEverythingSecret(t *testing.T) {
	raw := "\x1b[2J\x1b[H\x1b[1mPlease visit the following URL to authorize the application:\x1b[0m\r\n" +
		"https://accounts.google.com/o/oauth2/v2/auth?client_id=123&state=abcdefabcdefabcdefabcdefabcdefabcdef&code_challenge=XYZ\r\n" +
		"Enter the authorization code: 4/0AeanS0Zq2x9abcDEFghiJKL\r\n" +
		"\x1b[31mFailed to sign in. Message: account someone@example.com requires GOOGLE_CLOUD_PROJECT\x1b[0m\r\n" +
		"Logged in with token ya29.A0ARrdaM-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG\r\n"
	got := cleanTranscript([]byte(raw))

	for _, want := range []string{"Please visit the following URL", "Enter the authorization code", "Failed to sign in. Message:", "requires GOOGLE_CLOUD_PROJECT", "<link to accounts.google.com>"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q:\n%s", want, got)
		}
	}
	for _, secret := range []string{"4/0AeanS0Zq2x9abcDEFghiJKL", "someone@example.com", "client_id=123", "code_challenge", "ya29.A0ARrdaM", "abcdefabcdefabcdefabcdefabcdefabcdef"} {
		if strings.Contains(got, secret) {
			t.Errorf("transcript leaked %q:\n%s", secret, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Error("escape sequences should be gone")
	}
}

func TestTranscriptCollapsesRedrawsAndBorders(t *testing.T) {
	var raw strings.Builder
	for i := 0; i < 20; i++ { // a TUI redraws the same screen over and over
		raw.WriteString("\x1b[2K\x1b[1A│ Get started │\r\n")
		raw.WriteString("Failed to sign in. Message: boom\r\n")
		raw.WriteString("╭──────────────╮\r\n")
	}
	raw.WriteString("progress 10%\rprogress 50%\rprogress 100%\r\n")
	got := cleanTranscript([]byte(raw.String()))
	if strings.Count(got, "Failed to sign in. Message: boom") != 1 {
		t.Errorf("repeated redraws should collapse to one line:\n%s", got)
	}
	if strings.Contains(got, "╭") {
		t.Errorf("border-only lines should be dropped:\n%s", got)
	}
	if !strings.Contains(got, "progress 100%") || strings.Contains(got, "progress 10%") {
		t.Errorf("a carriage return should keep only the final version of a line:\n%s", got)
	}
}

func TestTranscriptKeepsOnlyTheEndOfALongSession(t *testing.T) {
	var tr transcripts
	tr.start("gemini")
	for i := 0; i < 3000; i++ {
		tr.add("gemini", []byte("line number "+strings.Repeat("x", 5)+" "+string(rune('a'+i%26))+"\r\n"))
	}
	tr.add("gemini", []byte("THE LAST LINE\r\n"))
	text, _, ok := tr.get("gemini")
	if !ok || !strings.Contains(text, "THE LAST LINE") || len(text) > 6100 {
		t.Errorf("ok=%v len=%d tail=%q", ok, len(text), text[max(0, len(text)-30):])
	}
	if _, _, ok := tr.get("never-opened"); ok {
		t.Error("an agent that was never signed in has no transcript")
	}
	tr.start("gemini") // a new sign-in replaces the old one
	if text, _, _ := tr.get("gemini"); text != "" {
		t.Errorf("a new sign-in should start empty, got %q", text)
	}
}

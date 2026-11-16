package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestCodexCommands(t *testing.T) {
	a, _ := Get("codex")
	bypass := "--dangerously-bypass-approvals-and-sandbox"
	cases := []struct {
		prompt, mode string
		want         []string
	}{
		{"do it", HandsOff, []string{"codex", "exec", bypass, "--skip-git-repo-check", "--", "do it"}},
		{"--help me", HandsOff, []string{"codex", "exec", bypass, "--skip-git-repo-check", "--", "--help me"}},
		{"", HandsOff, []string{"codex", bypass}},
		{"do it", AskFirst, []string{"codex", "--", "do it"}},
		{"", AskFirst, []string{"codex"}},
	}
	for _, c := range cases {
		// The ref is ignored: Codex chooses its own conversation id.
		got, err := a.Command(c.prompt, c.mode, ref)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Command(%q,%q) = %v, %v; want %v", c.prompt, c.mode, got, err, c.want)
		}
	}
	if _, ok := a.FollowUp("x", HandsOff, ref); ok {
		t.Error("Codex must not resume 'the latest' conversation: sessions share one home folder")
	}
	if a.MemoryMode() != MemoryPrompt {
		t.Error("Codex memory goes in the prompt until AGENTS.md loading is verified")
	}
}

func TestGeminiCommands(t *testing.T) {
	a, _ := Get("gemini")
	cases := []struct {
		prompt, mode string
		want         []string
	}{
		{"do it", HandsOff, []string{"gemini", "--skip-trust", "--session-id", ref, "--approval-mode", "yolo", "--prompt=do it"}},
		{"-x weird", HandsOff, []string{"gemini", "--skip-trust", "--session-id", ref, "--approval-mode", "yolo", "--prompt=-x weird"}},
		{"", HandsOff, []string{"gemini", "--skip-trust", "--session-id", ref, "--approval-mode", "yolo"}},
		{"do it", AskFirst, []string{"gemini", "--skip-trust", "--session-id", ref, "--prompt-interactive=do it"}},
	}
	for _, c := range cases {
		got, err := a.Command(c.prompt, c.mode, ref)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Command(%q,%q) = %v, %v; want %v", c.prompt, c.mode, got, err, c.want)
		}
	}
	if _, err := a.Command("x", "bogus", ref); err == nil {
		t.Error("unknown mode should fail")
	}
	if _, ok := a.FollowUp("x", HandsOff, ref); ok {
		t.Error("Gemini must not resume 'the latest' conversation")
	}
}

func TestAgentsAreCompleteAndSorted(t *testing.T) {
	infos := Infos()
	if len(infos) < 3 {
		t.Fatalf("want claude, codex and gemini, got %d agents", len(infos))
	}
	for i, in := range infos {
		a, err := Get(in.ID)
		if err != nil || a.Info().Label != in.Label {
			t.Errorf("Get(%q) = %v, %v", in.ID, a, err)
		}
		if in.Label == "" || in.Description == "" || in.LoginHelp == "" || len(in.LoginArgv) == 0 {
			t.Errorf("%s: needs a label, description, login help and login command", in.ID)
		}
		if len(in.StatusArgv) > 0 && in.LoggedIn == nil {
			t.Errorf("%s: a status command needs a way to read its result", in.ID)
		}
		if a.MemoryMode() != MemoryFile && a.MemoryMode() != MemoryPrompt {
			t.Errorf("%s: unknown memory mode %q", in.ID, a.MemoryMode())
		}
		if i > 0 && strings.ToLower(infos[i-1].Label) > strings.ToLower(in.Label) {
			t.Errorf("not sorted by label: %s before %s", infos[i-1].Label, in.Label)
		}
	}
}

func TestLoginStatusReaders(t *testing.T) {
	c, _ := Get("claude")
	in := c.Info()
	if !in.LoggedIn("{\n  \"loggedIn\": true,\n  \"authMethod\": \"claude.ai\"\n}", true) {
		t.Error("claude: logged in output not recognised")
	}
	if in.LoggedIn("{\n  \"loggedIn\": false\n}", true) || in.LoggedIn("", false) {
		t.Error("claude: logged out treated as logged in")
	}
	x, _ := Get("codex")
	in = x.Info()
	if in.LoggedIn("Not logged in", true) || in.LoggedIn("", true) || in.LoggedIn("Logged in using ChatGPT", false) {
		t.Error("codex: logged out treated as logged in")
	}
	if !in.LoggedIn("Logged in using ChatGPT", true) {
		t.Error("codex: logged in output not recognised")
	}
	g, _ := Get("gemini")
	in = g.Info()
	if !in.LoggedIn("", true) || in.LoggedIn("", false) {
		t.Error("gemini: the credentials-file check should follow the exit status")
	}
}

func TestUnverifiedAgentsSaySo(t *testing.T) {
	for _, id := range []string{"codex", "gemini"} {
		a, _ := Get(id)
		if v := a.Info().Verified; v.Unattended || v.Hosts {
			t.Errorf("%s: do not claim a real run was checked until one was", id)
		}
	}
	c, _ := Get("claude")
	if v := c.Info().Verified; !v.Unattended || !v.Status || !v.Hosts {
		t.Errorf("claude was verified with real sessions: %+v", v)
	}
}

package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"runtime"
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

func TestGeminiSignInSkipsTheMenuWhereDigitsAreShortcuts(t *testing.T) {
	a, _ := Get("gemini")
	argv := a.Info().LoginArgv
	if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
		t.Fatalf("login argv = %v", argv)
	}
	for _, want := range []string{"oauth-personal", "selectedType", "exec gemini --skip-trust"} {
		if !strings.Contains(argv[2], want) {
			t.Errorf("login script missing %q", want)
		}
	}
	help := a.Info().LoginHelp
	if !strings.Contains(help, "Enter the authorization code") || !strings.Contains(help, "Wait for the prompt before pasting") {
		t.Errorf("the help must say where to paste and when: %q", help)
	}
}

// The settings step runs for real where sh and node exist (CI's Linux runners).
func TestGeminiSettingsStepReplacesAnAPIKeyChoiceAndKeepsOtherSettings(t *testing.T) {
	sh, err1 := exec.LookPath("sh")
	_, err2 := exec.LookPath("node")
	if err1 != nil || err2 != nil || runtime.GOOS == "windows" {
		t.Skip("needs sh and node")
	}
	a, _ := Get("gemini")
	script := strings.TrimSuffix(a.Info().LoginArgv[2], " && exec gemini --skip-trust")
	home := t.TempDir()
	if err := os.MkdirAll(home+"/.gemini", 0o755); err != nil {
		t.Fatal(err)
	}
	before := `{"security":{"auth":{"selectedType":"gemini-api-key"}},"ui":{"theme":"dark"}}`
	if err := os.WriteFile(home+"/.gemini/settings.json", []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // twice: running it again changes nothing
		cmd := exec.Command(sh, "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script failed: %v\n%s", err, out)
		}
	}
	b, _ := os.ReadFile(home + "/.gemini/settings.json")
	var got map[string]map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v\n%s", err, b)
	}
	if got["security"]["auth"].(map[string]any)["selectedType"] != "oauth-personal" || got["ui"]["theme"] != "dark" {
		t.Errorf("settings = %s", b)
	}
	// A first-ever sign-in, with no settings file at all.
	home2 := t.TempDir()
	cmd := exec.Command(sh, "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+home2)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed on a fresh home: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(home2 + "/.gemini/settings.json"); err != nil || !strings.Contains(string(b), "oauth-personal") {
		t.Errorf("fresh home: %s, %v", b, err)
	}
}

func TestEveryAgentCanBeInspectedAndReset(t *testing.T) {
	for _, in := range Infos() {
		if len(in.InspectArgv) == 0 || len(in.ResetArgv) == 0 || in.ResetHelp == "" {
			t.Errorf("%s: needs a way to show its saved state and to reset it, and to say what reset removes", in.ID)
		}
	}
}

// The Gemini scripts run for real where sh exists (CI's Linux runners).
func TestGeminiInspectShowsNoSecretsAndResetClearsTheRightFiles(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	a, _ := Get("gemini")
	home := t.TempDir()
	d := home + "/.gemini"
	os.MkdirAll(d+"/history", 0o755)
	files := map[string]string{
		"settings.json":           `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`,
		"gemini-credentials.json": "SECRET-ENCRYPTED-KEY-MATERIAL",
		"oauth_creds.json":        "SECRET-REFRESH-TOKEN",
		"state.json":              "{}",
		"installation_id":         "id",
	}
	for n, c := range files {
		os.WriteFile(d+"/"+n, []byte(c), 0o644)
	}
	os.WriteFile(d+"/history/keep.txt", []byte("history"), 0o644)
	run := func(argv []string) string {
		cmd := exec.Command(sh, argv[1:]...)
		cmd.Env = append(os.Environ(), "HOME="+home, "GEMINI_API_KEY=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v failed: %v\n%s", argv[:2], err, out)
		}
		return string(out)
	}

	got := run(a.Info().InspectArgv)
	for _, want := range []string{"gemini-credentials.json", "oauth_creds.json", "settings.json", "gemini-api-key", "API key variables set in this sandbox: 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("inspect output missing %q:\n%s", want, got)
		}
	}
	for _, secret := range []string{"SECRET-ENCRYPTED-KEY-MATERIAL", "SECRET-REFRESH-TOKEN"} {
		if strings.Contains(got, secret) {
			t.Errorf("inspect must never print a credential file's contents: leaked %q", secret)
		}
	}

	run(a.Info().ResetArgv)
	for _, gone := range []string{"settings.json", "gemini-credentials.json", "oauth_creds.json", "state.json"} {
		if _, err := os.Stat(d + "/" + gone); err == nil {
			t.Errorf("reset should remove %s", gone)
		}
	}
	for _, kept := range []string{"installation_id", "history/keep.txt"} {
		if _, err := os.Stat(d + "/" + kept); err != nil {
			t.Errorf("reset must keep %s", kept)
		}
	}
	run(a.Info().ResetArgv) // resetting again is fine
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

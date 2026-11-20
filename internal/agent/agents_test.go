package agent

import (
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
	// With a model chosen.
	withModel := a.WithModel("gpt-6-luna")
	modelCases := []struct {
		prompt, mode string
		want         []string
	}{
		{"do it", HandsOff, []string{"codex", "exec", bypass, "--skip-git-repo-check", "--model", "gpt-6-luna", "--", "do it"}},
		{"", HandsOff, []string{"codex", bypass, "--model", "gpt-6-luna"}},
		{"do it", AskFirst, []string{"codex", "--model", "gpt-6-luna", "--", "do it"}},
		{"", AskFirst, []string{"codex", "--model", "gpt-6-luna"}},
	}
	for _, c := range modelCases {
		got, err := withModel.Command(c.prompt, c.mode, ref)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("WithModel.Command(%q,%q) = %v, %v; want %v", c.prompt, c.mode, got, err, c.want)
		}
	}
	if _, ok := a.FollowUp("x", HandsOff, ref); ok {
		t.Error("Codex must not resume 'the latest' conversation: sessions share one home folder")
	}
	if a.MemoryMode() != MemoryPrompt {
		t.Error("Codex memory goes in the prompt until AGENTS.md loading is verified")
	}
}

func TestEveryAgentCanBeInspectedAndReset(t *testing.T) {
	for _, in := range Infos() {
		if len(in.InspectArgv) == 0 || len(in.ResetArgv) == 0 || in.ResetHelp == "" {
			t.Errorf("%s: needs a way to show its saved state and to reset it, and to say what reset removes", in.ID)
		}
	}
}

func TestAntigravityCommands(t *testing.T) {
	a, _ := Get("antigravity")
	cases := []struct {
		prompt, mode string
		want         []string
	}{
		{"do it", HandsOff, []string{"agy", "--dangerously-skip-permissions", "--print=do it"}},
		// `-p "--version"` and `-p -- "--version"` both ran as flags in the real CLI.
		{"--version", HandsOff, []string{"agy", "--dangerously-skip-permissions", "--print=--version"}},
		{"-h please\nsecond line", HandsOff, []string{"agy", "--dangerously-skip-permissions", "--print=-h please\nsecond line"}},
		{"", HandsOff, []string{"agy", "--dangerously-skip-permissions"}},
		{"do it", AskFirst, []string{"agy", "--prompt-interactive=do it"}},
		{"", AskFirst, []string{"agy"}},
	}
	for _, c := range cases {
		got, err := a.Command(c.prompt, c.mode, ref)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Command(%q,%q) = %v, %v; want %v", c.prompt, c.mode, got, err, c.want)
		}
	}
	// With a model chosen.
	withModel := a.WithModel("gemini-3.8-flash-high")
	modelCases := []struct {
		prompt, mode string
		want         []string
	}{
		{"do it", HandsOff, []string{"agy", "--dangerously-skip-permissions", "--model", "gemini-3.8-flash-high", "--print=do it"}},
		{"", HandsOff, []string{"agy", "--dangerously-skip-permissions", "--model", "gemini-3.8-flash-high"}},
		{"do it", AskFirst, []string{"agy", "--model", "gemini-3.8-flash-high", "--prompt-interactive=do it"}},
		{"", AskFirst, []string{"agy", "--model", "gemini-3.8-flash-high"}},
	}
	for _, c := range modelCases {
		got, err := withModel.Command(c.prompt, c.mode, ref)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("WithModel.Command(%q,%q) = %v, %v; want %v", c.prompt, c.mode, got, err, c.want)
		}
	}
	// StreamCommand with model.
	streamer := withModel.(Streamer)
	sgot, err := streamer.StreamCommand("do it", HandsOff, ref)
	if err != nil {
		t.Fatal(err)
	}
	swant := []string{"agy", "--dangerously-skip-permissions", "--output-format=stream-json", "--model", "gemini-3.8-flash-high", "--print=do it"}
	if !reflect.DeepEqual(sgot, swant) {
		t.Errorf("StreamCommand = %v; want %v", sgot, swant)
	}
	for _, c := range cases {
		got, _ := a.Command(c.prompt, c.mode, ref)
		for _, arg := range got[1:] {
			if arg == c.prompt && c.prompt != "" && strings.HasPrefix(c.prompt, "-") {
				t.Errorf("a dash-leading prompt must never be a separate argument: %v", got)
			}
		}
	}
	if _, err := a.Command("x", "bogus", ref); err == nil {
		t.Error("unknown mode should fail")
	}
	if _, ok := a.FollowUp("x", HandsOff, ref); ok {
		t.Error("agy must not resume 'the most recent' conversation: sessions share one home folder")
	}
	if len(a.Info().StatusArgv) == 0 || a.Info().Verified.Status {
		t.Error("agy's sign-in check looks for its saved login file, which has not been confirmed with a real sign-in, so it must be there and must not claim to be verified")
	}
}

// The sign-in check runs for real where sh and GNU find exist (CI's Linux runners).
func TestAntigravitySignInCheckFollowsItsSavedLogin(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	if out, err := exec.Command("find", "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), "GNU") {
		t.Skip("needs GNU find")
	}
	a, _ := Get("antigravity")
	check := func(home string) bool {
		cmd := exec.Command(sh, a.Info().StatusArgv[1:]...)
		cmd.Env = append(os.Environ(), "HOME="+home)
		return cmd.Run() == nil
	}
	home := t.TempDir()
	if check(home) {
		t.Error("an empty home is not signed in")
	}
	os.MkdirAll(home+"/.gemini/antigravity-cli/builtin/skills", 0o755)
	os.WriteFile(home+"/.gemini/antigravity-cli/builtin/skills/token-guide.md", []byte("x"), 0o644)
	os.WriteFile(home+"/.gemini/antigravity-cli/settings.json", []byte("{}"), 0o644)
	os.MkdirAll(home+"/.gemini/antigravity-cli/cache", 0o755)
	os.WriteFile(home+"/.gemini/antigravity-cli/cache/projects.json", []byte("x"), 0o644)
	if check(home) {
		t.Error("built-in files, settings and cache are not a login")
	}
	// Another agent's login must not count.
	os.MkdirAll(home+"/.codex", 0o755)
	os.WriteFile(home+"/.codex/auth.json", []byte("x"), 0o644)
	if check(home) {
		t.Error("another agent's auth.json is not Antigravity's login")
	}
	os.WriteFile(home+"/.gemini/antigravity-cli/auth.json", []byte(""), 0o644)
	if check(home) {
		t.Error("an empty file is not a login")
	}
	os.WriteFile(home+"/.gemini/antigravity-cli/auth.json", []byte(`{"x":1}`), 0o644)
	if !check(home) {
		t.Error("a saved auth.json should count as signed in")
	}
}

func TestAntigravityInspectAndResetOnlyTouchTheirOwnFiles(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	if out, err := exec.Command("find", "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), "GNU") {
		t.Skip("needs GNU find")
	}
	a, _ := Get("antigravity")
	home := t.TempDir()
	write := func(p, c string) {
		os.MkdirAll(home+"/"+p[:strings.LastIndex(p, "/")], 0o755)
		os.WriteFile(home+"/"+p, []byte(c), 0o644)
	}
	write(".gemini/antigravity-cli/settings.json", "SECRET-SETTINGS")
	write(".local/share/keyrings/login.keyring", "SECRET-KEYRING")
	write(".config/antigravity/state", "x")
	write(".claude/.credentials.json", "CLAUDE-LOGIN-MUST-SURVIVE")
	write(".gemini/gemini-credentials.json", "GEMINI-KEY-MUST-SURVIVE")
	write(".codex/auth.json", "CODEX-LOGIN-MUST-SURVIVE")
	run := func(argv []string) string {
		cmd := exec.Command(sh, argv[1:]...)
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v failed: %v\n%s", argv[:2], err, out)
		}
		return string(out)
	}
	got := run(a.Info().InspectArgv)
	if !strings.Contains(got, "antigravity-cli/settings.json") || !strings.Contains(got, "login.keyring") {
		t.Errorf("inspect should list the files:\n%s", got)
	}
	for _, secret := range []string{"SECRET-SETTINGS", "SECRET-KEYRING", "MUST-SURVIVE"} {
		if strings.Contains(got, secret) {
			t.Errorf("inspect must print names and sizes only, leaked %q", secret)
		}
	}
	if strings.Contains(got, ".claude") || strings.Contains(got, ".codex") || strings.Contains(got, "gemini-credentials") {
		t.Errorf("inspect must not list other agents' files:\n%s", got)
	}
	run(a.Info().ResetArgv)
	for _, gone := range []string{".gemini/antigravity-cli", ".local/share/keyrings", ".config/antigravity"} {
		if _, err := os.Stat(home + "/" + gone); err == nil {
			t.Errorf("reset should remove %s", gone)
		}
	}
	for _, kept := range []string{".claude/.credentials.json", ".gemini/gemini-credentials.json", ".codex/auth.json"} {
		if _, err := os.Stat(home + "/" + kept); err != nil {
			t.Errorf("resetting Antigravity must never touch another agent's login: %s was removed", kept)
		}
	}
}

func TestAgentsAreCompleteAndSorted(t *testing.T) {
	infos := Infos()
	if len(infos) < 3 {
		t.Fatalf("want claude, codex and antigravity, got %d agents", len(infos))
	}
	for i, in := range infos {
		a, err := Get(in.ID)
		if err != nil || a.Info().Label != in.Label {
			t.Errorf("Get(%q) = %v, %v", in.ID, a, err)
		}
		if in.Label == "" || in.Description == "" || in.LoginHelp == "" || len(in.LoginArgv) == 0 {
			t.Errorf("%s: needs a label, description, login help and login command", in.ID)
		}
		if len(in.Models) == 0 {
			t.Errorf("%s: should list known model names", in.ID)
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
	g, _ := Get("antigravity")
	in = g.Info()
	if !in.LoggedIn("", true) || in.LoggedIn("", false) {
		t.Error("antigravity: the sign-in file check should follow the exit status")
	}
}

func TestUnverifiedAgentsSaySo(t *testing.T) {
	if a, _ := Get("codex"); a.Info().Verified.Unattended || a.Info().Verified.Hosts {
		t.Errorf("codex: do not claim a real run was checked until one was")
	}
	if a, _ := Get("antigravity"); !a.Info().Verified.Unattended || a.Info().Verified.Hosts || a.Info().Verified.Status {
		t.Errorf("antigravity: one unattended run was seen; hosts and the sign-in check are not verified: %+v", a.Info().Verified)
	}
	c, _ := Get("claude")
	if v := c.Info().Verified; !v.Unattended || !v.Status || !v.Hosts {
		t.Errorf("claude was verified with real sessions: %+v", v)
	}
}

func TestModelValidation(t *testing.T) {
	valid := []string{
		"",
		"sonnet",
		"opus",
		"fable",
		"gpt-6-luna",
		"gemini-3.8-flash-high",
		"claude-sonnet-4-6",
		"meta-llama/Llama-3:8b",
		"provider.name/model-v1:tag",
		"a",
		strings.Repeat("a", 64),
	}
	for _, m := range valid {
		if err := ValidateModel(m); err != nil {
			t.Errorf("ValidateModel(%q) = %v; want nil", m, err)
		}
	}

	invalid := []string{
		strings.Repeat("a", 65), // too long
		"has space",
		"has\ttab",
		"has\nnewline",
		"-starts-with-dash",
		".starts-with-dot",
		":starts-with-colon",
		"/starts-with-slash",
		"model;rm",
		"model&echo",
		"model|cat",
		"model$HOME",
		"model`id`",
		"model'quote",
		"model\"quote",
		"model\\backslash",
		"model(paren)",
		"model<angle>",
	}
	for _, m := range invalid {
		if err := ValidateModel(m); err == nil {
			t.Errorf("ValidateModel(%q) = nil; want error", m)
		}
	}
}

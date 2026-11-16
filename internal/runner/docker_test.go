package runner

import (
	"reflect"
	"strings"
	"testing"
)

func TestRunArgs(t *testing.T) {
	d := Docker{Image: "img", CredsVolume: "creds"}
	got := d.runArgs(StartSpec{
		SessionID: "ab12", Memory: "2g",
		Mounts: []Mount{{HostPath: "/h/api", ContainerPath: "/remotes/api"}},
	})
	want := []string{"run", "-d", "--name", "unire-ab12", "--hostname", "unire-sandbox", "--label", "unire.session=ab12",
		"-v", "creds:/home/node", "-w", "/workspace",
		"--cap-drop", "NET_RAW", "--security-opt", "no-new-privileges", "--memory", "2g",
		"-v", "/h/api:/remotes/api", "img", "sleep", "infinity"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runArgs =\n%v\nwant\n%v", got, want)
	}
}

func TestRunArgsNetworkAndEnv(t *testing.T) {
	d := Docker{Image: "img", CredsVolume: "creds"}
	got := d.runArgs(StartSpec{SessionID: "x", Network: "unire-sandbox", Env: map[string]string{"HTTPS_PROXY": "http://p:3128", "A": "1"}})
	joined := strings.Join(got, " ")
	for _, want := range []string{"--network unire-sandbox", "-e A=1", "-e HTTPS_PROXY=http://p:3128"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if strings.Index(joined, "-e A=1") > strings.Index(joined, "-e HTTPS_PROXY") {
		t.Error("env should be in a stable order")
	}
	if strings.Contains(strings.Join(d.runArgs(StartSpec{SessionID: "x"}), " "), "--network") {
		t.Error("no network flag when none is requested")
	}
}

// A login saved by one container must be readable by the next, and some agent
// CLIs encrypt it with a key derived from the hostname.
func TestEveryContainerShareOneHostname(t *testing.T) {
	d := Docker{Image: "img", CredsVolume: "creds"}
	cases := map[string][]string{
		"session":        d.runArgs(StartSpec{SessionID: "x"}),
		"one-shot":       d.oneShotArgs(),
		"login terminal": d.LoginTerminalArgs("n", []string{"x"}),
	}
	for name, args := range cases {
		joined := " " + strings.Join(args, " ") + " "
		if !strings.Contains(joined, " --hostname unire-sandbox ") {
			t.Errorf("%s does not set the shared hostname: %v", name, args)
		}
	}
}

func TestOneShotHasNoNetworkAndOnlyTheLoginVolume(t *testing.T) {
	d := Docker{Image: "img", CredsVolume: "creds"}
	got := strings.Join(append(d.oneShotArgs(), "claude", "auth", "status"), " ")
	for _, want := range []string{"--rm", "--network none", "-v creds:/home/node", "img claude auth status"} {
		if !strings.Contains(got, want) {
			t.Errorf("one-shot args missing %q: %s", want, got)
		}
	}
	if strings.Count(got, " -v ") != 1 {
		t.Errorf("only the login volume may be mounted: %s", got)
	}
}

func TestLoginTerminalMountsOnlyTheLoginVolumeAndIsLabelled(t *testing.T) {
	d := Docker{Image: "img", CredsVolume: "creds"}
	got := d.LoginTerminalArgs("unire-login-ab", []string{"codex", "login", "--device-auth"})
	joined := strings.Join(got, " ")
	for _, want := range []string{"run -it --rm", "--name unire-login-ab", "--label unire.login=1", "--cap-drop NET_RAW",
		"-v creds:/home/node", "img codex login --device-auth"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q: %s", want, joined)
		}
	}
	if strings.Count(joined, " -v ") != 1 || strings.Contains(joined, "docker.sock") {
		t.Errorf("a login terminal must not get repos, tokens or the Docker socket: %s", joined)
	}
}

func TestExecArgsHidesEnvValues(t *testing.T) {
	got := execArgs("c", ExecOpts{
		Argv: []string{"git", "push"}, Dir: "/workspace/api",
		Env: map[string]string{"UNIRE_GIT_TOKEN": "secret-value"},
	})
	want := []string{"exec", "-w", "/workspace/api", "-e", "UNIRE_GIT_TOKEN", "c", "git", "push"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("execArgs = %v, want %v", got, want)
	}
}

func TestExecArgsTTY(t *testing.T) {
	got := execArgs("c", ExecOpts{Argv: []string{"claude"}, TTY: true})
	want := []string{"exec", "-i", "-t", "c", "claude"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("execArgs = %v, want %v", got, want)
	}
}

func TestAWorkspaceVolumeIsMountedWhenNamed(t *testing.T) {
	d := Docker{Image: "img", CredsVolume: "creds"}
	has := func(args []string, a, b string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == a && args[i+1] == b {
				return true
			}
		}
		return false
	}
	if has(d.runArgs(StartSpec{SessionID: "s1"}), "-v", "unire-ws-x:/workspace") {
		t.Error("no workspace named, so none is mounted")
	}
	if !has(d.runArgs(StartSpec{SessionID: "s1", Workspace: "unire-ws-x"}), "-v", "unire-ws-x:/workspace") {
		t.Error("a named workspace is mounted at /workspace")
	}
}

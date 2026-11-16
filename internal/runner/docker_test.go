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
	want := []string{"run", "-d", "--name", "unire-ab12", "--label", "unire.session=ab12",
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

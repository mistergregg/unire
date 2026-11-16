package runner

import (
	"reflect"
	"testing"
)

func TestRunArgs(t *testing.T) {
	d := Docker{Image: "img", CredsVolume: "creds"}
	got := d.runArgs(StartSpec{
		SessionID: "ab12", Memory: "2g",
		Mounts: []Mount{{HostPath: "/h/api", ContainerPath: "/remotes/api"}},
	})
	want := []string{"run", "-d", "--name", "unire-ab12", "--label", "unire.session=ab12",
		"-v", "creds:/home/node", "-w", "/workspace", "--memory", "2g",
		"-v", "/h/api:/remotes/api", "img", "sleep", "infinity"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runArgs =\n%v\nwant\n%v", got, want)
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

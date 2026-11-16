package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/runner"
)

// fakeBox stands in for Docker. It records every command and lets a test
// choose what the agent prints and whether it blocks.
type fakeBox struct {
	mu    sync.Mutex
	calls []runner.ExecOpts
	out   string
	block chan struct{}
}

type fakeRunner struct{ box *fakeBox }

func (r fakeRunner) Start(context.Context, runner.StartSpec) (runner.Sandbox, error) {
	return r.box, nil
}

func (b *fakeBox) Exec(ctx context.Context, o runner.ExecOpts) error {
	b.mu.Lock()
	b.calls = append(b.calls, o)
	out, block := b.out, b.block
	b.mu.Unlock()
	if len(o.Argv) > 0 && o.Argv[0] == "git" {
		for _, a := range o.Argv {
			if a == "rev-parse" {
				io.WriteString(o.Stdout, "abc\n")
			}
		}
		return nil
	}
	io.WriteString(o.Stdout, out)
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (b *fakeBox) Stop(context.Context) error { return nil }

func (b *fakeBox) snapshot() []runner.ExecOpts {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]runner.ExecOpts(nil), b.calls...)
}

// withServer sets up an owner and one fake Gitea server, and returns the
// server's host URL.
func withServer(t *testing.T, e *env, token string) string {
	t.Helper()
	gitea := fakeGitea(t, token)
	e.setup()
	code, body := e.do("POST", "/api/servers", map[string]any{"name": "G", "type": "gitea", "baseUrl": gitea.URL, "token": token})
	if code != http.StatusCreated {
		t.Fatalf("add server = %d %s", code, body)
	}
	return gitea.URL
}

func repoRef(base, fullName string) map[string]any {
	return map[string]any{"serverId": "g", "fullName": fullName, "cloneUrl": base + "/" + fullName + ".git"}
}

func (e *env) waitStatus(id string, want ...string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := e.do("GET", "/api/sessions/"+id+"/logs", nil)
		var res map[string]any
		json.Unmarshal([]byte(body), &res)
		for _, w := range want {
			if res["status"] == w {
				return res
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("session %s never reached %v; last: %s", id, want, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) launch(base string, extra map[string]any) (int, map[string]any) {
	body := map[string]any{"task": "Add login", "agent": "claude", "prompt": "build it", "repos": []any{repoRef(base, "greg/api")}}
	for k, v := range extra {
		body[k] = v
	}
	code, raw := e.do("POST", "/api/sessions", body)
	var res map[string]any
	json.Unmarshal([]byte(raw), &res)
	return code, res
}

func TestLaunchSessionRunsAndLogsWithoutLeakingTokens(t *testing.T) {
	e := newEnv(t)
	const token = "tok-very-secret-123"
	base := withServer(t, e, token)
	e.box.out = "agent says hello " + token + "\n"

	code, sess := e.launch(base, nil)
	if code != http.StatusCreated || sess["branch"] != "agent/claude/add-login" {
		t.Fatalf("launch = %d %v", code, sess)
	}
	id := sess["id"].(string)
	res := e.waitStatus(id, "done")
	if res["finished"] != true {
		t.Errorf("finished flag = %v", res["finished"])
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), "agent says hello ***") {
		t.Errorf("log missing scrubbed agent output: %s", raw)
	}
	if strings.Contains(string(raw), token) || strings.Contains(e.seen.String(), token) {
		t.Error("the token leaked into an API response")
	}

	// The token reaches git for that host, and never the agent.
	sawClone, sawAgent := false, false
	for _, c := range e.box.snapshot() {
		if len(c.Argv) > 0 && c.Argv[0] == "claude" {
			sawAgent = true
			if len(c.Env) != 0 {
				t.Errorf("agent got env: %v", c.Env)
			}
		}
		if len(c.Argv) > 1 && c.Argv[0] == "git" && strings.Contains(strings.Join(c.Argv, " "), "clone") {
			sawClone = true
			if c.Env["UNIRE_GIT_TOKEN"] != token {
				t.Error("clone did not get the server's token")
			}
		}
	}
	if !sawClone || !sawAgent {
		t.Errorf("clone=%v agent=%v", sawClone, sawAgent)
	}

	if code, _ := e.do("DELETE", "/api/sessions/"+id, nil); code != 200 {
		t.Errorf("delete finished session = %d", code)
	}
}

func TestCloneURLMustStayOnTheServersHost(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	host := strings.TrimPrefix(base, "http://")
	bad := map[string]string{
		"other host":  "https://evil.example.com/greg/api.git",
		"userinfo":    "http://user:pass@" + host + "/greg/api.git",
		"ssh":         "ssh://" + host + "/greg/api.git",
		"local path":  "/etc",
		"file scheme": "file:///etc",
	}
	for name, cloneURL := range bad {
		ref := map[string]any{"serverId": "g", "fullName": "greg/api", "cloneUrl": cloneURL}
		if code, res := e.launch(base, map[string]any{"repos": []any{ref}}); code != http.StatusBadRequest {
			t.Errorf("%s accepted: %d %v", name, code, res)
		}
	}
	// Option-looking branch names are refused too.
	ref := repoRef(base, "greg/api")
	ref["base"] = "--upload-pack=touch /tmp/x"
	if code, _ := e.launch(base, map[string]any{"repos": []any{ref}}); code != http.StatusBadRequest {
		t.Errorf("hostile base branch accepted: %d", code)
	}
	if len(e.box.snapshot()) != 0 {
		t.Error("nothing should have run for rejected requests")
	}
}

func TestLaunchValidation(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	cases := map[string]map[string]any{
		"no prompt":     {"prompt": "  "},
		"no task":       {"task": ""},
		"unknown agent": {"agent": "nope"},
		"no repos":      {"repos": []any{}},
		"unknown srv":   {"repos": []any{map[string]any{"serverId": "zzz", "fullName": "a/b", "cloneUrl": base + "/a/b.git"}}},
	}
	for name, extra := range cases {
		if code, _ := e.launch(base, extra); code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, code)
		}
	}
	if code, _ := e.do("POST", "/api/sessions", map[string]any{}, true); code != http.StatusForbidden {
		t.Errorf("no CSRF header = %d", code)
	}
}

func TestWorkspaces(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	ws := map[string]any{"name": "My App", "repos": []any{repoRef(base, "greg/api"), repoRef(base, "greg/web")}}
	if code, body := e.do("POST", "/api/workspaces", ws); code != 200 {
		t.Fatalf("save = %d %s", code, body)
	}
	_, body := e.do("GET", "/api/workspaces", nil)
	if !strings.Contains(body, `"id":"my-app"`) || !strings.Contains(body, "greg/web") {
		t.Errorf("list = %s", body)
	}
	bad := map[string]any{"name": "Bad", "repos": []any{map[string]any{"serverId": "g", "fullName": "x/y", "cloneUrl": "https://evil/x/y.git"}}}
	if code, _ := e.do("POST", "/api/workspaces", bad); code != http.StatusBadRequest {
		t.Errorf("workspace with a foreign clone URL = %d, want 400", code)
	}
	if code, _ := e.do("DELETE", "/api/workspaces/my-app", nil); code != 200 {
		t.Errorf("delete = %d", code)
	}
	if code, _ := e.do("DELETE", "/api/workspaces/my-app", nil); code != http.StatusNotFound {
		t.Errorf("second delete = %d", code)
	}
}

func TestStopAndInterrupted(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.box.block = make(chan struct{})

	_, a := e.launch(base, nil)
	idA := a["id"].(string)
	e.waitStatus(idA, "running")
	if code, _ := e.do("DELETE", "/api/sessions/"+idA, nil); code != http.StatusConflict {
		t.Errorf("deleting a running session = %d, want 409", code)
	}
	if code, _ := e.do("POST", "/api/sessions/"+idA+"/stop", nil); code != 200 {
		t.Fatalf("stop = %d", code)
	}
	e.waitStatus(idA, "stopped")
	if code, _ := e.do("POST", "/api/sessions/"+idA+"/stop", nil); code != http.StatusConflict {
		t.Errorf("stopping a finished session = %d, want 409", code)
	}

	// A session still running when Unire restarts is marked interrupted.
	_, b := e.launch(base, nil)
	idB := b["id"].(string)
	e.waitStatus(idB, "running")
	e.restart()
	res := e.waitStatus(idB, "interrupted")
	if res["finished"] != true {
		t.Error("interrupted sessions count as finished")
	}
}

func TestSandboxSettings(t *testing.T) {
	e := newEnv(t)
	e.setup()
	_, body := e.do("GET", "/api/sandbox", nil)
	if !strings.Contains(body, "unire-sandbox") || !strings.Contains(body, "docker run -it --rm -v unire-creds-claude:/home/node") {
		t.Errorf("defaults = %s", body)
	}
	for name, sb := range map[string]map[string]string{
		"flag as image": {"image": "--privileged", "credsVolume": "v"},
		"space in vol":  {"image": "img", "credsVolume": "a b"},
		"bad memory":    {"image": "img", "credsVolume": "v", "memory": "lots"},
		"bad cpus":      {"image": "img", "credsVolume": "v", "cpus": "-1"},
	} {
		if code, _ := e.do("PUT", "/api/sandbox", sb); code != http.StatusBadRequest {
			t.Errorf("%s accepted: %d", name, code)
		}
	}
	ok := map[string]string{"image": "my/img:1", "credsVolume": "my-creds", "memory": "4g", "cpus": "1.5"}
	if code, body := e.do("PUT", "/api/sandbox", ok); code != 200 || !strings.Contains(body, "-v my-creds:/home/node my/img:1 claude") {
		t.Errorf("save = %d %s", code, body)
	}
	e.restart()
	if _, body := e.do("GET", "/api/sandbox", nil); !strings.Contains(body, `"memory":"4g"`) {
		t.Errorf("settings lost on restart: %s", body)
	}
}

func TestSandboxImageDefaultComesFromEnvironment(t *testing.T) {
	t.Setenv("UNIRE_SANDBOX_IMAGE", "ghcr.io/example/unire-sandbox:latest")
	e := newEnv(t)
	e.setup()
	if _, body := e.do("GET", "/api/sandbox", nil); !strings.Contains(body, `"image":"ghcr.io/example/unire-sandbox:latest"`) {
		t.Errorf("env default ignored: %s", body)
	}
	// A saved value wins over the environment.
	e.do("PUT", "/api/sandbox", map[string]string{"image": "mine", "credsVolume": "v"})
	if _, body := e.do("GET", "/api/sandbox", nil); !strings.Contains(body, `"image":"mine"`) {
		t.Errorf("saved image not used: %s", body)
	}
}

func TestAgentsListed(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if _, body := e.do("GET", "/api/agents", nil); !strings.Contains(body, "claude") {
		t.Errorf("agents = %s", body)
	}
	if _, err := url.Parse(e.ts.URL); err != nil {
		t.Fatal(err)
	}
}

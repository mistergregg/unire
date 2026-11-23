package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistergregg/unire/internal/agent/agenttest"
	"github.com/mistergregg/unire/internal/egress"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/store"
)

// fakeBox stands in for Docker. It records every command and lets a test
// choose what the agent prints and whether it blocks.
type fakeBox struct {
	mu       sync.Mutex
	calls    []runner.ExecOpts
	out      string
	block    chan struct{}
	outFiles map[string]string // files the agent "writes" in the memory step
	prompts  []string          // what a steerable agent was told (its first message and later ones)
	files    map[string][]byte // files written into the sandbox, by path
	unready  []string          // attachments a message named before they were in the sandbox
	lostConv bool              // a resumed Claude says its conversation is gone
	heads    int               // rev-parse calls: the first of each pair is the start HEAD, the second differs (the agent committed)

	status         map[string]statusResult // answers to sign-in questions, by command name
	statusAsked    []string
	usageOut       string // what the usage command prints (RunOnline)
	usageAsked     int
	claudeUsageOut string // what `claude -p /usage` prints (RunOnline)
	claudeAsked    int
	agyUsageOut    string // what `agy --print=/usage` prints (RunOnline)
	agyAsked       int
}

type fakeRunner struct{ box *fakeBox }

func (r fakeRunner) Start(context.Context, runner.StartSpec) (runner.Sandbox, error) {
	return r.box, nil
}

func (b *fakeBox) Exec(ctx context.Context, o runner.ExecOpts) error {
	if inner, isAgent, isSignal := agenttest.Unwrap(o.Argv); isSignal {
		return nil
	} else if isAgent {
		b.mu.Lock()
		b.calls = append(b.calls, o)
		out, block, lost := b.out, b.block, b.lostConv
		b.mu.Unlock()
		if lost && inner[0] == "claude" && slices.Contains(inner, "--resume") {
			io.WriteString(o.Stderr, "No conversation found with session ID: x\n")
			return errors.New("exit status 1")
		}
		if inner[0] != "claude" { // another agent: it prints its output and ignores its input
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
		return agenttest.Claude(ctx, o.Stdin, o.Stdout,
			func(agenttest.User) string { return out },
			func(ctx context.Context, _ agenttest.User) {
				if block != nil {
					select {
					case <-block:
					case <-ctx.Done():
					}
				}
			},
			func(u agenttest.User) {
				b.mu.Lock()
				b.prompts = append(b.prompts, u.Text)
				for _, p := range attachedPaths.FindAllString(u.Text, -1) {
					if _, ok := b.files[p]; !ok {
						b.unready = append(b.unready, p)
					}
				}
				b.mu.Unlock()
			})
	}
	b.mu.Lock()
	b.calls = append(b.calls, o)
	out, block := b.out, b.block
	b.mu.Unlock()
	if len(o.Argv) > 0 {
		switch o.Argv[0] {
		case "git":
			for _, a := range o.Argv {
				if a == "rev-parse" {
					b.mu.Lock()
					b.heads++
					n := b.heads
					b.mu.Unlock()
					if n%2 == 1 {
						io.WriteString(o.Stdout, "aaa\n")
					} else {
						io.WriteString(o.Stdout, "bbb\n")
					}
				}
			}
			if slices.Contains(o.Argv, "diff") && slices.Contains(o.Argv, "--quiet") {
				return errors.New("exit status 1") // there are differences
			}
			return nil
		case "ls":
			return fmt.Errorf("exit status 2") // nothing was cloned or asked yet
		case "mkdir", "rm", "sh":
			if o.Stdin != nil {
				data, _ := io.ReadAll(o.Stdin)
				b.mu.Lock()
				if b.files == nil {
					b.files = map[string][]byte{}
				}
				b.files[o.Argv[len(o.Argv)-1]] = data
				b.mu.Unlock()
			}
			return nil
		case "head":
			b.mu.Lock()
			c, ok := b.outFiles[o.Argv[len(o.Argv)-1]]
			b.mu.Unlock()
			if !ok {
				return fmt.Errorf("exit status 1")
			}
			io.WriteString(o.Stdout, c)
			return nil
		}
		for _, a := range o.Argv {
			if a == "--resume" {
				return nil
			}
		}
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

// Address makes the fake look like a Docker sandbox on the protected network.
func (b *fakeBox) Address(context.Context, string) (string, error) { return "172.30.0.7", nil }

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
				if res["finished"] == true {
					// A finished session may still be saving its memory in the background.
					e.waitMemorySaved(id)
					_, body = e.do("GET", "/api/sessions/"+id+"/logs", nil)
					json.Unmarshal([]byte(body), &res)
				}
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
		"task too long": {"task": strings.Repeat("x", 81)},
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

func TestNetworkSettings(t *testing.T) {
	e := newEnv(t)
	e.setup()
	_, body := e.do("GET", "/api/network", nil)
	if !strings.Contains(body, `"mode":"internet"`) || !strings.Contains(body, `"available":true`) || !strings.Contains(body, "Full access") {
		t.Errorf("defaults = %s", body)
	}
	for name, req := range map[string]map[string]any{
		"unknown mode": {"mode": "wide-open", "allow": []string{}},
		"bad entry":    {"mode": "internet", "allow": []string{"not a host!"}},
		"bad port":     {"mode": "internet", "allow": []string{"10.0.0.1:99999"}},
	} {
		if code, _ := e.do("PUT", "/api/network", req); code != http.StatusBadRequest {
			t.Errorf("%s accepted: %d", name, code)
		}
	}
	ok := map[string]any{"mode": "agent-only", "allow": []string{"192.168.0.50:5432", " nas.local ", "192.168.0.50:5432", ""}}
	code, body := e.do("PUT", "/api/network", ok)
	if code != 200 || !strings.Contains(body, `"mode":"agent-only"`) || !strings.Contains(body, `["192.168.0.50:5432","nas.local"]`) {
		t.Errorf("save = %d %s", code, body)
	}
	// The "Allow" button in a session log.
	if code, body = e.do("POST", "/api/network/allow", map[string]string{"address": "192.168.0.1:80"}); code != 200 || !strings.Contains(body, "192.168.0.1:80") {
		t.Errorf("allow = %d %s", code, body)
	}
	if code, _ = e.do("POST", "/api/network/allow", map[string]string{"address": "bad address!"}); code != http.StatusBadRequest {
		t.Errorf("bad allow = %d", code)
	}
	e.restart()
	if _, body = e.do("GET", "/api/network", nil); !strings.Contains(body, "agent-only") || !strings.Contains(body, "192.168.0.1:80") {
		t.Errorf("settings lost on restart: %s", body)
	}
}

func TestSessionNetworkModes(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")

	// Default is internet only, recorded on the session and in its log.
	code, sess := e.launch(base, nil)
	if code != http.StatusCreated || sess["network"] != "internet" {
		t.Fatalf("default launch = %d %v", code, sess)
	}
	res := e.waitStatus(sess["id"].(string), "done")
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), "network: internet only") {
		t.Errorf("log should state the network mode: %s", raw)
	}

	// A saved default changes what new sessions get, and a per-session choice overrides it.
	e.do("PUT", "/api/network", map[string]any{"mode": "agent-only", "allow": []string{}})
	_, s2 := e.launch(base, nil)
	if s2["network"] != "agent-only" {
		t.Errorf("saved default not used: %v", s2["network"])
	}
	_, s3 := e.launch(base, map[string]any{"network": "full"})
	if s3["network"] != "full" {
		t.Errorf("per-session choice ignored: %v", s3["network"])
	}
	if code, _ := e.launch(base, map[string]any{"network": "bogus"}); code != http.StatusBadRequest {
		t.Errorf("unknown mode = %d, want 400", code)
	}
	e.waitStatus(s2["id"].(string), "done")
	e.waitStatus(s3["id"].(string), "done")
}

func TestProtectedModesRefusedWithoutProtection(t *testing.T) {
	e := newEnv(t)
	e.info = egress.Info{Reason: "Unire is not running in a Docker container"}
	e.restart()
	base := withServer(t, e, "tok")

	code, res := e.launch(base, nil) // default: internet only
	if code != http.StatusBadRequest || !strings.Contains(res["error"].(string), "not running in a Docker container") {
		t.Errorf("want a refusal with the reason, got %d %v", code, res)
	}
	if len(e.box.snapshot()) != 0 {
		t.Error("nothing may run when protection is unavailable")
	}
	code, res = e.launch(base, map[string]any{"network": "full"})
	if code != http.StatusCreated {
		t.Errorf("full access should still work: %d %v", code, res)
	}
	e.waitStatus(res["id"].(string), "done")
	_, body := e.do("GET", "/api/network", nil)
	if !strings.Contains(body, `"available":false`) || !strings.Contains(body, "not running in a Docker container") {
		t.Errorf("status should explain: %s", body)
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

func TestTaskNameComesFromThePrompt(t *testing.T) {
	cases := map[string]string{
		"Add a login page":                    "Add a login page",
		"  Fix the bug\nthen update the docs": "Fix the bug",
		"":                                    "",
		strings.Repeat("word ", 30):           strings.TrimSpace(strings.Repeat("word ", 12)) + "…",
		strings.Repeat("x", 100):              strings.Repeat("x", 57) + "…",
	}
	for in, want := range cases {
		if got := taskNameFrom(in); got != want {
			t.Errorf("taskNameFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLaunchNamesTheTaskFromThePrompt(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	code, body := e.launch(base, map[string]any{"task": "", "prompt": "Add a login page\nwith a form"})
	if code != http.StatusCreated || body["task"] != "Add a login page" {
		t.Errorf("launch without a name = %d %s", code, body)
	}
}

func TestRepliesFormOneConversation(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	code, first := e.launch(base, map[string]any{"task": "", "prompt": "Add a login page"})
	if code != http.StatusCreated {
		t.Fatalf("launch = %d %v", code, first)
	}
	id1 := first["id"].(string)
	e.waitStatus(id1, "done")

	code, second := replyTo(e, id1, map[string]any{"prompt": "Now add a logout button"})
	if code != http.StatusCreated {
		t.Fatalf("reply = %d %v", code, second)
	}
	id2 := second["id"].(string)
	if second["branch"] != first["branch"] {
		t.Errorf("a reply continues on its conversation's branch: %v vs %v", second["branch"], first["branch"])
	}
	if second["parent"] != id1 || second["task"] != first["task"] || second["prompt"] != "Now add a logout button" {
		t.Errorf("a reply keeps the conversation's name and shows only what was written: %v", second)
	}
	e.waitStatus(id2, "done")

	if code, _ := replyTo(e, id1, map[string]any{"prompt": "again"}); code != http.StatusConflict {
		t.Errorf("replying to a turn that already has a reply = %d, want 409", code)
	}
	if code, _ := replyTo(e, id2, map[string]any{"prompt": "  "}); code != http.StatusBadRequest {
		t.Errorf("an empty reply = %d, want 400", code)
	}

	// Either turn opens the same conversation, first to last.
	for _, id := range []string{id1, id2} {
		_, body := e.do("GET", "/api/sessions/"+id+"/thread", nil)
		var thread []map[string]any
		json.Unmarshal([]byte(body), &thread)
		if len(thread) != 2 || thread[0]["id"] != id1 || thread[1]["id"] != id2 {
			t.Errorf("thread from %s = %s", id, body)
		}
	}
	// The list shows the conversation once, with the newest turn's state.
	_, body := e.do("GET", "/api/sessions", nil)
	var list []map[string]any
	json.Unmarshal([]byte(body), &list)
	if len(list) != 1 || list[0]["id"] != id1 || list[0]["latest"] != id2 {
		t.Errorf("list = %s", body)
	}
}

func TestCannotReplyWhileTheAgentIsRunning(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	box := e.box
	box.mu.Lock()
	box.block = make(chan struct{})
	box.mu.Unlock()
	_, first := e.launch(base, nil)
	id := first["id"].(string)
	e.waitStatus(id, "running")
	if code, _ := replyTo(e, id, map[string]any{"prompt": "more"}); code != http.StatusConflict {
		t.Errorf("reply to a running session = %d, want 409", code)
	}
	e.do("POST", "/api/sessions/"+id+"/stop", nil)
}

func replyTo(e *env, id string, body map[string]any) (int, map[string]any) {
	code, raw := e.do("POST", "/api/sessions/"+id+"/reply", body)
	var res map[string]any
	json.Unmarshal([]byte(raw), &res)
	return code, res
}

func TestAnsweringAQuestionNobodyAskedIsRefused(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	_, first := e.launch(base, nil)
	id := first["id"].(string)
	e.waitStatus(id, "done")
	if code, _ := e.do("POST", "/api/sessions/"+id+"/answer", map[string]any{"id": "1", "text": "yes"}); code != http.StatusConflict {
		t.Errorf("answer with no question = %d, want 409", code)
	}
	if code, _ := e.do("POST", "/api/sessions/"+id+"/answer", map[string]any{"id": "1", "text": "  "}); code != http.StatusBadRequest {
		t.Errorf("empty answer = %d, want 400", code)
	}
	if code, _ := e.do("POST", "/api/sessions/nope/answer", map[string]any{"id": "1", "text": "x"}); code != http.StatusNotFound {
		t.Errorf("answer for an unknown session = %d, want 404", code)
	}
}

func TestAReplyIsToldWhatTheAgentAnsweredBefore(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.box.mu.Lock()
	e.box.out = "The answer is 42."
	e.box.mu.Unlock()
	_, first := e.launch(base, map[string]any{"prompt": "What is the answer?"})
	id1 := first["id"].(string)
	e.waitStatus(id1, "done")
	// The conversation is lost before the reply: the agent must start a new one that
	// is told the whole story.
	e.box.mu.Lock()
	e.box.lostConv = true
	e.box.mu.Unlock()
	_, second := replyTo(e, id1, map[string]any{"prompt": "And the question?"})
	e.waitStatus(second["id"].(string), "done")

	// A steerable agent is told its task as its first message (not on its command line).
	e.box.mu.Lock()
	prompts := append([]string(nil), e.box.prompts...)
	e.box.mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("expected two agent prompts (the resume never got to read one), got %d", len(prompts))
	}
	for _, want := range []string{"<earlier-request>", "What is the answer?", "<earlier-answer>", "The answer is 42.", "And the question?"} {
		if !strings.Contains(prompts[1], want) {
			t.Errorf("the reply's prompt is missing %q:\n%s", want, prompts[1])
		}
	}
	if strings.Contains(prompts[0], "<earlier-answer>") {
		t.Error("a first message has no earlier answer")
	}
}

func TestAReplyBySameAgentResumesItsConversation(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	_, first := e.launch(base, map[string]any{"prompt": "What is the answer?"})
	id1 := first["id"].(string)
	e.waitStatus(id1, "done")
	_, second := replyTo(e, id1, map[string]any{"prompt": "And the question?"})
	e.waitStatus(second["id"].(string), "done")

	var claudes [][]string
	e.box.mu.Lock()
	for _, c := range e.box.calls {
		if inner, isAgent, _ := agenttest.Unwrap(c.Argv); isAgent && inner[0] == "claude" {
			claudes = append(claudes, inner)
		}
	}
	prompts := append([]string(nil), e.box.prompts...)
	e.box.mu.Unlock()
	if len(claudes) < 2 {
		t.Fatalf("expected two Claude runs, got %d", len(claudes))
	}
	argAfter := func(argv []string, flag string) string {
		if i := slices.Index(argv, flag); i >= 0 && i+1 < len(argv) {
			return argv[i+1]
		}
		return ""
	}
	conv := argAfter(claudes[0], "--session-id")
	if conv == "" || argAfter(claudes[len(claudes)-1], "--resume") != conv {
		t.Fatalf("the reply carries on the first conversation %q: %v", conv, claudes[len(claudes)-1])
	}
	if p := prompts[len(prompts)-1]; strings.Contains(p, "<earlier-request>") || !strings.Contains(p, "And the question?") {
		t.Errorf("a resumed agent is not told again what it remembers:\n%s", p)
	}
}

func TestSteeringARunningAgentThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.box.mu.Lock()
	e.box.block = make(chan struct{})
	e.box.mu.Unlock()
	code, first := e.launch(base, nil)
	if code != http.StatusCreated || first["canSteer"] != true {
		t.Fatalf("launch = %d, canSteer = %v (a Claude session can be steered)", code, first["canSteer"])
	}
	id := first["id"].(string)
	e.waitStatus(id, "running")

	if code, _ := e.do("POST", "/api/sessions/"+id+"/steer", map[string]any{"text": "  "}); code != http.StatusBadRequest {
		t.Errorf("an empty message = %d, want 400", code)
	}
	code, body := e.do("POST", "/api/sessions/"+id+"/steer", map[string]any{"text": "also add tests", "now": true})
	if code != http.StatusOK || !strings.Contains(body, `"id"`) {
		t.Fatalf("steer = %d %s", code, body)
	}
	// Wait until the agent has received its first message and is held: the driver has begun,
	// and the steer message will be taken before the agent can finish.
	held := false
	for i := 0; i < 300; i++ {
		e.box.mu.Lock()
		held = len(e.box.prompts) >= 1
		e.box.mu.Unlock()
		if held {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !held {
		_, l := e.do("GET", "/api/sessions/"+id+"/logs", nil)
		t.Fatalf("the agent never received its first message; log:\n%s", l)
	}
	e.box.mu.Lock()
	close(e.box.block)
	e.box.mu.Unlock()
	e.waitStatus(id, "done")

	_, logs := e.do("GET", "/api/sessions/"+id+"/logs", nil)
	for _, want := range []string{`"kind":"steer"`, "also add tests", `"kind":"steerstate"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs are missing %s: %s", want, logs)
		}
	}
	e.box.mu.Lock()
	told := append([]string(nil), e.box.prompts...)
	e.box.mu.Unlock()
	// The step that saves memory also talks to the agent, after the turn is done.
	var said []string
	for _, p := range told {
		if !strings.HasPrefix(p, "Your task is finished") {
			said = append(said, p)
		}
	}
	told = said
	if len(told) != 2 || told[1] != "also add tests" {
		short := make([]string, len(told))
		for i, p := range told {
			short[i] = fmt.Sprintf("%.60q", p)
		}
		t.Errorf("the agent was told %d messages: %v; logs: %.2500s", len(told), short, logs)
	}
	// Once it has finished, a message has to be a reply.
	if code, _ := e.do("POST", "/api/sessions/"+id+"/steer", map[string]any{"text": "late"}); code != http.StatusConflict {
		t.Errorf("a message to a finished session = %d, want 409", code)
	}
	if code, _ := e.do("POST", "/api/sessions/nope/steer", map[string]any{"text": "x"}); code != http.StatusNotFound {
		t.Errorf("an unknown session = %d, want 404", code)
	}
}

func TestPromotingAWaitingMessageThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.box.mu.Lock()
	e.box.block = make(chan struct{})
	e.box.mu.Unlock()
	_, first := e.launch(base, nil)
	id := first["id"].(string)
	e.waitStatus(id, "running")
	code, body := e.do("POST", "/api/sessions/"+id+"/steer", map[string]any{"text": "do this sooner"})
	if code != http.StatusOK {
		t.Fatalf("steer = %d %s", code, body)
	}
	var res map[string]string
	json.Unmarshal([]byte(body), &res)
	if code, _ := e.do("POST", "/api/sessions/"+id+"/steer/"+res["id"]+"/now", nil); code != http.StatusOK {
		t.Errorf("promote = %d, want 200", code)
	}
	e.box.mu.Lock()
	close(e.box.block)
	e.box.mu.Unlock()
	e.waitStatus(id, "done")
	if code, _ := e.do("POST", "/api/sessions/"+id+"/steer/"+res["id"]+"/now", nil); code != http.StatusConflict {
		t.Errorf("promote after the run = %d, want 409", code)
	}
}

func TestAReplyBoilsTheWholeConversationDown(t *testing.T) {
	mk := func(i int, agent, prompt string) store.Session {
		return store.Session{ID: fmt.Sprint("t", i), Agent: agent, Prompt: prompt, Branch: "agent/x"}
	}
	var thread []store.Session
	for i := 1; i <= 30; i++ {
		agentID := "claude"
		if i%2 == 0 {
			agentID = "antigravity"
		}
		thread = append(thread, mk(i, agentID, fmt.Sprintf("request number %d %s", i, strings.Repeat("x", 2000))))
	}
	answer := func(id string) string { return "answer of " + id + " " + strings.Repeat("y", 3000) }
	p := replyPreamble(thread, answer)

	if !strings.Contains(p, "request number 30") || !strings.Contains(p, "answer of t30") {
		t.Error("the last turn is given")
	}
	if !strings.Contains(p, "request number 29") {
		t.Error("the turn before is boiled down, not dropped")
	}
	if !strings.Contains(p, "earlier turns are left out") {
		t.Error("when old turns do not fit, it says so")
	}
	if strings.Contains(p, "request number 1 ") {
		t.Error("the oldest turns are the ones left out")
	}
	if strings.Contains(p, "request number 29 "+strings.Repeat("x", 1000)) {
		t.Error("an older request is clipped")
	}
	if len(p) > 36000 {
		t.Errorf("the preamble is %d characters, it should stay near its budget", len(p))
	}
	if !strings.Contains(p, "(antigravity)") || !strings.Contains(p, "branch agent/x") {
		t.Errorf("who did each turn and where the work is: %q", p[:300])
	}
	if replyPreamble(nil, answer) != "" {
		t.Error("no conversation, no preamble")
	}
}

func TestAReplyBySameAgentCarriesOnItsOwnConversation(t *testing.T) {
	answer := func(id string) string { return "answer of " + id }
	thread := []store.Session{
		{ID: "t1", Agent: "claude", Prompt: "first ask", Branch: "agent/x", AgentConv: "conv-1"},
		{ID: "t2", Agent: "claude", Prompt: "second ask", Branch: "agent/x", AgentConv: "conv-1"},
		{ID: "t3", Agent: "codex", Prompt: "third ask", Branch: "agent/x", AgentConv: "thread-9"},
	}

	resume, p, fallback := replyContext(thread, "claude", answer)
	if resume != "conv-1" {
		t.Fatalf("claude carries on its own conversation, got %q", resume)
	}
	if !strings.Contains(p, "third ask") || !strings.Contains(p, "answer of t3") {
		t.Errorf("it hears what the other agent did since: %q", p)
	}
	if strings.Contains(p, "first ask") {
		t.Errorf("what it already remembers is not repeated: %q", p)
	}
	if !strings.Contains(fallback, "first ask") || !strings.Contains(fallback, "third ask") {
		t.Errorf("the fallback tells the whole story: %q", fallback)
	}

	resume, p, _ = replyContext(thread, "codex", answer)
	if resume != "thread-9" || strings.Contains(p, "ask") {
		t.Errorf("the last agent carries on with nothing to catch up on: %q %q", resume, p)
	}

	resume, p, fallback = replyContext(thread, "antigravity", answer)
	if resume != "" || fallback != "" || !strings.Contains(p, "first ask") {
		t.Errorf("an agent new to the conversation is told all of it: %q %q", resume, p)
	}
}

func TestModelSelectionAndSwitchingRoundTrip(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok-1")

	// 1. Validation error on start.
	if code, _ := e.launch(base, map[string]any{"model": "bad;injection"}); code != http.StatusBadRequest {
		t.Errorf("start with invalid model = %d, want 400", code)
	}
	if code, _ := e.launch(base, map[string]any{"model": "with space"}); code != http.StatusBadRequest {
		t.Errorf("start with space model = %d, want 400", code)
	}

	// 2. Start with a model.
	code, sess := e.launch(base, map[string]any{"agent": "claude", "model": "opus"})
	if code != http.StatusCreated {
		t.Fatalf("start with model = %d %v", code, sess)
	}
	id := sess["id"].(string)
	if sess["model"] != "opus" {
		t.Errorf("sessionView.model = %v, want opus", sess["model"])
	}

	// Verify database record has the model.
	stSess, ok, err := e.srv.store.Session(id)
	if err != nil || !ok || stSess.Model != "opus" {
		t.Errorf("store.Session model = %q, want opus", stSess.Model)
	}

	// Verify GET /api/sessions/{id} returns the model.
	code, getBody := e.do("GET", "/api/sessions/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/sessions/{id} = %d %s", code, getBody)
	}
	var getView map[string]any
	json.Unmarshal([]byte(getBody), &getView)
	if getView["model"] != "opus" {
		t.Errorf("getView.model = %v, want opus", getView["model"])
	}

	e.waitStatus(id, "done")

	// 3. Validation error on reply.
	if code, _ := e.do("POST", "/api/sessions/"+id+"/reply", map[string]any{"prompt": "p", "model": "bad;injection"}); code != http.StatusBadRequest {
		t.Errorf("reply with invalid model = %d, want 400", code)
	}

	// 4. Reply switching agent with another model.
	code, replyRaw := e.do("POST", "/api/sessions/"+id+"/reply", map[string]any{
		"prompt": "now review with antigravity",
		"agent":  "antigravity",
		"model":  "gemini-3.8-flash-high",
	})
	if code != http.StatusCreated {
		t.Fatalf("reply with model = %d %s", code, replyRaw)
	}
	var replyView map[string]any
	json.Unmarshal([]byte(replyRaw), &replyView)
	replyID := replyView["id"].(string)
	if replyView["agent"] != "antigravity" || replyView["model"] != "gemini-3.8-flash-high" {
		t.Errorf("replyView agent=%v model=%v; want antigravity, gemini-3.8-flash-high", replyView["agent"], replyView["model"])
	}

	// Verify reply database record has the agent and model.
	stReply, ok, err := e.srv.store.Session(replyID)
	if err != nil || !ok || stReply.Agent != "antigravity" || stReply.Model != "gemini-3.8-flash-high" {
		t.Errorf("store.Session reply agent=%q model=%q; want antigravity, gemini-3.8-flash-high", stReply.Agent, stReply.Model)
	}

	// Verify GET /api/sessions/{replyId} returns agent and model.
	code, getReplyBody := e.do("GET", "/api/sessions/"+replyID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/sessions/{replyId} = %d %s", code, getReplyBody)
	}
	var getReplyView map[string]any
	json.Unmarshal([]byte(getReplyBody), &getReplyView)
	if getReplyView["agent"] != "antigravity" || getReplyView["model"] != "gemini-3.8-flash-high" {
		t.Errorf("getReplyView agent=%v model=%v; want antigravity, gemini-3.8-flash-high", getReplyView["agent"], getReplyView["model"])
	}

	e.waitStatus(replyID, "done")

	// A reply that keeps the agent and names no model keeps the model of the turn before.
	next := func(from string, body map[string]any) map[string]any {
		t.Helper()
		body["prompt"] = "carry on"
		code, raw := e.do("POST", "/api/sessions/"+from+"/reply", body)
		if code != http.StatusCreated {
			t.Fatalf("reply %v = %d %s", body, code, raw)
		}
		var v map[string]any
		json.Unmarshal([]byte(raw), &v)
		e.waitStatus(v["id"].(string), "done")
		return v
	}
	kept := next(replyID, map[string]any{})
	if kept["agent"] != "antigravity" || kept["model"] != "gemini-3.8-flash-high" {
		t.Errorf("same agent, no model: agent=%v model=%v; want the last turn's", kept["agent"], kept["model"])
	}
	// An empty model asks for the agent's default.
	reset := next(kept["id"].(string), map[string]any{"model": ""})
	if reset["agent"] != "antigravity" || reset["model"] != "" {
		t.Errorf("empty model: agent=%v model=%v; want antigravity with its default", reset["agent"], reset["model"])
	}
	// Naming the same agent counts as keeping it.
	picked := next(reset["id"].(string), map[string]any{"model": "gemini-3.8-flash-low"})
	kept2 := next(picked["id"].(string), map[string]any{"agent": "antigravity"})
	if kept2["model"] != "gemini-3.8-flash-low" {
		t.Errorf("same agent named, no model: model=%v; want the last turn's", kept2["model"])
	}
	// Another agent with no model starts from its own default.
	switched := next(kept2["id"].(string), map[string]any{"agent": "claude"})
	if switched["agent"] != "claude" || switched["model"] != "" {
		t.Errorf("other agent, no model: agent=%v model=%v; want claude with its default", switched["agent"], switched["model"])
	}

	// 5. Start and reply without a model (defaults).
	code, sessDef := e.launch(base, nil)
	if code != http.StatusCreated || sessDef["model"] != "" {
		t.Fatalf("default session = %d %v", code, sessDef)
	}
	defID := sessDef["id"].(string)
	e.waitStatus(defID, "done")

	code, replyDefRaw := e.do("POST", "/api/sessions/"+defID+"/reply", map[string]any{"prompt": "continue"})
	if code != http.StatusCreated {
		t.Fatalf("default reply = %d %s", code, replyDefRaw)
	}
	var replyDefView map[string]any
	json.Unmarshal([]byte(replyDefRaw), &replyDefView)
	if replyDefView["model"] != "" {
		t.Errorf("default reply model = %v; want empty", replyDefView["model"])
	}

	// 6. GET /api/agents includes models list for each agent.
	code, agentsBody := e.do("GET", "/api/agents", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/agents = %d %s", code, agentsBody)
	}
	var agentsList []map[string]any
	json.Unmarshal([]byte(agentsBody), &agentsList)
	foundAgents := map[string][]string{}
	for _, a := range agentsList {
		id := a["id"].(string)
		if models, ok := a["models"].([]any); ok {
			for _, m := range models {
				foundAgents[id] = append(foundAgents[id], m.(string))
			}
		}
	}
	for _, wantAgent := range []string{"claude", "codex", "antigravity"} {
		if len(foundAgents[wantAgent]) == 0 {
			t.Errorf("agent %s has no models in GET /api/agents: %v", wantAgent, foundAgents[wantAgent])
		}
	}
	if !slices.Contains(foundAgents["claude"], "opus") {
		t.Errorf("claude models %v missing opus", foundAgents["claude"])
	}
	if !slices.Contains(foundAgents["codex"], "gpt-6-luna") {
		t.Errorf("codex models %v missing gpt-6-luna", foundAgents["codex"])
	}
	if !slices.Contains(foundAgents["antigravity"], "gemini-3.8-flash-high") {
		t.Errorf("antigravity models %v missing gemini-3.8-flash-high", foundAgents["antigravity"])
	}
}

func TestListSessionsIncludesPulls(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	_, s1 := e.launch(base, nil)
	id1 := s1["id"].(string)

	if err := e.srv.store.AddPullRequest(store.PullRequest{
		RootID:   id1,
		ServerID: "srv",
		Repo:     "greg/api",
		Number:   42,
		Branch:   "b",
		Title:    "PR 42",
		URL:      "http://pr/42",
		State:    "open",
	}); err != nil {
		t.Fatal(err)
	}

	code, body := e.do("GET", "/api/sessions", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/sessions = %d %s", code, body)
	}
	var list []map[string]any
	json.Unmarshal([]byte(body), &list)
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	pulls, ok := list[0]["pulls"].([]any)
	if !ok || len(pulls) != 1 {
		t.Fatalf("expected 1 pull request in session, got %v", list[0]["pulls"])
	}
	p := pulls[0].(map[string]any)
	if p["number"].(float64) != 42 || p["state"] != "open" || p["repo"] != "greg/api" {
		t.Errorf("pull mismatch: %v", p)
	}
}

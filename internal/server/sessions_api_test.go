package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	heads    int               // rev-parse calls: the first of each pair is the start HEAD, the second differs (the agent committed)

	status      map[string]statusResult // answers to sign-in questions, by command name
	statusAsked []string
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
		out, block := b.out, b.block
		b.mu.Unlock()
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
			func(u agenttest.User) { b.mu.Lock(); b.prompts = append(b.prompts, u.Text); b.mu.Unlock() })
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
			return nil
		case "ls":
			return fmt.Errorf("exit status 2") // nothing was cloned or asked yet
		case "mkdir", "rm", "sh":
			if o.Stdin != nil {
				io.Copy(io.Discard, o.Stdin)
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
	_, second := replyTo(e, id1, map[string]any{"prompt": "And the question?"})
	e.waitStatus(second["id"].(string), "done")

	// A steerable agent is told its task as its first message (not on its command line).
	e.box.mu.Lock()
	prompts := append([]string(nil), e.box.prompts...)
	e.box.mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("expected two agent runs, got %d", len(prompts))
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
	// Release the agent only once it has taken the message: a message that reaches an agent
	// that has already finished is "not delivered", which is correct but not what this tests.
	for i := 0; i < 300; i++ {
		_, l := e.do("GET", "/api/sessions/"+id+"/logs", nil)
		if strings.Contains(l, `"kind":"steerstate"`) && strings.Contains(l, "queued") {
			break
		}
		time.Sleep(10 * time.Millisecond)
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
		t.Errorf("the agent was told %d messages: %v", len(told), short)
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
		thread = append(thread, mk(i, agentID, fmt.Sprintf("request number %d %s", i, strings.Repeat("x", 800))))
	}
	answer := func(id string) string { return "answer of " + id + " " + strings.Repeat("y", 1500) }
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
	if strings.Contains(p, "request number 29 "+strings.Repeat("x", 600)) {
		t.Error("an older request is clipped")
	}
	if len(p) > 14000 {
		t.Errorf("the preamble is %d characters, it should stay near its budget", len(p))
	}
	if !strings.Contains(p, "(antigravity)") || !strings.Contains(p, "branch agent/x") {
		t.Errorf("who did each turn and where the work is: %q", p[:300])
	}
	if replyPreamble(nil, answer) != "" {
		t.Error("no conversation, no preamble")
	}
}

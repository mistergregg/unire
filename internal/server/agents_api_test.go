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

	"github.com/coder/websocket"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/terminal"
)

// RunOnce makes the fake runner answer sign-in status questions. Results come
// from box.status, keyed by the first word of the command.
func (r fakeRunner) RunOnce(_ context.Context, argv []string) (string, bool, error) {
	r.box.mu.Lock()
	defer r.box.mu.Unlock()
	r.box.statusAsked = append(r.box.statusAsked, argv[0])
	if res, ok := r.box.status[argv[0]]; ok {
		return res.out, res.ok, nil
	}
	return "", false, io.ErrUnexpectedEOF
}

type statusResult struct {
	out string
	ok  bool
}

var _ runner.OneShot = fakeRunner{}

func (b *fakeBox) setStatus(m map[string]statusResult) {
	b.mu.Lock()
	b.status = m
	b.mu.Unlock()
}

func (b *fakeBox) asked() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.statusAsked...)
}

func TestAgentsPageListsAgentsWithSignInStatus(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.setStatus(map[string]statusResult{
		"claude": {`{"loggedIn": true, "authMethod": "claude.ai"}`, true},
		"codex":  {"Not logged in", true},
		"sh":     {"", true}, // antigravity's sign-in file check
	})
	code, body := e.do("GET", "/api/agents", nil)
	if code != 200 {
		t.Fatalf("agents = %d %s", code, body)
	}
	var list []map[string]any
	json.Unmarshal([]byte(body), &list)
	got := map[string]map[string]any{}
	for _, a := range list {
		got[a["id"].(string)] = a
	}
	if len(got) < 3 {
		t.Fatalf("want claude, codex and antigravity: %s", body)
	}
	if got["claude"]["loggedIn"] != true || got["codex"]["loggedIn"] != false || got["antigravity"]["loggedIn"] != true {
		t.Errorf("sign-in states wrong: claude=%v codex=%v antigravity=%v", got["claude"]["loggedIn"], got["codex"]["loggedIn"], got["antigravity"]["loggedIn"])
	}
	for _, a := range list {
		if a["label"] == "" || a["description"] == "" || a["loginHelp"] == "" {
			t.Errorf("incomplete agent: %v", a)
		}
		if _, leaked := a["LoginArgv"]; leaked {
			t.Error("commands must not be sent to the browser")
		}
	}
	if v := got["codex"]["verified"].(map[string]any); v["unattended"] != false {
		t.Errorf("codex has not been run for real, so it must not claim to be: %v", v)
	}
}

func TestStatusUnknownIsNotReportedAsSignedOut(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.setStatus(map[string]statusResult{}) // every status question fails
	_, body := e.do("GET", "/api/agents", nil)
	var list []map[string]any
	json.Unmarshal([]byte(body), &list)
	for _, a := range list {
		if a["loggedIn"] != nil {
			t.Errorf("%v: when the check itself fails the answer is unknown, not %v", a["id"], a["loggedIn"])
		}
		if a["statusNote"] == nil || a["statusNote"] == "" {
			t.Errorf("%v: unknown status should say why", a["id"])
		}
	}
}

func TestStatusIsCachedUntilRechecked(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.setStatus(map[string]statusResult{"claude": {`{"loggedIn": false}`, true}, "codex": {"Not logged in", true}, "sh": {"", false}})
	e.do("GET", "/api/agents", nil)
	first := len(e.box.asked())
	e.do("GET", "/api/agents", nil)
	if len(e.box.asked()) != first {
		t.Error("a second request inside the cache window should not start more containers")
	}
	e.box.setStatus(map[string]statusResult{"claude": {`{"loggedIn": true}`, true}, "codex": {"Not logged in", true}, "sh": {"", false}})
	_, body := e.do("POST", "/api/agents/recheck", nil)
	if !strings.Contains(strings.ReplaceAll(body, " ", ""), `"id":"claude"`) || !strings.Contains(body, `"loggedIn":true`) {
		t.Errorf("recheck should show the new state: %s", body)
	}
}

func TestLaunchRefusedWhenAVerifiedAgentIsSignedOut(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.box.setStatus(map[string]statusResult{"claude": {`{"loggedIn": false}`, true}, "codex": {"Not logged in", true}, "sh": {"", false}})
	code, res := e.launch(base, nil) // claude
	if code != http.StatusBadRequest || !strings.Contains(res["error"].(string), "Claude Code is not signed in") || !strings.Contains(res["error"].(string), "Agents") {
		t.Errorf("want a clear refusal pointing at the Agents page, got %d %v", code, res)
	}
	if len(e.box.snapshot()) != 0 {
		t.Error("nothing may run for a signed-out agent")
	}
	// Codex's check is unverified (it is a guess), so it must not block a launch.
	code, res = e.launch(base, map[string]any{"agent": "codex"})
	if code != http.StatusCreated {
		t.Errorf("an unverified status check must never block: %d %v", code, res)
	}
	e.waitStatus(res["id"].(string), "done")
}

func TestLaunchAllowedWhenTheSignInCheckCannotRun(t *testing.T) {
	e := newEnv(t)
	base := withServer(t, e, "tok")
	e.box.setStatus(map[string]statusResult{}) // the check fails: unknown
	if code, res := e.launch(base, nil); code != http.StatusCreated {
		t.Errorf("an unknown status must not block: %d %v", code, res)
	} else {
		e.waitStatus(res["id"].(string), "done")
	}
}

func TestInspectAndResetRunTheAgentsOwnCommands(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.setStatus(map[string]statusResult{
		"sh":     {"Antigravity files (name, bytes):\n  auth.json  82", true},
		"claude": {`{"loggedIn": true}`, true},
		"codex":  {"Not logged in", true},
	})
	code, body := e.do("POST", "/api/agents/antigravity/inspect", nil)
	if code != 200 || !strings.Contains(body, "auth.json") || !strings.Contains(body, `"ok":true`) {
		t.Errorf("inspect = %d %s", code, body)
	}
	before := len(e.box.asked())
	if code, _ := e.do("POST", "/api/agents/antigravity/reset", nil); code != 200 {
		t.Fatalf("reset = %d", code)
	}
	if len(e.box.asked()) <= before || e.box.asked()[before] != "sh" {
		t.Errorf("reset should have run the agent's own reset command, asked: %v", e.box.asked())
	}
	if code, _ := e.do("POST", "/api/agents/nope/reset", nil); code != http.StatusNotFound {
		t.Errorf("unknown agent = %d", code)
	}
}

func TestResetForgetsTheCachedSignInState(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.setStatus(map[string]statusResult{"claude": {`{"loggedIn": true}`, true}, "codex": {"Not logged in", true}, "sh": {"done", true}})
	e.do("GET", "/api/agents", nil)
	e.box.setStatus(map[string]statusResult{"claude": {`{"loggedIn": false}`, true}, "codex": {"Not logged in", true}, "sh": {"done", true}})
	e.do("POST", "/api/agents/claude/reset", nil)
	_, body := e.do("GET", "/api/agents", nil)
	var list []map[string]any
	json.Unmarshal([]byte(body), &list)
	for _, a := range list {
		if a["id"] == "claude" && a["loggedIn"] != false {
			t.Errorf("after a reset the page must show the new state, got %v", a["loggedIn"])
		}
	}
}

func TestLastSignInOutputIsKeptCleanedAndOwnerOnly(t *testing.T) {
	e := newEnv(t)
	e.setup()
	started := withTerminal(e)
	if code, _ := e.do("GET", "/api/agents/antigravity/last-signin", nil); code != http.StatusNotFound {
		t.Errorf("before any sign-in = %d, want 404", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, e.wsURL("/api/agents/antigravity/login"), &websocket.DialOptions{HTTPHeader: e.cookieHeader()})
	if err != nil {
		t.Fatal(err)
	}
	// The terminal shows an error, a link and a code.
	deadline := time.Now().Add(3 * time.Second)
	for len(*started) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	(*started)[0].in <- []byte("\x1b[31mFailed to sign in. Message: needs GOOGLE_CLOUD_PROJECT\x1b[0m\r\nhttps://accounts.google.com/o/oauth2/v2/auth?state=SECRETSTATE\r\ncode 4/0AeanS0Zq2x9abcDEFghiJKL\r\n")
	if _, _, err := c.Read(ctx); err != nil { // wait until the browser has received it
		t.Fatal(err)
	}
	code, body := e.do("GET", "/api/agents/antigravity/last-signin", nil)
	if code != 200 || !strings.Contains(body, "Failed to sign in. Message: needs GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("last sign-in = %d %s", code, body)
	}
	for _, secret := range []string{"SECRETSTATE", "4/0AeanS0Zq2x9abcDEFghiJKL", "\\u001b"} {
		if strings.Contains(body, secret) {
			t.Errorf("leaked %q: %s", secret, body)
		}
	}
	c.Close(websocket.StatusNormalClosure, "")
	e.do("POST", "/api/logout", nil)
	if code, _ := e.do("GET", "/api/agents/antigravity/last-signin", nil); code != http.StatusUnauthorized {
		t.Errorf("without login = %d", code)
	}
}

func TestAgentToolsAreOwnerOnlyAndPostOnly(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if code, _ := e.do("GET", "/api/agents/antigravity/reset", nil); code == 200 {
		t.Error("a reset must not be reachable with GET (a link or image could trigger it)")
	}
	if code, _ := e.do("POST", "/api/agents/antigravity/reset", nil, true); code != http.StatusForbidden {
		t.Errorf("a reset without the CSRF header = %d, want 403", code)
	}
	e.do("POST", "/api/logout", nil)
	for _, p := range []string{"/api/agents/antigravity/reset", "/api/agents/antigravity/inspect"} {
		if code, _ := e.do("POST", p, nil); code != http.StatusUnauthorized {
			t.Errorf("%s without login = %d", p, code)
		}
	}
}

func TestInspectOutputIsCapped(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.box.setStatus(map[string]statusResult{"sh": {strings.Repeat("x", 50000), true}})
	_, body := e.do("POST", "/api/agents/antigravity/inspect", nil)
	if len(body) > 5000 {
		t.Errorf("output not capped: %d bytes", len(body))
	}
}

// ---- login terminal ----

type echoProc struct {
	mu     sync.Mutex
	in     chan []byte
	closed chan struct{}
	once   sync.Once
	sizes  [][2]uint16
	argv   []string
}

func newEchoProc(argv []string) *echoProc {
	return &echoProc{in: make(chan []byte, 16), closed: make(chan struct{}), argv: argv}
}
func (p *echoProc) Read(b []byte) (int, error) {
	select {
	case d := <-p.in:
		return copy(b, d), nil
	case <-p.closed:
		return 0, io.EOF
	}
}
func (p *echoProc) Write(b []byte) (int, error) {
	p.in <- append([]byte(nil), b...)
	return len(b), nil
}
func (p *echoProc) Resize(c, r uint16) error {
	p.mu.Lock()
	p.sizes = append(p.sizes, [2]uint16{c, r})
	p.mu.Unlock()
	return nil
}
func (p *echoProc) Wait() error  { <-p.closed; return nil }
func (p *echoProc) Close() error { p.once.Do(func() { close(p.closed) }); return nil }
func (p *echoProc) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

// withTerminal makes the server start echoProcs instead of Docker containers.
func withTerminal(e *env) *[]*echoProc {
	var started []*echoProc
	var mu sync.Mutex
	e.srv.terminalFor = func(Sandbox) terminal.Starter {
		return func(argv []string, cols, rows uint16) (terminal.Process, error) {
			p := newEchoProc(argv)
			p.sizes = append(p.sizes, [2]uint16{cols, rows})
			mu.Lock()
			started = append(started, p)
			mu.Unlock()
			return p, nil
		}
	}
	return &started
}

func (e *env) wsURL(path string) string { return "ws" + strings.TrimPrefix(e.ts.URL, "http") + path }

func (e *env) cookieHeader() http.Header {
	u, _ := url.Parse(e.ts.URL)
	h := http.Header{}
	for _, c := range e.c.Jar.Cookies(u) {
		h.Add("Cookie", c.Name+"="+c.Value)
	}
	return h
}

func TestLoginTerminalCarriesKeystrokesAndRunsOnlyTheFixedCommand(t *testing.T) {
	e := newEnv(t)
	e.setup()
	started := withTerminal(e)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, e.wsURL("/api/agents/codex/login?cols=120&rows=40"), &websocket.DialOptions{HTTPHeader: e.cookieHeader()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, []byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("echo = %q, %v", data, err)
	}
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":150,"rows":50}`)); err != nil {
		t.Fatal(err)
	}
	// A resize with a silly size is ignored.
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":60000,"rows":1}`))
	time.Sleep(150 * time.Millisecond)

	p := (*started)[0]
	want, _ := agent.Get("codex")
	if strings.Join(p.argv, " ") != strings.Join(want.Info().LoginArgv, " ") {
		t.Errorf("ran %v, want the agent's own sign-in command %v", p.argv, want.Info().LoginArgv)
	}
	p.mu.Lock()
	sizes := append([][2]uint16(nil), p.sizes...)
	p.mu.Unlock()
	if len(sizes) != 2 || sizes[0] != [2]uint16{120, 40} || sizes[1] != [2]uint16{150, 50} {
		t.Errorf("sizes = %v, want start 120x40 then 150x50 and no bogus size", sizes)
	}

	// Closing the browser connection ends the command.
	c.Close(websocket.StatusNormalClosure, "bye")
	deadline := time.Now().Add(3 * time.Second)
	for !p.isClosed() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !p.isClosed() {
		t.Error("the command must be stopped when the terminal closes")
	}
}

func TestLoginTerminalRefusals(t *testing.T) {
	e := newEnv(t)
	e.setup()
	withTerminal(e)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dial := func(path string, h http.Header) (*http.Response, error) {
		_, resp, err := websocket.Dial(ctx, e.wsURL(path), &websocket.DialOptions{HTTPHeader: h})
		return resp, err
	}

	if resp, _ := dial("/api/agents/claude/login", nil); resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without login: %v", resp)
	}
	if resp, _ := dial("/api/agents/nope/login", e.cookieHeader()); resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown agent: %v", resp)
	}
	// A page on another site cannot drive the owner's terminal.
	foreign := e.cookieHeader()
	foreign.Set("Origin", "https://evil.example")
	if resp, err := dial("/api/agents/claude/login", foreign); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("a foreign Origin must be refused with 403, got %v err=%v", resp, err)
	}
	// The browser cannot pick the command: there is no parameter for one.
	c, _, err := websocket.Dial(ctx, e.wsURL("/api/agents/claude/login?cmd=sh&argv=sh"), &websocket.DialOptions{HTTPHeader: e.cookieHeader()})
	if err != nil {
		t.Fatal(err)
	}
	c.Close(websocket.StatusNormalClosure, "")
}

func TestOnlyOneLoginTerminalAtATime(t *testing.T) {
	e := newEnv(t)
	e.setup()
	withTerminal(e)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, _, err := websocket.Dial(ctx, e.wsURL("/api/agents/claude/login"), &websocket.DialOptions{HTTPHeader: e.cookieHeader()})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	_, resp, err := websocket.Dial(ctx, e.wsURL("/api/agents/codex/login"), &websocket.DialOptions{HTTPHeader: e.cookieHeader()})
	if err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Errorf("a second terminal should be refused with 409, got %v err=%v", resp, err)
	}
	first.Close(websocket.StatusNormalClosure, "")
	// After the first closes, a new one can open.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, _, err := websocket.Dial(ctx, e.wsURL("/api/agents/codex/login"), &websocket.DialOptions{HTTPHeader: e.cookieHeader()}); err == nil {
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("a new terminal should open after the first one closed")
}

func TestClosingTheTerminalForgetsCachedSignInState(t *testing.T) {
	e := newEnv(t)
	e.setup()
	withTerminal(e)
	e.box.setStatus(map[string]statusResult{"claude": {`{"loggedIn": false}`, true}, "codex": {"Not logged in", true}, "sh": {"", false}})
	e.do("GET", "/api/agents", nil)
	e.box.setStatus(map[string]statusResult{"claude": {`{"loggedIn": true}`, true}, "codex": {"Not logged in", true}, "sh": {"", false}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, e.wsURL("/api/agents/claude/login"), &websocket.DialOptions{HTTPHeader: e.cookieHeader()})
	if err != nil {
		t.Fatal(err)
	}
	c.Close(websocket.StatusNormalClosure, "")
	time.Sleep(300 * time.Millisecond)
	_, body := e.do("GET", "/api/agents", nil)
	var list []map[string]any
	json.Unmarshal([]byte(body), &list)
	for _, a := range list {
		if a["id"] == "claude" && a["loggedIn"] != true {
			t.Errorf("after the sign-in terminal closed, the page should show the new state: %v", a["loggedIn"])
		}
	}
}

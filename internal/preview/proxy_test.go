package preview

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTokenRoundTrip(t *testing.T) {
	s := NewSigner()
	tok := s.Mint("s1.app", PurposeLink, time.Minute)
	if _, ok := s.Verify("s1.app", PurposeLink, tok); !ok {
		t.Fatal("a fresh token must verify")
	}
	if _, ok := s.Verify("s2.app", PurposeLink, tok); ok {
		t.Fatal("a token is only good for its own preview")
	}
	if _, ok := s.Verify("s1.app", PurposeSession, tok); ok {
		t.Fatal("a link token must not work as a session token")
	}
	if _, ok := NewSigner().Verify("s1.app", PurposeLink, tok); ok {
		t.Fatal("another secret must not verify")
	}
}

func TestTokenExpiresAndRejectsTampering(t *testing.T) {
	s := NewSigner()
	now := time.Unix(1_000_000, 0)
	s.now = func() time.Time { return now }
	tok := s.Mint("k", PurposeLink, time.Minute)
	now = now.Add(2 * time.Minute)
	if _, ok := s.Verify("k", PurposeLink, tok); ok {
		t.Fatal("an expired token must not verify")
	}
	now = time.Unix(1_000_000, 0)
	exp, sig, _ := strings.Cut(tok, ".")
	later := strconv.Itoa(1_000_000+3600) + "." + sig
	if _, ok := s.Verify("k", PurposeLink, later); ok {
		t.Fatalf("extending the expiry (%s) must break the signature", exp)
	}
	for _, bad := range []string{"", ".", "x.y", "123", "123.", ".abc", "999999999999999999999.AAAA"} {
		if _, ok := s.Verify("k", PurposeLink, bad); ok {
			t.Errorf("%q must not verify", bad)
		}
	}
}

// fixture: an app behind a hub.
type fixture struct {
	hub  *Hub
	app  *httptest.Server
	port int
	base string
	seen chan *http.Request
}

func newFixture(t *testing.T, appHandler func(http.ResponseWriter, *http.Request)) *fixture {
	t.Helper()
	f := &fixture{seen: make(chan *http.Request, 10)}
	f.app = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen <- r
		appHandler(w, r)
	}))
	t.Cleanup(f.app.Close)
	f.hub = &Hub{Signer: NewSigner(), Host: "127.0.0.1"}
	t.Cleanup(f.hub.CloseAll)
	port, err := f.hub.Open("s1.app", strings.TrimPrefix(f.app.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	f.port = port
	f.base = "http://127.0.0.1:" + strconv.Itoa(port)
	return f
}

// login follows the link flow and returns a client holding the session cookie.
func (f *fixture) login(t *testing.T) *http.Client {
	t.Helper()
	jar := newJar()
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(f.base + "/index.html?x=1&" + linkParam + "=" + url.QueryEscape(f.hub.LinkToken("s1.app")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("link login: status %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/index.html?x=1" {
		t.Fatalf("the token must be removed from the address, got %q", loc)
	}
	if resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("the redirect must not leak the address as a referrer")
	}
	return c
}

func TestLoginRedirectStaysOnThisSite(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(f.base + "//evil.example/x?" + linkParam + "=" + url.QueryEscape(f.hub.LinkToken("s1.app")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/evil.example/x" {
		t.Fatalf("Location %q must be a path on this site", loc)
	}
}

func TestNoCookieNoAccessAndAppNeverSeesTheRequest(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "secret app") })
	resp, err := http.Get(f.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	select {
	case <-f.seen:
		t.Fatal("an unauthenticated request reached the app")
	default:
	}
}

func TestBadAndWrongLinkTokens(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	other := f.hub.Signer.Mint("s9.other", PurposeLink, time.Minute)
	sess := f.hub.Signer.Mint("s1.app", PurposeSession, time.Minute)
	for name, tok := range map[string]string{"garbage": "nope", "other preview": other, "session token as link": sess} {
		resp, err := http.Get(f.base + "/?" + linkParam + "=" + url.QueryEscape(tok))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, resp.StatusCode)
		}
	}
}

func TestProxyAfterLogin(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello from "+r.URL.Path) })
	c := f.login(t)
	resp, err := c.Get(f.base + "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello from /index.html" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

func TestUnireCookiesNeverReachTheApp(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	c := f.login(t)
	<-drain(f.seen)
	req, _ := http.NewRequest("GET", f.base+"/", nil)
	req.Header.Set("Cookie", "unire_session=OWNERLOGIN; theme=dark; unire_other=1")
	// Add the preview cookie the jar holds as well.
	for _, ck := range c.Jar.Cookies(req.URL) {
		req.AddCookie(ck)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	got := (<-f.seen).Header.Get("Cookie")
	if strings.Contains(got, "unire_") || strings.Contains(got, "OWNERLOGIN") {
		t.Fatalf("Unire cookies reached the app: %q", got)
	}
	if got != "theme=dark" {
		t.Fatalf("the app's own cookie must still arrive, got %q", got)
	}
}

func TestAppCannotSetUnireCookies(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "unire_session=; Max-Age=0; Path=/")
		w.Header().Add("Set-Cookie", "unire_preview_1=x")
		w.Header().Add("Set-Cookie", "theme=dark; Path=/")
	})
	c := f.login(t)
	resp, err := c.Get(f.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	got := resp.Header.Values("Set-Cookie")
	if len(got) != 1 || !strings.HasPrefix(got[0], "theme=dark") {
		t.Fatalf("Set-Cookie after filtering: %q", got)
	}
}

func TestSessionCookieIsHttpOnlyAndNamedByPort(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	resp, _ := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).
		Get(f.base + "/?" + linkParam + "=" + url.QueryEscape(f.hub.LinkToken("s1.app")))
	resp.Body.Close()
	cs := resp.Cookies()
	if len(cs) != 1 || cs[0].Name != sessionCookieName(f.port) || !cs[0].HttpOnly || cs[0].MaxAge <= 0 {
		t.Fatalf("cookies %+v", cs)
	}
}

func TestSessionCookieOfOnePreviewDoesNotOpenAnother(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	app2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "two") }))
	defer app2.Close()
	port2, err := f.hub.Open("s2.app", strings.TrimPrefix(app2.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	c := f.login(t)
	// Send preview 1's cookie value to preview 2 under preview 2's cookie name.
	var tok string
	for _, ck := range c.Jar.Cookies(&url.URL{Scheme: "http", Host: "127.0.0.1"}) {
		tok = ck.Value
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port2)+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName(port2), Value: tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d: a session for one preview must not open another", resp.StatusCode)
	}
}

func TestExpiredSessionIsRefused(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	old := f.hub.Signer.Mint("s1.app", PurposeSession, -time.Minute)
	req, _ := http.NewRequest("GET", f.base+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName(f.port), Value: old})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestAppDownGivesFriendlyBadGateway(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	c := f.login(t)
	f.app.Close()
	resp, err := c.Get(f.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "not answering") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

func TestWebSocketStyleUpgradeIsProxied(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("no hijacker")
			return
		}
		conn, rw, _ := hj.Hijack()
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		rw.Flush()
		line, _ := rw.ReadString('\n')
		rw.WriteString("echo:" + line)
		rw.Flush()
	})
	c := f.login(t)
	var cookie *http.Cookie
	for _, ck := range c.Jar.Cookies(&url.URL{Scheme: "http", Host: "127.0.0.1"}) {
		cookie = ck
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(f.port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\nCookie: "+cookie.Name+"="+cookie.Value+"\r\n\r\n")
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("status line %q", status)
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if l == "\r\n" {
			break
		}
	}
	io.WriteString(conn, "ping\n")
	got, _ := br.ReadString('\n')
	if got != "echo:ping\n" {
		t.Fatalf("got %q", got)
	}
}

func TestHubOpenIsIdempotentAndMovesWithTheTarget(t *testing.T) {
	h := &Hub{Signer: NewSigner(), Host: "127.0.0.1"}
	defer h.CloseAll()
	p1, err := h.Open("k", "a:1")
	if err != nil {
		t.Fatal(err)
	}
	if p2, _ := h.Open("k", "a:1"); p2 != p1 {
		t.Fatal("same key and target must keep the port")
	}
	p3, _ := h.Open("k", "b:2")
	if p3 == p1 {
		// The old listener is closed, so the system may hand the port out again;
		// what matters is that the key now points at the new listener.
		t.Log("port reused")
	}
	if got, _ := h.Port("k"); got != p3 {
		t.Fatalf("Port = %d, want %d", got, p3)
	}
}

func TestHubUsesOnlyThePortRangeAndRunsOut(t *testing.T) {
	free := freePorts(t, 2)
	h := &Hub{Signer: NewSigner(), Host: "127.0.0.1", Low: free[0], High: free[0]}
	defer h.CloseAll()
	p, err := h.Open("a", "x:1")
	if err != nil || p != free[0] {
		t.Fatalf("port %d err %v", p, err)
	}
	if _, err := h.Open("b", "x:1"); err == nil {
		t.Fatal("a full range must be an error, not a port outside it")
	}
	h.Close("a")
	if _, err := h.Open("b", "x:1"); err != nil {
		t.Fatalf("a closed preview must free its port: %v", err)
	}
}

func TestHubSkipsPortsInUse(t *testing.T) {
	free := freePorts(t, 1)
	busy, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(free[0]))
	if err != nil {
		t.Skip("port taken meanwhile")
	}
	defer busy.Close()
	h := &Hub{Signer: NewSigner(), Host: "127.0.0.1", Low: free[0], High: free[0]}
	defer h.CloseAll()
	if _, err := h.Open("a", "x:1"); err == nil {
		t.Fatal("a port held by something else must not be taken")
	}
}

func TestCloseAllRefusesNewPreviews(t *testing.T) {
	h := &Hub{Signer: NewSigner(), Host: "127.0.0.1"}
	h.CloseAll()
	if _, err := h.Open("a", "x:1"); err == nil {
		t.Fatal("expected an error after CloseAll")
	}
}

func TestFilterCookieHeader(t *testing.T) {
	got := filterCookieHeader([]string{"a=1; unire_session=zzz", "b=2;unire_x=3;  c=4"})
	if got != "a=1; b=2; c=4" {
		t.Fatalf("got %q", got)
	}
	if filterCookieHeader([]string{"unire_session=zzz"}) != "" || filterCookieHeader(nil) != "" {
		t.Fatal("nothing left must give an empty header")
	}
}

func freePorts(t *testing.T, n int) []int {
	t.Helper()
	var ports []int
	var lns []net.Listener
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		lns = append(lns, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, ln := range lns {
		ln.Close()
	}
	return ports
}

func drain(c chan *http.Request) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-c:
			default:
				close(done)
				return
			}
		}
	}()
	return done
}

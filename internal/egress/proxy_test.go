package egress

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// All test traffic is on loopback, which is a private address, so these tests
// exercise the "block private" and "allow by entry" paths for real.

type events struct {
	mu      sync.Mutex
	connect []string
	blocked []string
}

func (e *events) onConnect(d string) { e.mu.Lock(); e.connect = append(e.connect, d); e.mu.Unlock() }
func (e *events) onBlocked(d, r string) {
	e.mu.Lock()
	e.blocked = append(e.blocked, d+" / "+r)
	e.mu.Unlock()
}

// start runs a proxy and registers the loopback client with the policy.
func start(t *testing.T, pol Policy) (proxyURL *url.URL, ev *events, p *Proxy) {
	t.Helper()
	p = NewProxy()
	ev = &events{}
	p.Register("127.0.0.1", pol, ev.onConnect, ev.onBlocked)
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u, ev, p
}

func clientVia(proxy *url.URL) *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true},
	}
}

func originServer(t *testing.T) (*httptest.Server, string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "origin says hi")
	}))
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "http://")
}

func TestHTTPToPrivateAddressIsBlocked(t *testing.T) {
	origin, hostport := originServer(t)
	proxy, ev, _ := start(t, Policy{SessionID: "s1", Mode: ModeInternet})

	resp, err := clientVia(proxy).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "Blocked by Unire") || !strings.Contains(string(body), hostport) {
		t.Errorf("got %d %q, want a 403 that names %s", resp.StatusCode, body, hostport)
	}
	if len(ev.blocked) != 1 || !strings.Contains(ev.blocked[0], "private network address") {
		t.Errorf("blocked events = %v", ev.blocked)
	}
	// A second try is blocked again but logged only once.
	clientVia(proxy).Get(origin.URL)
	if len(ev.blocked) != 1 {
		t.Errorf("a repeated block should be logged once, got %v", ev.blocked)
	}
}

func TestHTTPAllowedByEntry(t *testing.T) {
	origin, hostport := originServer(t)
	entry, err := ParseEntry(hostport)
	if err != nil {
		t.Fatal(err)
	}
	proxy, ev, _ := start(t, Policy{SessionID: "s1", Mode: ModeInternet, Entries: []Entry{entry}})

	resp, err := clientVia(proxy).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "origin says hi" {
		t.Errorf("got %d %q", resp.StatusCode, body)
	}
	if len(ev.connect) != 1 || ev.connect[0] != hostport || len(ev.blocked) != 0 {
		t.Errorf("connect=%v blocked=%v", ev.connect, ev.blocked)
	}
}

func TestLocalhostByNameIsBlocked(t *testing.T) {
	_, hostport := originServer(t)
	_, port, _ := net.SplitHostPort(hostport)
	proxy, ev, _ := start(t, Policy{SessionID: "s1", Mode: ModeInternet})
	resp, err := clientVia(proxy).Get("http://localhost:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || len(ev.blocked) != 1 {
		t.Errorf("a name that resolves to loopback must be blocked: %d %v", resp.StatusCode, ev.blocked)
	}
}

// connectThrough opens a tunnel and returns the status line.
func connectThrough(t *testing.T, proxy *url.URL, dest string) (string, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxy.Host, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", dest, dest)
	br := bufio.NewReader(c)
	status, _ := br.ReadString('\n')
	return strings.TrimSpace(status), c, br
}

func TestConnectBlockedAndAllowed(t *testing.T) {
	// A TCP echo server on loopback.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	dest := l.Addr().String()

	proxy, ev, _ := start(t, Policy{SessionID: "s1", Mode: ModeInternet})
	status, c, _ := connectThrough(t, proxy, dest)
	c.Close()
	if !strings.Contains(status, "403") || len(ev.blocked) != 1 {
		t.Errorf("CONNECT to loopback: %q blocked=%v", status, ev.blocked)
	}

	entry, _ := ParseEntry(dest)
	proxy2, ev2, _ := start(t, Policy{SessionID: "s2", Mode: ModeInternet, Entries: []Entry{entry}})
	status, c, br := connectThrough(t, proxy2, dest)
	defer c.Close()
	if !strings.Contains(status, "200") {
		t.Fatalf("allowed CONNECT = %q", status)
	}
	br.ReadString('\n') // blank line ending the response headers
	fmt.Fprint(c, "ping\n")
	line, _ := br.ReadString('\n')
	if line != "ping\n" {
		t.Errorf("tunnel did not carry data: %q", line)
	}
	if len(ev2.connect) != 1 {
		t.Errorf("connect events = %v", ev2.connect)
	}
}

func TestUnknownClientIsRefused(t *testing.T) {
	p := NewProxy() // nobody registered
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	origin, _ := originServer(t)
	resp, err := clientVia(u).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unregistered client got %d, want 403", resp.StatusCode)
	}
}

func TestUnregisterStopsAccess(t *testing.T) {
	origin, hostport := originServer(t)
	entry, _ := ParseEntry(hostport)
	proxy, _, p := start(t, Policy{Mode: ModeInternet, Entries: []Entry{entry}})
	if resp, err := clientVia(proxy).Get(origin.URL); err != nil || resp.StatusCode != 200 {
		t.Fatalf("before unregister: %v %v", resp, err)
	}
	p.Unregister("127.0.0.1")
	if resp, err := clientVia(proxy).Get(origin.URL); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("after unregister want 403, got %v %v", resp, err)
	}
}

func TestPlainRequestsWithoutProxyFormAreRejected(t *testing.T) {
	proxy, _, _ := start(t, Policy{Mode: ModeFull})
	resp, err := http.Get(proxy.String() + "/not-absolute")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

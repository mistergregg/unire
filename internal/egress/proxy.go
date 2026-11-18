package egress

import (
	"context"
	"errors"
	"fmt"
	"github.com/mistergregg/unire/internal/safe"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BlockedError is returned when the policy refuses a destination.
type BlockedError struct {
	Dest   string
	Reason string
}

func (e *BlockedError) Error() string { return "blocked " + e.Dest + " (" + e.Reason + ")" }

// registration is one sandbox known to the proxy, found by its address.
type registration struct {
	pol       Policy
	onConnect func(dest string)
	onBlocked func(dest, reason string)
	transport *http.Transport

	mu   sync.Mutex
	seen map[string]bool
}

// once calls f the first time it sees key, so a chatty agent does not flood the log.
func (r *registration) once(kind, key string, f func()) {
	if f == nil {
		return
	}
	r.mu.Lock()
	k := kind + " " + key
	first := !r.seen[k]
	r.seen[k] = true
	r.mu.Unlock()
	if first {
		f()
	}
}

// Proxy is an HTTP and HTTPS (CONNECT) forward proxy that applies the policy
// of whichever sandbox is calling. A sandbox is identified by its address on
// the sandbox network, which only the proxy can see and the sandbox cannot
// choose.
type Proxy struct {
	mu       sync.Mutex
	byIP     map[string]*registration
	resolver *net.Resolver
	dialer   net.Dialer
}

// NewProxy returns a proxy with no sandboxes registered.
func NewProxy() *Proxy {
	return &Proxy{
		byIP:     map[string]*registration{},
		resolver: net.DefaultResolver,
		dialer:   net.Dialer{Timeout: 15 * time.Second},
	}
}

// Register tells the proxy which policy applies to connections from ip.
// onConnect and onBlocked (either may be nil) are called once per destination.
func (p *Proxy) Register(ip string, pol Policy, onConnect func(dest string), onBlocked func(dest, reason string)) {
	r := &registration{pol: pol, onConnect: onConnect, onBlocked: onBlocked, seen: map[string]bool{}}
	r.transport = &http.Transport{
		DialContext:           func(ctx context.Context, network, addr string) (net.Conn, error) { return p.dial(ctx, r, addr) },
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 10 * time.Minute,
	}
	p.mu.Lock()
	p.byIP[ip] = r
	p.mu.Unlock()
}

// Policy returns the policy registered for a sandbox address, if any.
func (p *Proxy) Policy(ip string) (Policy, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r := p.byIP[ip]; r != nil {
		return r.pol, true
	}
	return Policy{}, false
}

// Decide reports whether the policy registered for sandbox address ip allows
// a connection to dest (host:port) that resolves to addr.
func (p *Proxy) Decide(ip, host string, addr net.IP, port int) (bool, string) {
	pol, ok := p.Policy(ip)
	if !ok {
		return false, "unknown sandbox"
	}
	return pol.decide(host, addr, port)
}

// Unregister forgets a sandbox.
func (p *Proxy) Unregister(ip string) {
	p.mu.Lock()
	r := p.byIP[ip]
	delete(p.byIP, ip)
	p.mu.Unlock()
	if r != nil {
		r.transport.CloseIdleConnections()
	}
}

func (p *Proxy) lookup(remoteAddr string) *registration {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byIP[host]
}

// dial resolves the destination itself and connects to an address the policy
// allows. Connecting to the checked address, not the name, stops a name from
// resolving differently the second time.
func (p *Proxy) dial(ctx context.Context, r *registration, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("bad address %q", addr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("bad port in %q", addr)
	}
	host = strings.Trim(host, "[]")
	dest := net.JoinHostPort(host, portStr)

	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		found, err := p.resolver.LookupIPAddr(ctx, host)
		if err != nil || len(found) == 0 {
			return nil, fmt.Errorf("cannot resolve %s", host)
		}
		for _, f := range found {
			ips = append(ips, f.IP)
		}
	}

	var lastErr error
	var reason string
	tried := false
	for _, ip := range ips {
		ok, why := r.pol.decide(host, ip, port)
		if !ok {
			reason = why
			continue
		}
		tried = true
		conn, err := p.dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), portStr))
		if err == nil {
			r.once("connect", dest, func() { r.onConnect(dest) })
			return conn, nil
		}
		lastErr = err
	}
	if !tried {
		r.once("blocked", dest, func() { r.onBlocked(dest, reason) })
		return nil, &BlockedError{Dest: dest, Reason: reason}
	}
	return nil, lastErr
}

// Handler returns the proxy's HTTP handler.
func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r := p.lookup(req.RemoteAddr)
		if r == nil {
			http.Error(w, "Unire: this client is not a registered sandbox", http.StatusForbidden)
			return
		}
		if req.Method == http.MethodConnect {
			p.connect(w, req, r)
		} else {
			p.forward(w, req, r)
		}
	})
}

// Serve runs the proxy on l until it is closed.
func (p *Proxy) Serve(l net.Listener) error {
	srv := &http.Server{Handler: p.Handler(), ReadHeaderTimeout: 15 * time.Second}
	return srv.Serve(l)
}

func writeBlocked(w http.ResponseWriter, err error) {
	var be *BlockedError
	if errors.As(err, &be) {
		http.Error(w, "Blocked by Unire's network policy: "+be.Dest+" ("+be.Reason+"). "+
			"The owner can allow it in Unire's network settings.", http.StatusForbidden)
		return
	}
	http.Error(w, "Unire proxy: "+err.Error(), http.StatusBadGateway)
}

func (p *Proxy) connect(w http.ResponseWriter, req *http.Request, r *registration) {
	upstream, err := p.dial(req.Context(), r, req.Host)
	if err != nil {
		writeBlocked(w, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "cannot tunnel", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go func() {
		defer safe.Recover("proxy copy", nil)
		_, _ = io.Copy(upstream, client)
		closeWrite(upstream)
	}()
	_, _ = io.Copy(client, upstream)
	client.Close()
	upstream.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

var hopHeaders = []string{"Connection", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func (p *Proxy) forward(w http.ResponseWriter, req *http.Request, r *registration) {
	if !req.URL.IsAbs() || req.URL.Scheme != "http" {
		http.Error(w, "Unire proxy: only absolute http:// requests and CONNECT are supported", http.StatusBadRequest)
		return
	}
	out := req.Clone(req.Context())
	out.RequestURI = ""
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	resp, err := r.transport.RoundTrip(out)
	if err != nil {
		writeBlocked(w, err)
		return
	}
	defer resp.Body.Close()
	for _, h := range hopHeaders {
		resp.Header.Del(h)
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

package preview

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// LinkTTL is how long the URL that opens a preview works.
	LinkTTL = 2 * time.Minute
	// SessionTTL is how long the browser stays signed in to one preview.
	SessionTTL = 12 * time.Hour

	linkParam    = "unire_token"
	cookiePrefix = "unire_" // cookies with this prefix never reach the app, and the app cannot set them
)

func sessionCookieName(port int) string { return cookiePrefix + "preview_" + strconv.Itoa(port) }

// Hub gives each running preview its own port from a range and proxies that
// port to the preview container.
//
// Why a port per preview and not a path on the dashboard: an app such as a
// Flutter web build expects to live at the root of its site. Browsers share
// cookies between ports of the same host, so the proxy hides Unire's own
// cookies from the app and refuses to let the app set cookies with Unire's
// prefix (see filterCookieHeader and dropUnireSetCookies).
type Hub struct {
	Signer *Signer
	// Host is the address to listen on. Empty means all addresses, which is what
	// the Docker port mapping needs.
	Host string
	// Low and High are the first and last port to use. Low 0 lets the system pick
	// any free port (for tests).
	Low, High int

	mu     sync.Mutex
	byKey  map[string]*listener
	closed bool
}

type listener struct {
	port   int
	target string
	ln     net.Listener
	srv    *http.Server
}

// stop closes the listener itself as well as the server: if the server's
// goroutine has not started yet, closing only the server would leave the port
// bound for a moment and a quick reopen would fail.
func (l *listener) stop() {
	l.srv.Close()
	l.ln.Close()
}

// Open starts proxying a free port of the range to target ("host:port" on the
// container network) and returns that port. Calling it again for the same key
// with the same target returns the same port.
func (h *Hub) Open(key, target string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, fmt.Errorf("preview proxy is shut down")
	}
	if h.byKey == nil {
		h.byKey = map[string]*listener{}
	}
	if l, ok := h.byKey[key]; ok {
		if l.target == target {
			return l.port, nil
		}
		l.stop() // the container moved: serve the new target on a new port
		delete(h.byKey, key)
	}
	ln, port, err := h.listen()
	if err != nil {
		return 0, err
	}
	l := &listener{port: port, target: target, ln: ln}
	l.srv = &http.Server{Handler: h.handler(key, port, target), ReadHeaderTimeout: 10 * time.Second}
	h.byKey[key] = l
	go l.srv.Serve(ln)
	return port, nil
}

func (h *Hub) listen() (net.Listener, int, error) {
	if h.Low == 0 && h.High == 0 {
		ln, err := net.Listen("tcp", net.JoinHostPort(h.Host, "0"))
		if err != nil {
			return nil, 0, err
		}
		return ln, ln.Addr().(*net.TCPAddr).Port, nil
	}
	taken := map[int]bool{}
	for _, l := range h.byKey {
		taken[l.port] = true
	}
	for p := h.Low; p <= h.High; p++ {
		if taken[p] {
			continue
		}
		if ln, err := net.Listen("tcp", net.JoinHostPort(h.Host, strconv.Itoa(p))); err == nil {
			return ln, p, nil
		}
	}
	return nil, 0, fmt.Errorf("no free preview port between %d and %d", h.Low, h.High)
}

// Port returns the port a preview is served on.
func (h *Hub) Port(key string) (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.byKey[key]
	if !ok {
		return 0, false
	}
	return l.port, true
}

// LinkToken returns a token for the URL that opens the preview: append it as
// ?unire_token=... to the preview's address.
func (h *Hub) LinkToken(key string) string { return h.Signer.Mint(key, PurposeLink, LinkTTL) }

// Close stops serving a preview.
func (h *Hub) Close(key string) {
	h.mu.Lock()
	l, ok := h.byKey[key]
	delete(h.byKey, key)
	h.mu.Unlock()
	if ok {
		l.stop()
	}
}

// CloseAll stops every preview and refuses new ones.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	h.closed = true
	all := h.byKey
	h.byKey = nil
	h.mu.Unlock()
	for _, l := range all {
		l.stop()
	}
}

func (h *Hub) handler(key string, port int, target string) http.Handler {
	up := &url.URL{Scheme: "http", Host: target}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(up)
			pr.Out.Host = pr.In.Host // some dev servers check it
			pr.Out.Header.Set("Cookie", filterCookieHeader(pr.In.Header.Values("Cookie")))
			if pr.Out.Header.Get("Cookie") == "" {
				pr.Out.Header.Del("Cookie")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			dropUnireSetCookies(resp.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			page(w, http.StatusBadGateway, "The preview is not answering",
				"The app has not started yet, or it stopped. Check the preview log in Unire.")
		},
	}
	cname := sessionCookieName(port)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Someone arriving with a link token trades it for a cookie and is
		//    sent to the same address without the token in it.
		if tok := r.URL.Query().Get(linkParam); tok != "" {
			if _, ok := h.Signer.Verify(key, PurposeLink, tok); !ok {
				page(w, http.StatusUnauthorized, "This link has expired", "Open the preview again from Unire.")
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: cname, Value: h.Signer.Mint(key, PurposeSession, SessionTTL), Path: "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
				MaxAge: int(SessionTTL.Seconds()),
			})
			q := r.URL.Query()
			q.Del(linkParam)
			dest := *r.URL
			dest.RawQuery = q.Encode()
			// A path like //host would be read by a browser as another site.
			dest.Path = "/" + strings.TrimLeft(dest.Path, "/")
			dest.RawPath = ""
			w.Header().Set("Referrer-Policy", "no-referrer")
			http.Redirect(w, r, dest.RequestURI(), http.StatusSeeOther)
			return
		}
		// 2. Everyone else needs the cookie.
		c, err := r.Cookie(cname)
		if err != nil {
			page(w, http.StatusUnauthorized, "Open this preview from Unire", "This address only works from the preview tab in Unire.")
			return
		}
		if _, ok := h.Signer.Verify(key, PurposeSession, c.Value); !ok {
			page(w, http.StatusUnauthorized, "Your preview session ended", "Open the preview again from Unire.")
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// filterCookieHeader returns the Cookie header to send to the app: everything
// except Unire's own cookies (the dashboard login and the preview sessions).
func filterCookieHeader(values []string) string {
	var keep []string
	for _, v := range values {
		for _, part := range strings.Split(v, ";") {
			part = strings.TrimSpace(part)
			if part == "" || strings.HasPrefix(part, cookiePrefix) {
				continue
			}
			keep = append(keep, part)
		}
	}
	return strings.Join(keep, "; ")
}

// dropUnireSetCookies removes cookies the app tries to set with Unire's prefix.
// Browsers share cookies between ports of one host, so an app could otherwise
// overwrite or clear the owner's dashboard login.
func dropUnireSetCookies(h http.Header) {
	vals := h.Values("Set-Cookie")
	if len(vals) == 0 {
		return
	}
	h.Del("Set-Cookie")
	for _, v := range vals {
		if strings.HasPrefix(strings.TrimSpace(v), cookiePrefix) {
			continue
		}
		h.Add("Set-Cookie", v)
	}
}

func page(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><meta name=viewport content="width=device-width"><title>%s</title>
<body style="font:15px system-ui,sans-serif;max-width:28em;margin:15vh auto;padding:0 1em;color:#334155">
<h2 style="margin:0 0 .4em">%s</h2><p>%s</p>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(body))
}

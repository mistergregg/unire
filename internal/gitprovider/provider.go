// Package gitprovider holds git server plugins. Each provider type is one
// file that registers itself; the rest of Unire only uses this interface.
package gitprovider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Repo is a repository on a git server.
type Repo struct {
	FullName      string `json:"fullName"`
	Description   string `json:"description"`
	CloneURL      string `json:"cloneUrl"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
}

// Config is what a provider needs to talk to one server.
type Config struct {
	Type               string
	BaseURL            string // server root, e.g. https://git.example.com
	Token              string
	InsecureSkipVerify bool // for self-signed certificates on home servers
}

// Provider is one connected git server.
type Provider interface {
	// TestConnection checks the token and returns the authenticated user name.
	TestConnection(ctx context.Context) (string, error)
	// ListRepos returns repos the token can see, filtered by a name query.
	ListRepos(ctx context.Context, query string) ([]Repo, error)
}

// Factory builds a provider from config.
type Factory func(Config) (Provider, error)

// Info describes a provider type. The web UI builds its "add server" form
// from this, so a new provider needs no UI changes.
type Info struct {
	Type            string `json:"type"`  // stable id, e.g. "gitlab"
	Label           string `json:"label"` // shown in the UI, e.g. "GitLab"
	BaseURLRequired bool   `json:"baseUrlRequired"`
	BaseURLHint     string `json:"baseUrlHint"` // e.g. "leave empty for gitlab.com"
	TokenHelp       string `json:"tokenHelp"`   // where to create the token and which scopes it needs
}

type registration struct {
	info    Info
	factory Factory
}

var registry = map[string]registration{}

// Register adds a provider type. Providers call it from init.
func Register(info Info, f Factory) { registry[info.Type] = registration{info, f} }

// Infos lists registered provider types, sorted by label.
func Infos() []Info {
	out := make([]Info, 0, len(registry))
	for _, r := range registry {
		out = append(out, r.info)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

// Types lists registered type ids, sorted.
func Types() []string {
	t := make([]string, 0, len(registry))
	for k := range registry {
		t = append(t, k)
	}
	sort.Strings(t)
	return t
}

// New builds a provider for cfg.Type.
func New(cfg Config) (Provider, error) {
	r, ok := registry[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("unknown git server type %q (have: %s)", cfg.Type, strings.Join(Types(), ", "))
	}
	if r.info.BaseURLRequired && cfg.BaseURL == "" {
		return nil, fmt.Errorf("%s needs a server URL", r.info.Label)
	}
	return r.factory(cfg)
}

// client is shared HTTP plumbing for API-based providers.
type client struct {
	http    *http.Client
	baseURL string
	header  http.Header
}

func newClient(cfg Config, base string, header http.Header) *client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for self-signed home servers
	}
	return &client{
		http:    &http.Client{Timeout: 20 * time.Second, Transport: tr},
		baseURL: strings.TrimRight(base, "/"),
		header:  header,
	}
}

func (c *client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	for k, v := range c.header {
		req.Header[k] = v
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &httpError{Status: resp.StatusCode, msg: "the server rejected the token (HTTP " + fmt.Sprint(resp.StatusCode) + ")" + serverReason(resp, body)}
	case resp.StatusCode == http.StatusNotFound:
		return &httpError{Status: resp.StatusCode, msg: "not found (HTTP 404): check the server URL and type"}
	case resp.StatusCode >= 300:
		return &httpError{Status: resp.StatusCode, msg: fmt.Sprintf("unexpected response (HTTP %d)", resp.StatusCode)}
	}
	return json.Unmarshal(body, out)
}

// httpError is an error response from a git server's API.
type httpError struct {
	Status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

// serverReason adds why the server said no, which is usually the fastest way
// to see a missing token permission. It uses only the "message" or "error"
// field of a JSON reply, shortened, and never echoes a web page. A web page
// instead of JSON usually means a proxy or firewall in front of the server.
func serverReason(resp *http.Response, body []byte) string {
	var j struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &j) == nil {
		m := strings.TrimSpace(j.Message)
		if m == "" {
			m = strings.TrimSpace(j.Error)
		}
		m = strings.Map(func(r rune) rune {
			if r < 32 {
				return ' '
			}
			return r
		}, m)
		if len(m) > 200 {
			m = m[:200] + "…"
		}
		if m != "" {
			return ": " + m
		}
		return ""
	}
	trimmed := strings.TrimSpace(string(body))
	if strings.Contains(resp.Header.Get("Content-Type"), "text/html") || strings.HasPrefix(trimmed, "<") {
		return ". The reply was a web page, not an API answer, so a proxy or firewall in front of the server may be blocking Unire"
	}
	return ""
}

// hasStatus reports whether err is an API error with the given status.
func hasStatus(err error, status int) bool {
	var he *httpError
	return errors.As(err, &he) && he.Status == status
}

func matches(r Repo, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	return q == "" || strings.Contains(strings.ToLower(r.FullName), q) ||
		strings.Contains(strings.ToLower(r.Description), q)
}

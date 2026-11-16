package sessions

import (
	"context"
	"errors"
	"net"
	"net/url"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/egress"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/session"
	"github.com/mistergregg/unire/internal/store"
)

func networkLabel(m egress.Mode) string {
	switch m {
	case egress.ModeFull:
		return "full access (no filtering)"
	case egress.ModeAgentOnly:
		return "agent only (the agent's own service, your git servers and allowed addresses)"
	}
	return "internet only (private networks blocked)"
}

// gitEntries allows each repo's git server (host and port) in every
// protected mode, because cloning and pushing must work even when the server
// is on the private network.
func gitEntries(repos []session.Repo) []egress.Entry {
	var out []egress.Entry
	seen := map[string]bool{}
	for _, r := range repos {
		u, err := url.Parse(r.URL)
		if err != nil || u.Hostname() == "" {
			continue
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
		}
		if port == "" {
			continue
		}
		key := net.JoinHostPort(u.Hostname(), port)
		if seen[key] {
			continue
		}
		seen[key] = true
		if e, err := egress.ParseEntry(key); err == nil {
			out = append(out, e)
		}
	}
	return out
}

func (m *Manager) policyFor(spec session.Spec, mode egress.Mode, cfg Config) (egress.Policy, error) {
	ad, err := agent.Get(spec.Agent)
	if err != nil {
		return egress.Policy{}, err
	}
	// The owner's allowed addresses apply in both protected modes. In agent-only
	// mode they are also the way to add a host the agent turns out to need.
	entries := append(gitEntries(spec.Repos), cfg.Egress.Allow...)
	return egress.Policy{SessionID: spec.ID, Mode: mode, Entries: entries, AgentHosts: ad.Hosts()}, nil
}

// netRunner puts sandboxes on the protected network and registers each one
// with the proxy before anything runs inside it.
type netRunner struct {
	inner runner.Runner
	proxy *egress.Proxy
	info  egress.Info
	pol   egress.Policy
	st    *store.Store
	id    string
}

func (n *netRunner) Start(ctx context.Context, s runner.StartSpec) (runner.Sandbox, error) {
	s.Network = n.info.Network
	env := map[string]string{}
	for k, v := range s.Env {
		env[k] = v
	}
	for k, v := range n.info.ProxyEnv() {
		env[k] = v
	}
	s.Env = env

	sb, err := n.inner.Start(ctx, s)
	if err != nil {
		return nil, err
	}
	stop := func() {
		c, cancel := context.WithTimeout(context.Background(), 30e9)
		defer cancel()
		_ = sb.Stop(c)
	}
	a, ok := sb.(runner.Addressable)
	if !ok {
		stop()
		return nil, errors.New("this sandbox type cannot be network-protected")
	}
	ip, err := a.Address(ctx, n.info.Network)
	if err != nil {
		stop()
		return nil, err
	}
	n.proxy.Register(ip, n.pol,
		func(dest string) { _ = n.st.AddEvent(n.id, "net", dest) },
		func(dest, reason string) { _ = n.st.AddEvent(n.id, "blocked", "blocked "+dest+" ("+reason+")") },
	)
	return &netSandbox{Sandbox: sb, unregister: func() { n.proxy.Unregister(ip) }}, nil
}

type netSandbox struct {
	runner.Sandbox
	unregister func()
}

func (s *netSandbox) Stop(ctx context.Context) error {
	s.unregister()
	return s.Sandbox.Stop(ctx)
}

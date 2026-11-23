package sessions

import (
	"errors"
	"fmt"

	"github.com/mistergregg/unire/internal/egress"
)

// PreviewAccess is what a preview container of a conversation needs: the
// volume with its repos, and a place on the protected network under the same
// rules as the chat that asked for it.
type PreviewAccess struct {
	Workspace string            // docker volume holding the conversation's repos
	Root      string            // the conversation's first session
	Network   string            // docker network to join
	Env       map[string]string // points the app's tools at the egress proxy
	// Register tells the egress proxy about the preview container (by its IP on
	// Network) and returns the function that forgets it again.
	Register func(ip string) (unregister func())
}

// PreviewAccess works out how a preview for session id gets onto the network.
// A preview always goes through the egress proxy, so its package downloads
// follow the chat's network mode: "internet only" and "agent only" are applied
// as they are, and "full access" becomes a policy that allows everything.
// Previews need the protected network, so they are unavailable when Unire is
// not running in Docker with the socket mounted.
func (m *Manager) PreviewAccess(id string) (PreviewAccess, error) {
	s, ok, err := m.st.Session(id)
	if err != nil {
		return PreviewAccess{}, err
	}
	if !ok {
		return PreviewAccess{}, errors.New("no such chat")
	}
	cfg := m.config()
	if !cfg.Egress.Info.Available {
		return PreviewAccess{}, fmt.Errorf("previews are not available here: %s", cfg.Egress.Info.Reason)
	}
	mode := egress.Mode(s.Network)
	if mode == "" {
		mode = egress.ModeInternet
	}
	if _, ok := egress.ParseMode(string(mode)); !ok {
		return PreviewAccess{}, fmt.Errorf("unknown network mode %q", s.Network)
	}
	pol := previewPolicy(id, mode, cfg.Egress.Allow)
	root := m.rootOf(s.ID, s.ParentID)
	proxy, st := cfg.Egress.Proxy, m.st
	return PreviewAccess{
		Workspace: workspaceName(root),
		Root:      root,
		Network:   cfg.Egress.Info.Network,
		Env:       cfg.Egress.Info.ProxyEnv(),
		Register: func(ip string) func() {
			proxy.Register(ip, pol,
				func(dest string) { _ = st.AddEvent(id, "net", dest) },
				func(dest, reason string) { _ = st.AddEvent(id, "blocked", "blocked "+dest+" ("+reason+")") },
			)
			return func() { proxy.Unregister(ip) }
		},
	}, nil
}

// previewPolicy is the chat's network rules as they apply to a preview: the
// owner's allowed addresses in the two protected modes, nothing else. A
// preview needs no git server and no agent service.
func previewPolicy(id string, mode egress.Mode, allow []egress.Entry) egress.Policy {
	return egress.Policy{SessionID: id, Mode: mode, Entries: allow}
}

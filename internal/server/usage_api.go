package server

import (
	"context"
	"net/http"
	"time"

	"github.com/mistergregg/unire/internal/agent"
)

const usageStaleDuration = 10 * time.Minute

// usageFor returns the usage reading for agentID, checking the database first,
// and if appropriate (e.g. Codex offline check when missing or stale), querying
// the CLI directly.
func (s *Server) usageFor(ctx context.Context, agentID string) agent.AgentUsage {
	// Antigravity CLI does not report usage limits.
	if agentID == "antigravity" {
		return agent.DefaultUsage(agentID)
	}

	u, ok, err := s.store.AgentUsage(agentID)
	if err != nil || !ok {
		u = agent.DefaultUsage(agentID)
	}

	now := s.timeNow()
	isStale := u.UpdatedAt == 0 || now.Sub(time.Unix(u.UpdatedAt, 0)) > usageStaleDuration

	// Codex can be queried cheaply offline when no chat is running.
	if agentID == "codex" && (u.UpdatedAt == 0 || isStale) {
		if s.sessions == nil || !s.sessions.Running() {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if fresh, err := agent.QueryCodexUsage(cctx); err == nil && fresh != nil {
				fresh.UpdatedAt = now.Unix()
				_ = s.store.SaveUsage(*fresh)
				fresh.Stale = false
				return *fresh
			}
		}
	}

	if u.Reported {
		u.Stale = isStale
	}
	return u
}

// allUsage returns the usage reading for every registered agent in agent.Infos() order.
func (s *Server) allUsage(ctx context.Context) []agent.AgentUsage {
	infos := agent.Infos()
	out := make([]agent.AgentUsage, len(infos))
	for i, in := range infos {
		out[i] = s.usageFor(ctx, in.ID)
	}
	return out
}

func (s *Server) getUsage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.allUsage(r.Context()))
}

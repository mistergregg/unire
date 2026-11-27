package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/safe"
)

const (
	// usageStaleAfter: an older reading is shown as out of date.
	usageStaleAfter = 10 * time.Minute
	// usageRefreshAfter: an agent that can be asked outside a chat is asked
	// again once its reading is this old, so an open page rarely shows a stale one.
	usageRefreshAfter = 5 * time.Minute
	// usageRetryAfter: whatever a try gave, the next waits at least this long.
	usageRetryAfter = time.Minute
	// usageProbeTimeout bounds one try (a throwaway sandbox and one request).
	usageProbeTimeout = 45 * time.Second
)

// usageProbes remembers, per agent, the last time Unire asked the agent for
// its usage outside a chat, whether it is asking now, and why the last try failed.
type usageProbes struct {
	mu   sync.Mutex
	last map[string]time.Time
	busy map[string]bool
	note map[string]string
	wg   sync.WaitGroup
}

// usageFor returns the latest usage reading for agentID. It never waits for an
// agent: when the reading is old and the agent can be asked cheaply, a
// background try is started and its answer shows up on a later request.
func (s *Server) usageFor(agentID string) agent.AgentUsage {
	ad, err := agent.Get(agentID)
	if err != nil {
		return agent.DefaultUsage(agentID)
	}
	u := agent.DefaultUsage(agentID)
	u.Label = ad.Info().Label
	if !u.Reported {
		return u // this CLI reports no usage limits
	}
	if got, ok, err := s.store.AgentUsage(agentID); err == nil && ok {
		got.Label = u.Label
		u = got
	}
	now := s.timeNow()
	age := now.Sub(time.Unix(u.UpdatedAt, 0))
	u.Stale = u.UpdatedAt != 0 && age > usageStaleAfter
	if u.UpdatedAt == 0 || age > usageRefreshAfter {
		s.refreshUsage(ad.Info(), now)
	}
	s.usage.mu.Lock()
	if u.UpdatedAt == 0 || u.Stale {
		u.Note = s.usage.note[agentID]
	}
	s.usage.mu.Unlock()
	return u
}

// canProbeOnline reports whether Unire can run a short online probe for agentID
// right now (the runner supports RunOnline, no chat is using the agent, and if
// requireLogin is true the agent is not known to be signed out).
func (s *Server) canProbeOnline(agentID string, requireLogin bool) (runner.OnlineShot, bool) {
	shot, ok := s.runnerFactory(s.sandbox()).(runner.OnlineShot)
	if !ok {
		return nil, false
	}
	if s.sessions != nil && s.sessions.AgentBusy(agentID) {
		return nil, false
	}
	if requireLogin {
		s.signins.mu.Lock()
		st, seen := s.signins.m[agentID]
		s.signins.mu.Unlock()
		if seen && st.known && !st.loggedIn {
			return nil, false
		}
	}
	return shot, true
}

// refreshUsage starts a background try to read the agent's usage, unless the
// agent cannot be asked outside a chat, it is signed out, a chat with it is
// queued or running (they share the login and could both refresh it at once),
// a try is under way, or the last try was less than usageRetryAfter ago.
func (s *Server) refreshUsage(info agent.Info, now time.Time) {
	if len(info.UsageArgv) == 0 || info.ParseUsage == nil {
		return
	}
	shot, ok := s.canProbeOnline(info.ID, true)
	if !ok {
		return
	}

	p := &s.usage
	p.mu.Lock()
	if p.busy[info.ID] || now.Sub(p.last[info.ID]) < usageRetryAfter {
		p.mu.Unlock()
		return
	}
	p.busy[info.ID], p.last[info.ID] = true, now
	p.wg.Add(1)
	p.mu.Unlock()

	go func() {
		defer p.wg.Done()
		note := ""
		defer func() {
			p.mu.Lock()
			p.busy[info.ID], p.note[info.ID] = false, note
			p.mu.Unlock()
		}()
		defer safe.Recover("usage of "+info.ID, nil)
		ctx, cancel := context.WithTimeout(context.Background(), usageProbeTimeout)
		defer cancel()
		out, _, err := shot.RunOnline(ctx, info.UsageArgv)
		if err != nil {
			note = "could not ask Docker"
			return
		}
		u, err := info.ParseUsage(out)
		if err != nil {
			note = shorten(err.Error(), 160)
			return
		}
		u.AgentID, u.UpdatedAt = info.ID, s.timeNow().Unix()
		if err := s.store.SaveUsage(*u); err != nil {
			note = "could not save the reading"
		}
	}()
}

// allUsage returns the reading for every agent, in agent.Infos() order.
func (s *Server) allUsage() []agent.AgentUsage {
	infos := agent.Infos()
	out := make([]agent.AgentUsage, len(infos))
	for i, in := range infos {
		out[i] = s.usageFor(in.ID)
	}
	return out
}

func (s *Server) getUsage(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.allUsage())
}

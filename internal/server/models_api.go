package server

import (
	"context"
	"sync"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/safe"
)

const (
	// modelsRefreshAfter: an agent's discovered model list is refreshed in the
	// background once it is this old.
	modelsRefreshAfter = 15 * time.Minute
	// modelsRetryAfter: whatever a try gave, the next waits at least this long
	// (unless sign-in state changes).
	modelsRetryAfter = time.Minute
	// modelsProbeTimeout bounds one try (a throwaway sandbox and one request).
	modelsProbeTimeout = 45 * time.Second
)

type modelsProbes struct {
	mu    sync.Mutex
	last  map[string]time.Time
	busy  map[string]bool
	dirty map[string]bool
	wg    sync.WaitGroup
}

func (p *modelsProbes) invalidate() {
	p.mu.Lock()
	for _, info := range agent.Infos() {
		p.dirty[info.ID] = true
	}
	p.last = map[string]time.Time{}
	p.mu.Unlock()
}

// modelsFor returns the latest known models for info, falling back to
// info.Models until a live list has been discovered. It never waits for an
// agent: when the list is missing, dirty (after a sign-in change), or old, a
// background try is started and its result shows up on a later request.
func (s *Server) modelsFor(info agent.Info) []string {
	models := info.Models
	var updatedAt int64
	var hasSaved bool
	if got, at, ok, err := s.store.AgentModels(info.ID); err == nil && ok && len(got) > 0 {
		models, updatedAt, hasSaved = got, at, true
	}
	now := s.timeNow()
	s.models.mu.Lock()
	dirty := s.models.dirty[info.ID]
	s.models.mu.Unlock()
	if !hasSaved || dirty || now.Sub(time.Unix(updatedAt, 0)) > modelsRefreshAfter {
		s.refreshModels(info, now, false)
	}
	return models
}

func (s *Server) refreshAllModels(force bool) {
	now := s.timeNow()
	for _, info := range agent.Infos() {
		s.refreshModels(info, now, force)
	}
}

func (s *Server) refreshModels(info agent.Info, now time.Time, force bool) {
	if len(info.ModelsArgv) == 0 || info.ParseModels == nil {
		return
	}
	shot, ok := s.canProbeOnline(info.ID, info.ModelsRequireLogin)
	if !ok {
		return
	}
	p := &s.models
	p.mu.Lock()
	last, seen := p.last[info.ID]
	if p.busy[info.ID] || (!force && seen && now.Sub(last) < modelsRetryAfter) {
		p.mu.Unlock()
		return
	}
	p.busy[info.ID], p.last[info.ID] = true, now
	p.wg.Add(1)
	p.mu.Unlock()

	go func() {
		defer p.wg.Done()
		succeeded := false
		defer func() {
			p.mu.Lock()
			p.busy[info.ID] = false
			if succeeded {
				delete(p.dirty, info.ID)
			}
			p.mu.Unlock()
		}()
		defer safe.Recover("models of "+info.ID, nil)
		ctx, cancel := context.WithTimeout(context.Background(), modelsProbeTimeout)
		defer cancel()
		out, _, err := shot.RunOnline(ctx, info.ModelsArgv)
		if err != nil {
			return
		}
		models, err := info.ParseModels(out)
		if err != nil || len(models) == 0 {
			return
		}
		if err := s.store.SaveModels(info.ID, models, s.timeNow().Unix()); err == nil {
			succeeded = true
		}
	}()
}

package server

import (
	"context"
	"net/http"
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
	gen   map[string]uint64
	note  map[string]string
	wg    sync.WaitGroup
}

func (p *modelsProbes) invalidate() {
	p.mu.Lock()
	if p.dirty == nil {
		p.dirty = map[string]bool{}
	}
	if p.gen == nil {
		p.gen = map[string]uint64{}
	}
	for _, info := range agent.Infos() {
		p.dirty[info.ID] = true
		p.gen[info.ID]++
		delete(p.note, info.ID)
	}
	p.last = map[string]time.Time{}
	p.mu.Unlock()
}

// modelsMetaFor returns the latest known models for info, cache update timestamp,
// and any recent probe note, falling back to info.Models until a live list has
// been discovered. It never waits for an agent: when the list is missing, dirty
// (after a sign-in change), or old, a background try is started and its result
// shows up on a later request.
func (s *Server) modelsMetaFor(info agent.Info) ([]string, int64, string) {
	models := info.Models
	var updatedAt int64
	var hasSaved bool
	if got, at, ok, err := s.store.AgentModels(info.ID); err == nil && ok && len(got) > 0 {
		models, updatedAt, hasSaved = got, at, true
	}
	now := s.timeNow()
	s.models.mu.Lock()
	dirty := s.models.dirty[info.ID]
	note := s.models.note[info.ID]
	s.models.mu.Unlock()
	if !hasSaved || dirty || now.Sub(time.Unix(updatedAt, 0)) > modelsRefreshAfter {
		s.refreshModels(info, now, false)
	}
	return models, updatedAt, note
}

// refreshModelsEndpoint invalidates cached probe throttling and triggers a
// fresh model discovery pass before returning the updated agent list.
func (s *Server) refreshModelsEndpoint(w http.ResponseWriter, r *http.Request) {
	s.models.invalidate()
	s.refreshAllModels(true)
	s.listAgents(w, r)
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
	if p.gen == nil {
		p.gen = map[string]uint64{}
	}
	if p.note == nil {
		p.note = map[string]string{}
	}
	startGen := p.gen[info.ID]
	p.busy[info.ID], p.last[info.ID] = true, now
	p.wg.Add(1)
	p.mu.Unlock()

	go func() {
		defer p.wg.Done()
		succeeded := false
		var probeNote string
		defer func() {
			p.mu.Lock()
			p.busy[info.ID] = false
			if p.gen[info.ID] == startGen {
				if succeeded {
					delete(p.dirty, info.ID)
					delete(p.note, info.ID)
				} else if probeNote != "" {
					p.note[info.ID] = probeNote
				}
			}
			p.mu.Unlock()
		}()
		defer safe.Recover("models of "+info.ID, nil)
		base := s.bgCtx
		if base == nil {
			base = context.Background()
		}
		ctx, cancel := context.WithTimeout(base, modelsProbeTimeout)
		defer cancel()
		out, _, err := shot.RunOnline(ctx, info.ModelsArgv)
		if base.Err() != nil {
			return
		}
		if err != nil {
			probeNote = shorten(err.Error(), 120)
			return
		}
		models, err := info.ParseModels(out)
		if err != nil {
			probeNote = shorten(err.Error(), 120)
			return
		}
		if len(models) == 0 {
			probeNote = "no models returned"
			return
		}
		p.mu.Lock()
		sameGen := p.gen[info.ID] == startGen
		p.mu.Unlock()
		if !sameGen {
			return
		}
		if err := s.store.SaveModels(info.ID, models, s.timeNow().Unix()); err == nil {
			succeeded = true
		} else {
			probeNote = "could not save models"
		}
	}()
}

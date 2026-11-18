package server

import (
	"context"
	"github.com/mistergregg/unire/internal/safe"
	"net/http"
	"strconv"
	"time"
)

// Limits are how long things may run and how long they are kept.
type Limits struct {
	MaxRunMinutes     int `json:"maxRunMinutes"`     // the longest one run may take; 0 means no limit
	KeepLogsDays      int `json:"keepLogsDays"`      // activity lines older than this are deleted; 0 keeps them
	KeepWorkspaceDays int `json:"keepWorkspaceDays"` // workspace volumes of conversations idle this long are removed; 0 keeps them
}

// The defaults: a run longer than four hours is almost certainly stuck, activity
// lines are the bulk of the database, and a workspace is cheap to clone again.
const (
	defaultMaxRunMinutes = 240
	defaultKeepLogsDays  = 90
	defaultKeepWorkspace = 14
)

func (s *Server) limits() Limits {
	get := func(key string, def int) int {
		if v, ok, _ := s.store.Setting(key); ok {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				return n
			}
		}
		return def
	}
	return Limits{
		MaxRunMinutes:     get("max_run_minutes", defaultMaxRunMinutes),
		KeepLogsDays:      get("keep_logs_days", defaultKeepLogsDays),
		KeepWorkspaceDays: get("keep_workspace_days", defaultKeepWorkspace),
	}
}

func (s *Server) getLimits(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.limits())
}

func (s *Server) putLimits(w http.ResponseWriter, r *http.Request) {
	var l Limits
	if !readJSON(w, r, &l) {
		return
	}
	switch {
	case l.MaxRunMinutes < 0 || l.MaxRunMinutes > 7*24*60:
		writeErr(w, http.StatusBadRequest, "the longest run should be between 0 (no limit) and 10,080 minutes")
		return
	case l.KeepLogsDays < 0 || l.KeepLogsDays > 3650 || l.KeepWorkspaceDays < 0 || l.KeepWorkspaceDays > 3650:
		writeErr(w, http.StatusBadRequest, "days should be between 0 (keep for ever) and 3,650")
		return
	}
	for k, v := range map[string]int{"max_run_minutes": l.MaxRunMinutes, "keep_logs_days": l.KeepLogsDays, "keep_workspace_days": l.KeepWorkspaceDays} {
		if err := s.store.SetSetting(k, strconv.Itoa(v)); err != nil {
			serverError(w)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.limits())
}

// cleanupEvery is how often old logs and workspaces are looked at.
const cleanupEvery = 6 * time.Hour

// runCleanup removes old logs and idle workspaces, a little after start-up and then
// every few hours, until the server closes.
func (s *Server) runCleanup(stop <-chan struct{}) {
	do := func() {
		defer safe.Recover("cleanup", nil)
		l := s.limits()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		day := 24 * time.Hour
		s.sessions.Cleanup(ctx, time.Duration(l.KeepLogsDays)*day, time.Duration(l.KeepWorkspaceDays)*day)
	}
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			do()
			t.Reset(cleanupEvery)
		}
	}
}

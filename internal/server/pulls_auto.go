package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Opening pull requests without asking. By default Unire asks the owner before it
// opens a pull request for a branch an agent pushed. The owner can let it open
// them as soon as the agent is done: a turn that pushed a branch and suggested a
// pull request for it (with the pr tool) then gets one, with the agent's title and
// description. Branches pushed by earlier turns are still offered as before.

const autoOpenSetting = "pulls_auto_open"

func (s *Server) autoOpenPulls() bool {
	v, _, _ := s.store.Setting(autoOpenSetting)
	return v == "on"
}

type pullSettings struct {
	AutoOpen bool `json:"autoOpen"`
}

func (s *Server) getPullSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, pullSettings{AutoOpen: s.autoOpenPulls()})
}

func (s *Server) putPullSettings(w http.ResponseWriter, r *http.Request) {
	var req pullSettings
	if !readJSON(w, r, &req) {
		return
	}
	v := "off"
	if req.AutoOpen {
		v = "on"
	}
	if err := s.store.SetSetting(autoOpenSetting, v); err != nil {
		serverError(w)
		return
	}
	writeJSON(w, http.StatusOK, pullSettings{AutoOpen: s.autoOpenPulls()})
}

// afterTurn runs when a turn's work is pushed. With auto-open on, it opens a pull
// request for each branch this turn pushed that has none yet, if the agent
// suggested one. It returns lines for the turn's log.
func (s *Server) afterTurn(ctx context.Context, sessionID string) []string {
	if !s.autoOpenPulls() {
		return nil
	}
	events, err := s.store.Events(sessionID, 0, 5000)
	if err != nil {
		return nil
	}
	suggested := false
	var pushed []string
	for _, e := range events {
		switch {
		case e.Kind == "prtitle":
			suggested = true
		case e.Kind == "log" && (strings.Contains(e.Text, ": pushed ") || strings.Contains(e.Text, ": also pushed ")):
			pushed = append(pushed, e.Text)
		}
	}
	if !suggested || len(pushed) == 0 {
		return nil
	}
	thread, err := s.threadOf(sessionID)
	if err != nil || len(thread) == 0 {
		return nil
	}
	props, err := s.pulls.Proposals(thread)
	if err != nil {
		return []string{"could not open a pull request: " + err.Error()}
	}
	var lines []string
	for _, p := range props {
		if !pushedNow(pushed, p.Branch) {
			continue // an earlier turn's branch: the owner is still asked about it
		}
		v, err := s.pulls.Open(ctx, thread, p)
		if err != nil {
			lines = append(lines, fmt.Sprintf("could not open a pull request for %s: %v", p.Repo, err))
			continue
		}
		lines = append(lines, fmt.Sprintf("opened pull request #%d in %s (Settings: open pull requests without asking)", v.Number, p.Repo))
	}
	return lines
}

// pushedNow reports whether one of a turn's push lines is for branch.
func pushedNow(lines []string, branch string) bool {
	for _, l := range lines {
		_, rest, _ := strings.Cut(l, "pushed ")
		name, _, _ := strings.Cut(rest, " ")
		if name == branch {
			return true
		}
	}
	return false
}

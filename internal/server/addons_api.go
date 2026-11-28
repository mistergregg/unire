package server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/mistergregg/unire/addons"
	"github.com/mistergregg/unire/internal/runner"
)

// Add-ons are optional tool sets for the sandbox (see package addons). The ones
// turned on are kept as a comma-separated list in the "addons" setting.

func (s *Server) enabledAddons() []string {
	v, _, _ := s.store.Setting("addons")
	return addons.Clean(strings.Split(v, ","))
}

// sessionRunner is the runner new runs use, and the guide added to the agent's
// prompt: with add-ons on, sandboxes start in the image that has them (built
// on this host when needed); otherwise the sandbox image from Settings.
func (s *Server) sessionRunner(sb Sandbox) (runner.Runner, string) {
	on := s.enabledAddons()
	if len(on) == 0 {
		return s.runnerFactory(sb), ""
	}
	return addonRunner{s: s, sb: sb, ids: on}, addons.Guide(on)
}

type addonView struct {
	addons.Addon
	Enabled bool `json:"enabled"`
}

func (s *Server) getAddons(w http.ResponseWriter, _ *http.Request) {
	on := s.enabledAddons()
	list := make([]addonView, 0, len(addons.All))
	for _, a := range addons.All {
		list = append(list, addonView{Addon: a, Enabled: slices.Contains(on, a.ID)})
	}
	resp := map[string]any{"addons": list, "base": s.sandbox().Image}
	if len(on) > 0 {
		if b := s.addonBuilds.status(s.sandbox().Image, on); b != nil {
			resp["build"] = b
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) putAddons(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled []string `json:"enabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	for _, id := range req.Enabled {
		if _, ok := addons.Lookup(id); !ok {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown add-on %q", id))
			return
		}
	}
	on := addons.Clean(req.Enabled)
	if err := s.store.SetSetting("addons", strings.Join(on, ",")); err != nil {
		serverError(w)
		return
	}
	// Start building now so the image is ready by the next chat. A build that
	// is running or done is not started again.
	if len(on) > 0 {
		base := s.sandbox().Image
		s.bgWG.Add(1)
		go func() {
			defer s.bgWG.Done()
			_, _ = s.addonBuilds.image(s.bgCtx, s.bgCtx, &s.bgWG, base, on, false, nil)
		}()
	}
	s.getAddons(w, r)
}

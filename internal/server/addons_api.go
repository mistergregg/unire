package server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/mistergregg/unire/internal/addons"
)

// Add-ons are optional tool sets for the sandbox (see package addons). The
// ones turned on are kept as a comma-separated list in "addons"; an owner who
// builds an add-on image themselves can name it in "addon_image_<id>".

func (s *Server) addonState() (on []string, images map[string]string) {
	v, _, _ := s.store.Setting("addons")
	on = addons.Clean(strings.Split(v, ","))
	images = map[string]string{}
	for _, a := range addons.All {
		if img, ok, _ := s.store.Setting("addon_image_" + a.ID); ok && img != "" {
			images[a.ID] = img
		}
	}
	return on, images
}

// sessionImage is the image new runs use: an add-on's image when one is on,
// otherwise the sandbox image from Settings. guide is added to the agent's prompt.
func (s *Server) sessionImage(sb Sandbox) (image, guide string) {
	on, images := s.addonState()
	if img, g := addons.Pick(on, images); img != "" {
		return img, g
	}
	return sb.Image, ""
}

type addonView struct {
	addons.Addon
	Enabled     bool   `json:"enabled"`
	CustomImage string `json:"customImage"`
}

func (s *Server) getAddons(w http.ResponseWriter, _ *http.Request) {
	on, images := s.addonState()
	list := make([]addonView, 0, len(addons.All))
	for _, a := range addons.All {
		list = append(list, addonView{Addon: a, Enabled: slices.Contains(on, a.ID), CustomImage: images[a.ID]})
	}
	image, _ := s.sessionImage(s.sandbox())
	writeJSON(w, http.StatusOK, map[string]any{"addons": list, "image": image})
}

func (s *Server) putAddons(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled []string          `json:"enabled"`
		Images  map[string]string `json:"images"`
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
	settings := map[string]string{"addons": strings.Join(addons.Clean(req.Enabled), ",")}
	for id, img := range req.Images {
		if _, ok := addons.Lookup(id); !ok {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown add-on %q", id))
			return
		}
		img = strings.TrimSpace(img)
		if img != "" && !dockerName.MatchString(img) {
			writeErr(w, http.StatusBadRequest, "image name looks invalid")
			return
		}
		settings["addon_image_"+id] = img
	}
	for k, v := range settings {
		if err := s.store.SetSetting(k, v); err != nil {
			serverError(w)
			return
		}
	}
	s.getAddons(w, r)
}

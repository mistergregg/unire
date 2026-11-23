package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mistergregg/unire/internal/preview"
	"github.com/mistergregg/unire/internal/store"
)

// previewService runs the live previews of conversations. The preview package
// does the work; this file only connects it to chats, the network rules and
// the web API.
type previewService struct {
	mgr *preview.Manager
	// hub gives each running preview a port. It is nil when UNIRE_PREVIEW_PORTS
	// is not set (or not valid), which switches previews off; hubErr says why.
	hub    *preview.Hub
	hubErr string
	// detect looks for something to preview in a conversation's workspace volume.
	detect func(ctx context.Context, image, volume string) ([]preview.Found, error)
}

func newPreviewService() *previewService {
	p := &previewService{mgr: &preview.Manager{Engine: preview.Docker{}}, detect: preview.DetectInVolume}
	raw := strings.TrimSpace(os.Getenv("UNIRE_PREVIEW_PORTS"))
	if raw == "" {
		p.hubErr = "Previews are off. Set UNIRE_PREVIEW_PORTS (for example 9100-9199) and publish those ports on the Unire container."
		return p
	}
	low, high, err := preview.ParsePortRange(raw)
	if err != nil {
		p.hubErr = "UNIRE_PREVIEW_PORTS: " + err.Error()
		return p
	}
	p.hub = &preview.Hub{Signer: preview.NewSigner(), Low: low, High: high}
	return p
}

// shutdown stops every preview and frees their ports.
func (p *previewService) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = p.mgr.StopAll(ctx)
	if p.hub != nil {
		p.hub.CloseAll()
	}
}

// unavailable says why previews cannot be used right now, or "" if they can.
func (s *Server) previewsUnavailable() string {
	if s.previews.hub == nil {
		return s.previews.hubErr
	}
	if !s.egressInfo.Available {
		return "Previews need Unire to run in Docker with the Docker socket mounted: " + s.egressInfo.Reason
	}
	return ""
}

type previewStatusView struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	Logs    string `json:"logs,omitempty"`
	// URL opens the preview. It carries a link token that works for two minutes.
	URL string `json:"url,omitempty"`
}

type previewView struct {
	Repo   string             `json:"repo"`
	Name   string             `json:"name"`
	Recipe string             `json:"recipe"`
	Dir    string             `json:"dir"`
	Status *previewStatusView `json:"status,omitempty"` // empty until it has been started
}

type previewRepoError struct {
	Repo  string `json:"repo"`
	Error string `json:"error"`
}

type previewListView struct {
	Available bool               `json:"available"`
	Reason    string             `json:"reason,omitempty"`
	Previews  []previewView      `json:"previews"`
	Problems  []previewRepoError `json:"problems,omitempty"`
}

func previewKey(root, repo, name string) string { return root + "." + repo + "." + name }

// conversationRoot is the first session of the conversation x belongs to.
func (s *Server) conversationRoot(w http.ResponseWriter, x store.Session) (string, bool) {
	thread, err := s.threadOf(x.ID)
	if err != nil || len(thread) == 0 {
		serverError(w)
		return "", false
	}
	return thread[0].ID, true
}

// statusView describes a preview's state for the UI. A running one gets a URL
// on its own port, with a fresh link token.
func (s *Server) previewStatusView(r *http.Request, key string) *previewStatusView {
	st, ok := s.previews.mgr.Status(key)
	if !ok {
		return nil
	}
	v := &previewStatusView{State: string(st.State), Message: st.Message, Logs: st.Logs}
	if st.State == preview.StateReady && s.previews.hub != nil {
		port, err := s.previews.hub.Open(key, st.Target)
		if err != nil {
			v.State, v.Message = string(preview.StateFailed), err.Error()
			return v
		}
		v.URL = "http://" + net.JoinHostPort(requestHost(r), strconv.Itoa(port)) + "/?unire_token=" + s.previews.hub.LinkToken(key)
	}
	return v
}

// requestHost is the host the browser used to reach Unire, without its port:
// a preview is on the same host, at its own port.
func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

func (s *Server) listPreviews(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	out := previewListView{Previews: []previewView{}}
	if why := s.previewsUnavailable(); why != "" {
		out.Reason = why
		writeJSON(w, http.StatusOK, out)
		return
	}
	root, ok := s.conversationRoot(w, x)
	if !ok {
		return
	}
	access, err := s.sessions.PreviewAccess(x.ID)
	if err != nil {
		out.Reason = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	found, err := s.previews.detect(r.Context(), s.sandbox().Image, access.Workspace)
	if err != nil {
		out.Reason = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Available = true
	for _, f := range found {
		if f.Err != "" {
			out.Problems = append(out.Problems, previewRepoError{Repo: f.Repo, Error: f.Err})
		}
		for _, p := range f.Previews {
			out.Previews = append(out.Previews, previewView{
				Repo: f.Repo, Name: p.Name, Recipe: p.Recipe.Name, Dir: p.Dir,
				Status: s.previewStatusView(r, previewKey(root, f.Repo, p.Name)),
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) startPreview(w http.ResponseWriter, r *http.Request) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return
	}
	if why := s.previewsUnavailable(); why != "" {
		writeErr(w, http.StatusServiceUnavailable, why)
		return
	}
	access, err := s.sessions.PreviewAccess(x.ID)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	found, err := s.previews.detect(r.Context(), s.sandbox().Image, access.Workspace)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	repo, name := r.PathValue("repo"), r.PathValue("name")
	var pv *preview.Preview
	for _, f := range found {
		for i := range f.Previews {
			if f.Repo == repo && f.Previews[i].Name == name {
				pv = &f.Previews[i]
			}
		}
	}
	if pv == nil {
		writeErr(w, http.StatusNotFound, "there is no such preview in this workspace")
		return
	}
	key := previewKey(access.Root, repo, name)
	sb := s.sandbox()
	s.previews.mgr.Start(key, *pv, preview.StartOpts{
		Network: access.Network, Workspace: access.Workspace, RepoDir: repo,
		Memory: sb.EffectiveMemory(), CPUs: sb.CPUs, Env: access.Env, Register: access.Register,
	})
	writeJSON(w, http.StatusOK, s.previewStatusView(r, key))
}

func (s *Server) previewKeyOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	x, ok := s.lookupSession(w, r)
	if !ok {
		return "", false
	}
	root, ok := s.conversationRoot(w, x)
	if !ok {
		return "", false
	}
	return previewKey(root, r.PathValue("repo"), r.PathValue("name")), true
}

func (s *Server) getPreview(w http.ResponseWriter, r *http.Request) {
	key, ok := s.previewKeyOf(w, r)
	if !ok {
		return
	}
	v := s.previewStatusView(r, key)
	if v == nil {
		writeErr(w, http.StatusNotFound, "this preview has not been started")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) stopPreview(w http.ResponseWriter, r *http.Request) {
	key, ok := s.previewKeyOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.previews.mgr.Stop(ctx, key); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not stop the preview: "+err.Error())
		return
	}
	if s.previews.hub != nil {
		s.previews.hub.Close(key)
	}
	writeJSON(w, http.StatusOK, s.previewStatusView(r, key))
}

func (s *Server) previewLogs(w http.ResponseWriter, r *http.Request) {
	key, ok := s.previewKeyOf(w, r)
	if !ok {
		return
	}
	if _, known := s.previews.mgr.Status(key); !known {
		writeErr(w, http.StatusNotFound, "this preview has not been started")
		return
	}
	logs := s.previews.mgr.Logs(r.Context(), key, 300)
	const max = 64 << 10
	if len(logs) > max {
		logs = logs[len(logs)-max:]
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, logs)
}

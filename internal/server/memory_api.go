package server

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mistergregg/unire/internal/memory"
	"github.com/mistergregg/unire/internal/store"
)

// A repo key is "<server id>/<full name>".
var repoKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[A-Za-z0-9._/-]{1,200}$`)

func validRepoKey(k string) bool { return repoKeyRe.MatchString(k) && !strings.Contains(k, "..") }

type summaryView struct {
	SessionID string   `json:"sessionId"`
	Task      string   `json:"task"`
	Agent     string   `json:"agent"`
	Branch    string   `json:"branch"`
	Status    string   `json:"status"`
	Source    string   `json:"source"`
	Repos     []string `json:"repos"`
	CreatedAt int64    `json:"createdAt"`
	Summary   string   `json:"summary,omitempty"`
	Snippet   string   `json:"snippet,omitempty"`
}

func toSummaryView(x store.Summary, full bool) summaryView {
	v := summaryView{x.SessionID, x.Task, x.Agent, x.Branch, x.Status, x.Source, x.Repos, x.CreatedAt.Unix(), "", ""}
	if v.Repos == nil {
		v.Repos = []string{}
	}
	if full {
		v.Summary = x.Summary
	} else {
		v.Snippet = memory.Cap(strings.Join(strings.Fields(x.Summary), " "), 240)
	}
	return v
}

// listMemory lists every repo that has a brief or any session summary.
func (s *Server) listMemory(w http.ResponseWriter, _ *http.Request) {
	briefs, err := s.store.RepoMemories()
	if err != nil {
		serverError(w)
		return
	}
	counts, err := s.store.SummaryCounts()
	if err != nil {
		serverError(w)
		return
	}
	type row struct {
		Key       string `json:"key"`
		Name      string `json:"name"`
		HasBrief  bool   `json:"hasBrief"`
		Summaries int    `json:"summaries"`
		UpdatedAt int64  `json:"updatedAt"`
	}
	rows := map[string]*row{}
	nameOf := func(key string) string {
		if i := strings.Index(key, "/"); i >= 0 {
			return key[i+1:]
		}
		return key
	}
	for _, b := range briefs {
		rows[b.RepoKey] = &row{Key: b.RepoKey, Name: nameOf(b.RepoKey), HasBrief: true, UpdatedAt: b.UpdatedAt.Unix()}
	}
	for k, n := range counts {
		if rows[k] == nil {
			rows[k] = &row{Key: k, Name: nameOf(k)}
		}
		rows[k].Summaries = n
	}
	out := make([]*row, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].Key < out[j].Key
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRepoMemory(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if !validRepoKey(key) {
		writeErr(w, http.StatusBadRequest, "invalid repo")
		return
	}
	s.writeRepoMemory(w, key)
}

func (s *Server) writeRepoMemory(w http.ResponseWriter, key string) {
	m, has, err := s.store.RepoMemory(key)
	if err != nil {
		serverError(w)
		return
	}
	list, err := s.store.RepoSummaries(key, 50)
	if err != nil {
		serverError(w)
		return
	}
	sums := []summaryView{}
	for _, x := range list {
		sums = append(sums, toSummaryView(x, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key": key, "hasBrief": has, "brief": m.Brief, "hasPrevious": m.Previous != "",
		"updatedAt": m.UpdatedAt.Unix(), "bySession": m.SessionID, "summaries": sums,
		"limit": memory.BriefCap,
	})
}

// putRepoMemory saves a brief the owner wrote or edited.
func (s *Server) putRepoMemory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Brief string `json:"brief"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validRepoKey(req.Key) {
		writeErr(w, http.StatusBadRequest, "invalid repo")
		return
	}
	brief, _ := memory.Redact(memory.Clean(req.Brief), nil)
	if brief == "" {
		writeErr(w, http.StatusBadRequest, "the brief is empty; use Clear to remove it")
		return
	}
	if len(brief) > memory.BriefCap {
		writeErr(w, http.StatusBadRequest, "the brief is too long; the limit is 6000 characters")
		return
	}
	if err := s.store.SetRepoMemory(req.Key, brief, ""); err != nil {
		serverError(w)
		return
	}
	s.writeRepoMemory(w, req.Key)
}

func (s *Server) restoreRepoMemory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validRepoKey(req.Key) {
		writeErr(w, http.StatusBadRequest, "invalid repo")
		return
	}
	ok, err := s.store.RestoreRepoMemory(req.Key)
	switch {
	case err != nil:
		serverError(w)
	case !ok:
		writeErr(w, http.StatusConflict, "there is no earlier version to restore")
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) deleteRepoMemory(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if !validRepoKey(key) {
		writeErr(w, http.StatusBadRequest, "invalid repo")
		return
	}
	ok, err := s.store.DeleteRepoMemory(key)
	switch {
	case err != nil:
		serverError(w)
	case !ok:
		writeErr(w, http.StatusNotFound, "no brief for this repo")
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) searchMemory(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo != "" && !validRepoKey(repo) {
		writeErr(w, http.StatusBadRequest, "invalid repo")
		return
	}
	list, err := s.store.SearchSummaries(r.URL.Query().Get("q"), repo, 30)
	if err != nil {
		serverError(w)
		return
	}
	out := []summaryView{}
	for _, x := range list {
		out = append(out, toSummaryView(x, false))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getSummary(w http.ResponseWriter, r *http.Request) {
	x, ok, err := s.store.Summary(r.PathValue("id"))
	switch {
	case err != nil:
		serverError(w)
	case !ok:
		writeErr(w, http.StatusNotFound, "no summary for this session")
	default:
		writeJSON(w, http.StatusOK, toSummaryView(x, true))
	}
}

func (s *Server) deleteSummary(w http.ResponseWriter, r *http.Request) {
	ok, err := s.store.DeleteSummary(r.PathValue("id"))
	switch {
	case err != nil:
		serverError(w)
	case !ok:
		writeErr(w, http.StatusNotFound, "no summary for this session")
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// memoryStats compares runs with memory on and off over the last 90 days: how they
// went, what memory cost, and what became of their pull requests. It is for the
// owner to judge, not a verdict: the groups are not matched, so small numbers say little.
func (s *Server) memoryStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.MemoryStats(time.Now().AddDate(0, 0, -90))
	if err != nil {
		serverError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": 90, "groups": stats})
}

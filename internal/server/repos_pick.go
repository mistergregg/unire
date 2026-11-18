package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mistergregg/unire/internal/repopick"
	"github.com/mistergregg/unire/internal/safe"
	"github.com/mistergregg/unire/internal/sessions"
	"github.com/mistergregg/unire/internal/store"
)

// The repos each git server lists are remembered for a few minutes, so guessing
// the repos for a message does not ask every server every time.
const catalogFor = 5 * time.Minute

type catalogEntry struct {
	at    time.Time
	repos []repopick.Repo
}

type catalogCache struct {
	mu sync.Mutex
	m  map[string]catalogEntry
}

func matchRepo(r repopick.Repo, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	return q == "" || strings.Contains(strings.ToLower(r.FullName), q) ||
		strings.Contains(strings.ToLower(r.Description), q)
}

// listCached lists a server's repos matching query, from the cache when fresh.
func (s *Server) listCached(ctx context.Context, g store.GitServer, query string) []repopick.Repo {
	key := g.ID + "\x00" + query
	s.catalog.mu.Lock()
	if e, ok := s.catalog.m[key]; ok && time.Since(e.at) < catalogFor {
		s.catalog.mu.Unlock()
		return e.repos
	}
	// GitHub's ListRepos does in-memory filtering from the full repo list.
	// If the server's base repo list is already cached, filter from it directly
	// without another network call.
	if g.Type == "github" && query != "" {
		if base, ok := s.catalog.m[g.ID+"\x00"]; ok && time.Since(base.at) < catalogFor {
			s.catalog.mu.Unlock()
			var filtered []repopick.Repo
			for _, r := range base.repos {
				if matchRepo(r, query) {
					filtered = append(filtered, r)
				}
			}
			return filtered
		}
	}
	s.catalog.mu.Unlock()
	p, err := s.provider(g)
	if err != nil {
		return nil
	}
	list, err := p.ListRepos(ctx, query)
	if err != nil {
		return nil
	}
	out := make([]repopick.Repo, 0, len(list))
	for _, r := range list {
		out = append(out, repopick.Repo{ServerID: g.ID, ServerName: g.Name, FullName: r.FullName, CloneURL: r.CloneURL, DefaultBranch: r.DefaultBranch, Description: r.Description})
	}
	s.catalog.mu.Lock()
	if s.catalog.m == nil {
		s.catalog.m = map[string]catalogEntry{}
	}
	s.catalog.m[key] = catalogEntry{time.Now(), out}
	s.catalog.mu.Unlock()
	return out
}

// reposFor is every repo the owner's git servers show, plus what each server
// finds for the distinctive words of the text.
func (s *Server) reposFor(ctx context.Context, text string) []repopick.Repo {
	servers, err := s.store.GitServers()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []repopick.Repo
	var mu sync.Mutex
	add := func(list []repopick.Repo) {
		mu.Lock()
		defer mu.Unlock()
		for _, r := range list {
			if !seen[r.Key()] {
				seen[r.Key()] = true
				out = append(out, r)
			}
		}
	}
	words := append([]string{""}, repopick.SearchWords(text)...)
	var wg sync.WaitGroup
	for _, g := range servers {
		g := g
		// For GitHub, the base query ("") already returns all accessible repos,
		// and word queries only filter that same set (which add() deduplicates anyway).
		serverWords := words
		if g.Type == "github" {
			serverWords = []string{""}
		}
		for _, w := range serverWords {
			w := w
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer safe.Recover("reposFor", nil)
				add(s.listCached(ctx, g, w))
			}()
		}
	}
	wg.Wait()
	return out
}

// repoHistory is which repos past conversations used, and together.
func (s *Server) repoHistory() repopick.History {
	list, err := s.store.Sessions(400)
	if err != nil {
		return repopick.History{}
	}
	var chats [][]string
	for _, x := range list {
		if x.ParentID != "" {
			continue // a reply continues its conversation
		}
		var keys []string
		for _, ref := range x.Repos {
			keys = append(keys, store.RepoKey(ref))
		}
		chats = append(chats, keys)
	}
	return repopick.BuildHistory(chats)
}

func (s *Server) repoBriefs() map[string]string {
	out := map[string]string{}
	if list, err := s.store.RepoMemories(); err == nil {
		for _, m := range list {
			out[m.RepoKey] = m.Brief
		}
	}
	return out
}

// rankRepos guesses the repos a text needs, apart from those already chosen.
func (s *Server) rankRepos(ctx context.Context, text string, have []store.RepoRef) []repopick.Candidate {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var keys []string
	for _, r := range have {
		keys = append(keys, store.RepoKey(r))
	}
	return repopick.Rank(text, s.reposFor(ctx, text), s.repoHistory(), keys, s.repoBriefs())
}

func refOf(c repopick.Candidate) store.RepoRef {
	return store.RepoRef{ServerID: c.ServerID, FullName: c.FullName, CloneURL: c.CloneURL}
}

// pickNotes says, for the activity log, why each repo was picked.
func pickNotes(picked []repopick.Candidate) []string {
	var out []string
	for _, c := range picked {
		out = append(out, fmt.Sprintf("picked repo %s: %s", c.FullName, strings.Join(c.Why, "; ")))
	}
	return out
}

// suggestRepos ranks the repos a draft message needs, for the new chat form.
func (s *Server) suggestRepos(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt string          `json:"prompt"`
		Have   []store.RepoRef `json:"have"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Prompt) > 20000 {
		req.Prompt = req.Prompt[:20000]
	}
	c := s.rankRepos(r.Context(), req.Prompt, req.Have)
	if c == nil {
		c = []repopick.Candidate{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": c})
}

// Find answers the agent's request for another repo (the repo tool). See
// sessions.RepoFinder.
func (s *Server) Find(ctx context.Context, query, ownerText string, have []store.RepoRef) (sessions.FoundRepo, string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	query = strings.Trim(strings.TrimSpace(query), `"'`)
	if query == "" {
		return sessions.FoundRepo{}, `usage: unire repo "<name or words>"`
	}
	haveKeys := map[string]bool{}
	var keys []string
	for _, r := range have {
		haveKeys[store.RepoKey(r)] = true
		keys = append(keys, store.RepoKey(r))
	}
	all := s.reposFor(ctx, query)
	hist, briefs := s.repoHistory(), s.repoBriefs()

	var pick *repopick.Candidate
	// An exact "owner/name" needs no guessing.
	for _, r := range all {
		if strings.EqualFold(r.FullName, query) && !haveKeys[r.Key()] {
			c := repopick.Candidate{Repo: r, Why: []string{"you asked for it by name"}}
			pick = &c
			break
		}
	}
	if pick == nil {
		// Every word the agent wrote must appear in the repo's name or description, so
		// "echotalk mobile" does not turn into "echotalk-backend" and a bare "gbr" is
		// ambiguous rather than a guess.
		var q []string
		for _, w := range strings.Fields(strings.ToLower(query)) {
			if w = strings.Trim(w, `.,;:!?()[]{}"'`); w != "" {
				q = append(q, w)
			}
		}
		var matches []repopick.Repo
		for _, r := range all {
			if haveKeys[r.Key()] {
				continue
			}
			hay := strings.ToLower(r.FullName + " " + r.Description)
			ok := len(q) > 0
			for _, w := range q {
				ok = ok && strings.Contains(hay, w)
			}
			if ok {
				matches = append(matches, r)
			}
		}
		switch {
		case len(matches) == 0:
			return sessions.FoundRepo{}, "No repo of the owner's matches \"" + query + "\" (or it is already in this session)."
		case len(matches) > 1:
			var list []string
			for i, r := range matches {
				if i >= 6 {
					break
				}
				list = append(list, "- "+r.FullName)
			}
			return sessions.FoundRepo{}, "Which one? Run the command again with the exact name:\n" + strings.Join(list, "\n")
		}
		c := repopick.Candidate{Repo: matches[0], Why: []string{"its name or description matches \"" + query + "\""}}
		pick = &c
	}
	repos, err := s.resolveRepos([]store.RepoRef{refOf(*pick)})
	if err != nil {
		return sessions.FoundRepo{}, "Could not use " + pick.FullName + ": " + err.Error()
	}
	// The owner's own words (or what they usually use together) already point to it?
	named := false
	for _, c := range repopick.Auto(repopick.Rank(ownerText, all, hist, keys, briefs)) {
		if c.Key() == pick.Key() {
			named = true
		}
	}
	return sessions.FoundRepo{Repo: repos[0], Named: named, Why: strings.Join(pick.Why, "; ")}, ""
}

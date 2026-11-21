package server

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/mistergregg/unire/internal/pulls"
	"github.com/mistergregg/unire/internal/store"
)

// An open pull request whose branch has fallen behind the branch it goes into (main
// moved on after another pull request was merged) is brought up to date by itself, the
// way the Update branch button does it, so the owner never has to press it. When the
// two conflict, the agent is asked to merge and resolve them in a turn of its own, a
// couple of times at most. It follows the same switch as fixing failing checks.

const maxKeepTries = 2

type keepResult int

const (
	keepNothing keepResult = iota // current, or nothing could be done
	keepUpdated                   // the server updated the branch
	keepStarted                   // a turn to resolve conflicts was started
)

// keepTries counts, per pull request, the turns started to resolve conflicts with the
// base, and remembers the last problem it logged so a persistent one is logged once.
type keepTries struct {
	mu      sync.Mutex
	tries   map[string]int
	lastErr map[string]string
}

func (k *keepTries) get(key string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.tries[key]
}

func (k *keepTries) bump(key string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.tries == nil {
		k.tries = map[string]int{}
	}
	k.tries[key]++
	return k.tries[key]
}

// ok is called when the pull request is current: the count and the logged problem start over.
func (k *keepTries) ok(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.tries, key)
	delete(k.lastErr, key)
}

// problem reports whether msg is new for this pull request (so worth logging).
func (k *keepTries) problem(key, msg string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.lastErr == nil {
		k.lastErr = map[string]string{}
	}
	if k.lastErr[key] == msg {
		return false
	}
	k.lastErr[key] = msg
	return true
}

// keepPullCurrent brings one open pull request up to date, or starts the turn that
// resolves its conflicts with the base. last is the conversation's newest turn, which
// must be idle.
func (s *Server) keepPullCurrent(ctx context.Context, last store.Session, v pulls.View) keepResult {
	key := fmt.Sprintf("%s/%s#%d", v.ServerID, v.Repo, v.Number)
	fr, err := s.pulls.KeepCurrent(ctx, v)
	if err != nil {
		if s.keep.problem(key, err.Error()) {
			log.Printf("keeping %s#%d up to date: %v", v.Repo, v.Number, err)
		}
		return keepNothing
	}
	switch fr {
	case pulls.Updated:
		log.Printf("%s#%d was behind %s: updated its branch", v.Repo, v.Number, v.Base)
		s.keep.ok(key)
		return keepUpdated
	case pulls.Conflicting:
		if s.keep.get(key) >= maxKeepTries {
			return keepNothing // the owner sees it on the pull request
		}
		try := s.keep.bump(key)
		prompt := fmt.Sprintf("Unire is starting this turn by itself because pull request #%d (%s into %s) has fallen behind %s and cannot be updated automatically: they conflict (try %d of %d).\n\n"+
			"Run `git fetch origin` and `git merge origin/%s` on this branch, resolve every conflict so that both sides' intent is kept and nothing that works breaks (read both versions, do not just pick one side), run the tests if there are any, commit the merge and push so the pull request is up to date. "+
			"If the conflict is not something you can resolve safely, say so plainly instead of guessing.",
			v.Number, v.Branch, v.Base, v.Base, try, maxKeepTries, v.Base)
		if _, err := s.startReply(ctx, last, prompt, "", nil, nil); err != nil {
			log.Printf("resolving the conflicts of %s#%d could not start: %v", v.Repo, v.Number, err)
			return keepNothing
		}
		return keepStarted
	}
	s.keep.ok(key)
	return keepNothing
}

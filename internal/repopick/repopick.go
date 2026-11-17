// Package repopick guesses which repos a piece of work needs, from what the owner
// wrote, which repos exist on their git servers, and which they have used before
// (and together). It is plain text matching and counting: no model call, so it is
// free, fast and predictable, and it says why it picked each repo.
package repopick

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Repo is a repo on one of the owner's git servers.
type Repo struct {
	ServerID      string `json:"serverId"`
	ServerName    string `json:"serverName,omitempty"`
	FullName      string `json:"fullName"`
	CloneURL      string `json:"cloneUrl"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Description   string `json:"description,omitempty"`
}

// Key identifies a repo across servers.
func (r Repo) Key() string { return r.ServerID + "/" + r.FullName }

// History is what past conversations show about which repos go together.
type History struct {
	Uses     map[string]int            // repo key -> conversations that used it
	Together map[string]map[string]int // repo key -> another repo key -> conversations that used both
}

// BuildHistory counts, from each past conversation's repo keys, how often each
// repo and each pair was used.
func BuildHistory(chats [][]string) History {
	h := History{Uses: map[string]int{}, Together: map[string]map[string]int{}}
	for _, chat := range chats {
		seen := map[string]bool{}
		var keys []string
		for _, k := range chat {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		for _, a := range keys {
			h.Uses[a]++
			for _, b := range keys {
				if a == b {
					continue
				}
				if h.Together[a] == nil {
					h.Together[a] = map[string]int{}
				}
				h.Together[a][b]++
			}
		}
	}
	return h
}

// Candidate is a repo that may be needed, and why.
type Candidate struct {
	Repo
	Score float64  `json:"score"`
	Auto  bool     `json:"auto"` // sure enough to use without asking, when the owner chose nothing
	Why   []string `json:"why"`
	exact bool     // its whole name was written, which outranks everything else
}

// Thresholds. A repo whose name the owner effectively wrote, or that is usually
// used with such a repo, is used without asking; weaker matches are only suggested.
const (
	autoScore    = 7.0
	suggestScore = 2.5
	maxAuto      = 4
	maxSuggested = 8
)

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by for from has have how i if in into is it its me my of on or our so that the their them then there these they this to up us was we what when where which who why will with you your
		look looks looking see check review fix add make update change work working use using need needs want please also just can could would should do does done get got set run
		file files code repo repos repository project thing things stuff one two some any all not no yes more less new old`) {
		stop[w] = true
	}
}

// generic words say little about which repo is meant ("app" fits half of them).
var generic = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`app apps web website site api client server backend frontend mobile desktop service services lib core main test tests tool tools infra`) {
		generic[w] = true
	}
}

func stem(w string) string {
	if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
		return w[:len(w)-1]
	}
	return w
}

// words splits text into lower-case words, splitting camelCase too.
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	var prev rune
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.IsUpper(r) && unicode.IsLower(prev) {
				flush()
			}
			cur = append(cur, r)
		default:
			flush()
		}
		prev = r
	}
	flush()
	return out
}

func wordSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, w := range words(s) {
		if len(w) >= 2 && !stop[w] {
			set[stem(w)] = true
		}
	}
	return set
}

func shortName(full string) string {
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[i+1:]
	}
	return full
}

// Rank scores the repos for the text. have are the keys of repos already chosen
// (they are not offered again, but repos usually used with them are). briefs may
// hold each repo's memory brief by key and is optional.
func Rank(text string, repos []Repo, hist History, have []string, briefs map[string]string) []Candidate {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	T := wordSet(text)
	squashed := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)

	// How common each name word is among the repos: a word in every repo name
	// ("gbr") says little.
	df := map[string]int{}
	nameWords := map[string][]string{}
	for _, r := range repos {
		var ws []string
		seen := map[string]bool{}
		for _, w := range words(shortName(r.FullName)) {
			w = stem(w)
			if len(w) >= 2 && !seen[w] {
				seen[w] = true
				ws = append(ws, w)
				df[w]++
			}
		}
		nameWords[r.Key()] = ws
	}
	weight := func(w string) float64 {
		x := 1 / (1 + math.Log(float64(max(df[w], 1))))
		if generic[w] {
			x *= 0.35
		}
		return x
	}

	haveSet := map[string]bool{}
	for _, k := range have {
		haveSet[k] = true
	}
	type scored struct {
		c    Candidate
		name float64
		comp bool // usually used with a repo that is wanted
	}
	byKey := map[string]*scored{}
	for _, r := range repos {
		if haveSet[r.Key()] {
			continue
		}
		s := &scored{c: Candidate{Repo: r}}
		ws := nameWords[r.Key()]
		var total, hit float64
		var hits []string
		for _, w := range ws {
			total += weight(w)
			if T[w] {
				hit += weight(w)
				hits = append(hits, w)
			}
		}
		if total > 0 && hit > 0 {
			s.name = 10 * hit / total
		}
		// The whole name written as one word or with the separators changed:
		// "echotalk-mobile", "echotalk mobile" and "EchoTalkMobile" all count.
		if joined := strings.Join(words(shortName(r.FullName)), ""); len(joined) >= 4 && strings.Contains(squashed, joined) {
			s.name, hits = 10, ws
		}
		if s.name > 0 {
			s.c.Score += s.name
			s.c.Why = append(s.c.Why, "you wrote "+strings.Join(hits, ", ")+" (in its name)")
		}
		// Words of its description.
		if d := wordSet(r.Description); len(d) > 0 {
			n := 0
			for w := range d {
				if len(w) >= 4 && T[w] && !generic[w] {
					n++
				}
			}
			if n > 0 {
				s.c.Score += math.Min(3, float64(n))
				s.c.Why = append(s.c.Why, "its description matches")
			}
		}
		if b := briefs[r.Key()]; b != "" {
			n := 0
			bw := wordSet(b)
			for w := range T {
				if len(w) >= 5 && !generic[w] && bw[w] {
					n++
				}
			}
			if n >= 2 {
				s.c.Score++
				s.c.Why = append(s.c.Why, "its memory mentions the same things")
			}
		}
		// A repo you use a lot is a slightly better guess, but only if something else matches.
		if s.c.Score > 0 {
			if u := hist.Uses[r.Key()]; u > 0 {
				s.c.Score += math.Min(2, 0.7*math.Log(1+float64(u)))
			}
		}
		byKey[r.Key()] = s
	}

	// Repos usually used together with one that is wanted. The seeds are the repos
	// already chosen and those named strongly.
	seeds := append([]string(nil), have...)
	for k, s := range byKey {
		if s.name >= autoScore {
			seeds = append(seeds, k)
		}
	}
	for _, seed := range seeds {
		uses := hist.Uses[seed]
		if uses == 0 {
			continue
		}
		for other, n := range hist.Together[seed] {
			s, ok := byKey[other]
			if !ok || n < 2 || float64(n)/float64(uses) < 0.5 {
				continue
			}
			s.c.Score += 4
			s.c.Why = append(s.c.Why, "usually used with "+seedName(seed, repos))
			s.comp = true // a companion is as sure as a named repo
		}
	}

	var out []Candidate
	for _, s := range byKey {
		if s.c.Score < suggestScore {
			continue
		}
		s.c.exact = s.name >= 9.99
		s.c.Auto = (s.name >= autoScore && s.c.Score >= autoScore) || s.comp
		out = append(out, s.c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].exact != out[j].exact {
			return out[i].exact
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].FullName < out[j].FullName
	})
	autos := 0
	for i := range out {
		if out[i].Auto {
			autos++
			if autos > maxAuto {
				out[i].Auto = false
			}
		}
	}
	if len(out) > maxSuggested {
		out = out[:maxSuggested]
	}
	return out
}

func seedName(key string, repos []Repo) string {
	for _, r := range repos {
		if r.Key() == key {
			return shortName(r.FullName)
		}
	}
	return shortName(key)
}

// Auto returns the repos that are sure enough to use without asking.
func Auto(c []Candidate) []Candidate {
	var out []Candidate
	for _, x := range c {
		if x.Auto {
			out = append(out, x)
		}
	}
	return out
}

// SearchWords are the distinctive words of a text worth asking a git server to
// search for (the first repos a server lists may not include the one meant).
func SearchWords(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words(text) {
		if len(w) >= 4 && !stop[w] && !generic[w] && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

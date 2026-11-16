package server

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// A sign-in terminal is a program drawing on a screen, so its raw bytes are
// mostly cursor movement. To help work out why a sign-in went wrong, Unire
// keeps the most recent output of each agent's sign-in terminal in memory
// (never on disk), turned back into plain text, with sign-in codes, links and
// email addresses removed. It is shown only to the owner.

const transcriptKeep = 48 << 10 // raw bytes kept per agent

type transcriptEntry struct {
	raw []byte
	at  time.Time
}

type transcripts struct {
	mu sync.Mutex
	m  map[string]*transcriptEntry
}

func (t *transcripts) start(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]*transcriptEntry{}
	}
	t.m[id] = &transcriptEntry{at: time.Now()}
}

func (t *transcripts) add(id string, b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[id]
	if e == nil {
		return
	}
	e.raw = append(e.raw, b...)
	if len(e.raw) > transcriptKeep {
		e.raw = e.raw[len(e.raw)-transcriptKeep:]
	}
}

// get returns the cleaned text and when that sign-in started.
func (t *transcripts) get(id string) (string, time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[id]
	if e == nil {
		return "", time.Time{}, false
	}
	return cleanTranscript(e.raw), e.at, true
}

var (
	csiSeq   = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]`)
	oscSeq   = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	escOther = regexp.MustCompile(`\x1b[()][A-Za-z0-9]|\x1b[=>78MDEHc]`)
	urlRe    = regexp.MustCompile(`https?://[^\s"'<>]+`)
	emailRe  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// A Google authorization code, and any other long token-like string.
	codeRe  = regexp.MustCompile(`\b4/[A-Za-z0-9_-]{8,}`)
	tokenRe = regexp.MustCompile(`[A-Za-z0-9_-]{32,}`)
	boxOnly = regexp.MustCompile(`^[\s│╭╮╰╯─┌┐└┘├┤┬┴┼█▀▄▌▐▖▗▘▝▙▛▜▟]*$`)
)

// cleanTranscript turns raw terminal bytes into readable lines and removes
// anything that could be used to sign in as the owner.
func cleanTranscript(raw []byte) string {
	s := string(raw)
	s = oscSeq.ReplaceAllString(s, "")
	s = csiSeq.ReplaceAllString(s, "")
	s = escOther.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	// A bare carriage return redraws the line: keep the last version.
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if i := strings.LastIndex(l, "\r"); i >= 0 {
			if tail := l[i+1:]; strings.TrimSpace(tail) != "" {
				l = tail
			} else {
				l = strings.ReplaceAll(l, "\r", "")
			}
		}
		l = strings.Map(func(r rune) rune {
			if r < 32 && r != '\t' || r == 127 {
				return -1
			}
			return r
		}, l)
		l = strings.TrimRight(l, " \t")
		lines = append(lines, redactLine(l))
	}
	// A screen that redraws leaves the same lines again and again, often
	// alternating. Show each distinct line once, in order of first appearance,
	// and drop lines with nothing but borders.
	var out []string
	seen := map[string]bool{}
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || boxOnly.MatchString(t) || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, l)
	}
	text := strings.Join(out, "\n")
	if len(text) > 6000 {
		text = "…\n" + text[len(text)-6000:]
	}
	return text
}

func redactLine(l string) string {
	l = urlRe.ReplaceAllStringFunc(l, func(u string) string {
		host := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		return "<link to " + host + ">"
	})
	l = emailRe.ReplaceAllString(l, "<email>")
	l = codeRe.ReplaceAllString(l, "<code>")
	return tokenRe.ReplaceAllString(l, "<token>")
}

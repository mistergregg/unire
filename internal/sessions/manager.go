// Package sessions starts coding sessions in the background, records their
// log in the database, and lets the owner stop them.
package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/mistergregg/unire/internal/egress"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/session"
	"github.com/mistergregg/unire/internal/store"
	"github.com/mistergregg/unire/internal/workflow"
)

// MaxConcurrent is how many sessions run at once; more wait in the queue.
const MaxConcurrent = 4

// Config is read each time a session starts, so settings changes apply to new sessions.
type Config struct {
	Runner runner.Runner
	Memory string
	CPUs   string
	Egress Egress
}

// Egress is the sandbox network protection: the proxy, whether it is working,
// and the owner's allowed addresses.
type Egress struct {
	Proxy *egress.Proxy
	Info  egress.Info
	Allow []egress.Entry
}

// Repo is a repo to clone, with the token for its server.
type Repo struct {
	Ref   store.RepoRef
	Token string
}

// Request is what the owner asks for.
type Request struct {
	Task, Agent, Prompt, Mode string
	Network                   egress.Mode // empty means internet only
	Repos                     []Repo
}

// Manager runs sessions.
type Manager struct {
	st     *store.Store
	config func() Config

	sem chan struct{}
	wg  sync.WaitGroup

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	closing bool
}

// New returns a Manager. Sessions left running by a previous process are
// marked interrupted.
func New(st *store.Store, config func() Config) (*Manager, error) {
	if _, err := st.InterruptUnfinished(); err != nil {
		return nil, err
	}
	return &Manager{st: st, config: config, sem: make(chan struct{}, MaxConcurrent), cancels: map[string]context.CancelFunc{}}, nil
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// repoDirName makes a unique directory name from a repo's full name.
func repoDirName(fullName string, used map[string]bool) string {
	base := session.Slug(path.Base(strings.TrimSuffix(fullName, ".git")))
	name := base
	for n := 2; used[name]; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	used[name] = true
	return name
}

// Start queues a session and returns it. The session runs in the background.
func (m *Manager) Start(req Request) (store.Session, error) {
	if len(req.Repos) == 0 {
		return store.Session{}, errors.New("choose at least one repo")
	}
	if req.Network == "" {
		req.Network = egress.ModeInternet
	}
	if _, ok := egress.ParseMode(string(req.Network)); !ok {
		return store.Session{}, fmt.Errorf("unknown network mode %q", req.Network)
	}
	// Fail closed: never run a protected mode without the protection.
	if cfg := m.config(); req.Network != egress.ModeFull && !cfg.Egress.Info.Available {
		return store.Session{}, fmt.Errorf("network protection is not available (%s). Choose \"full\" network access to run without it", cfg.Egress.Info.Reason)
	}
	used := map[string]bool{}
	spec := session.Spec{ID: newID(), Task: req.Task, Agent: req.Agent, Prompt: req.Prompt, Mode: req.Mode}
	tokens := map[string]string{}
	var refs []store.RepoRef
	var secrets []string
	for _, r := range req.Repos {
		name := repoDirName(r.Ref.FullName, used)
		spec.Repos = append(spec.Repos, session.Repo{Name: name, URL: r.Ref.CloneURL, Base: r.Ref.Base})
		refs = append(refs, r.Ref)
		if r.Token != "" {
			tokens[name] = r.Token
			secrets = append(secrets, r.Token)
		}
	}
	rec := store.Session{ID: spec.ID, Task: req.Task, Agent: req.Agent, Mode: req.Mode, Prompt: req.Prompt, Branch: spec.Branch(), Network: string(req.Network), Repos: refs}

	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return store.Session{}, errors.New("Unire is shutting down")
	}
	if err := m.st.CreateSession(rec); err != nil {
		m.mu.Unlock()
		return store.Session{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[spec.ID] = cancel
	m.wg.Add(1)
	m.mu.Unlock()

	_ = m.st.AddEvent(spec.ID, "log", "queued")
	go m.run(ctx, spec, req.Network, tokens, secrets)
	got, _, _ := m.st.Session(spec.ID)
	return got, nil
}

// Stop asks a queued or running session to end. It reports whether one was found.
func (m *Manager) Stop(id string) bool {
	m.mu.Lock()
	cancel, ok := m.cancels[id]
	m.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// Shutdown stops every session and waits for them to finish.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closing = true
	for _, cancel := range m.cancels {
		cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// Wait blocks until all sessions have finished (for tests).
func (m *Manager) Wait() { m.wg.Wait() }

func (m *Manager) run(ctx context.Context, spec session.Spec, network egress.Mode, tokens map[string]string, secrets []string) {
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		delete(m.cancels, spec.ID)
		m.mu.Unlock()
	}()

	out := newLogWriter(m.st, spec.ID, "out", secrets)
	info := newLogWriter(m.st, spec.ID, "log", secrets)

	// Wait for a free slot, unless stopped while queued.
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-ctx.Done():
		m.finish(ctx, spec.ID, nil, secrets)
		return
	}
	if ctx.Err() != nil {
		m.finish(ctx, spec.ID, nil, secrets)
		return
	}

	_ = m.st.MarkRunning(spec.ID)
	cfg := m.config()
	var rn runner.Runner = cfg.Runner
	if network != egress.ModeFull {
		pol, perr := m.policyFor(spec, network, cfg)
		if perr != nil {
			m.finish(ctx, spec.ID, perr, secrets)
			return
		}
		rn = &netRunner{inner: cfg.Runner, proxy: cfg.Egress.Proxy, info: cfg.Egress.Info, pol: pol, st: m.st, id: spec.ID}
	}
	_ = m.st.AddEvent(spec.ID, "log", "network: "+networkLabel(network))
	err := workflow.Run(ctx, rn, spec, workflow.Options{
		Out: out, Err: info, GitTokens: tokens, Memory: cfg.Memory, CPUs: cfg.CPUs,
	})
	out.Flush()
	info.Flush()
	m.finish(ctx, spec.ID, err, secrets)
}

func (m *Manager) finish(ctx context.Context, id string, err error, secrets []string) {
	status, msg := store.StatusDone, ""
	switch {
	case ctx.Err() != nil:
		m.mu.Lock()
		closing := m.closing
		m.mu.Unlock()
		status = store.StatusStopped
		if closing {
			status = store.StatusInterrupted
			msg = "Unire stopped while this session was running"
		}
	case err != nil:
		status, msg = store.StatusFailed, scrub(err.Error(), secrets)
	}
	_ = m.st.AddEvent(id, "log", "session "+status)
	_ = m.st.MarkFinished(id, status, msg)
}

// ---- log writer ----

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// scrub removes secret values from text.
func scrub(text string, secrets []string) string {
	for _, s := range secrets {
		if len(s) >= 4 {
			text = strings.ReplaceAll(text, s, "***")
		}
	}
	return text
}

const maxLine = 4000

// logWriter turns written bytes into log lines in the database, with secret
// values and terminal colour codes removed.
type logWriter struct {
	st      *store.Store
	id      string
	kind    string
	secrets []string

	mu  sync.Mutex
	buf []byte
}

func newLogWriter(st *store.Store, id, kind string, secrets []string) *logWriter {
	return &logWriter{st: st, id: id, kind: kind, secrets: secrets}
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			if len(w.buf) > 8*maxLine { // a runaway line with no newline
				w.emit(string(w.buf))
				w.buf = nil
			}
			break
		}
		w.emit(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush writes any partial last line.
func (w *logWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

func (w *logWriter) emit(line string) {
	line = strings.TrimRight(ansi.ReplaceAllString(line, ""), "\r ")
	if line == "" {
		return
	}
	line = scrub(line, w.secrets)
	if len(line) > maxLine {
		line = line[:maxLine] + " …"
	}
	_ = w.st.AddEvent(w.id, w.kind, line)
}

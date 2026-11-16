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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mistergregg/unire/internal/egress"
	"github.com/mistergregg/unire/internal/memory"
	"github.com/mistergregg/unire/internal/runner"
	"github.com/mistergregg/unire/internal/session"
	"github.com/mistergregg/unire/internal/store"
	"github.com/mistergregg/unire/internal/tools"
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
	Memory                    bool        // give the agent project memory and save what it learns
	Repos                     []Repo
	Parent                    string // the session this one continues (a reply), or empty
	Preamble                  string // told to the agent before the prompt; not shown as part of what the owner wrote
}

// Manager runs sessions.
type Manager struct {
	st     *store.Store
	config func() Config

	sem chan struct{}
	wg  sync.WaitGroup

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	asks    map[string]chan string   // questions waiting for the owner, by session id + "/" + question id
	saving  map[string]chan struct{} // sessions whose memory is still being saved; closed when done
	closing bool
}

// New returns a Manager. Sessions left running by a previous process are
// marked interrupted.
func New(st *store.Store, config func() Config) (*Manager, error) {
	if _, err := st.InterruptUnfinished(); err != nil {
		return nil, err
	}
	return &Manager{st: st, config: config, sem: make(chan struct{}, MaxConcurrent), cancels: map[string]context.CancelFunc{}, asks: map[string]chan string{}, saving: map[string]chan struct{}{}}, nil
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

// RepoDirNames returns the directory name each repo gets in the sandbox, in
// order. Sessions and the UI use the same names.
func RepoDirNames(fullNames []string) []string {
	used := map[string]bool{}
	out := make([]string, len(fullNames))
	for i, n := range fullNames {
		out[i] = repoDirName(n, used)
	}
	return out
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
	spec := session.Spec{ID: newID(), Task: req.Task, Agent: req.Agent, Prompt: req.Preamble + req.Prompt, Mode: req.Mode}
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
	rec := store.Session{ID: spec.ID, Task: req.Task, Agent: req.Agent, Mode: req.Mode, Prompt: req.Prompt, // what the owner wrote, without the preamble
		Branch: spec.Branch(), Network: string(req.Network), Memory: req.Memory, Repos: refs, ParentID: req.Parent}

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
	go m.run(ctx, job{spec: spec, network: req.Network, memory: req.Memory, refs: refs, tokens: tokens, secrets: secrets, parent: req.Parent, root: m.rootOf(spec.ID, req.Parent)})
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

// job is everything a running session needs.
type job struct {
	spec    session.Spec
	network egress.Mode
	memory  bool
	refs    []store.RepoRef // same order as spec.Repos
	tokens  map[string]string
	secrets []string
	parent  string // the session this one continues, or empty
	root    string // the first session of the conversation: its id names the workspace
}

func (m *Manager) run(ctx context.Context, j job) {
	spec, network, tokens, secrets := j.spec, j.network, j.tokens, j.secrets
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		delete(m.cancels, spec.ID)
		m.mu.Unlock()
	}()
	if j.memory {
		// The memory step runs after the turn is marked finished. Until it is
		// done a reply waits for it, so the reply starts with this turn's memory.
		saved := make(chan struct{})
		m.mu.Lock()
		m.saving[spec.ID] = saved
		m.mu.Unlock()
		defer func() {
			close(saved)
			m.mu.Lock()
			delete(m.saving, spec.ID)
			m.mu.Unlock()
		}()
	}

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
	wo := workflow.Options{Out: out, Err: info, GitTokens: tokens, Memory: cfg.Memory, CPUs: cfg.CPUs, Workspace: workspaceName(j.root)}
	if j.memory {
		if j.parent != "" {
			m.waitSaved(ctx, j.parent)
		}
		wo.Mem = m.memoryPlan(j)
	}
	wo.Ask = m.askFunc(spec.ID)
	wo.Tools = tools.ForReply()
	if j.parent == "" {
		wo.Tools = tools.ForNewConversation() // a reply keeps its conversation's name
	}
	// The turn is finished as soon as the work is pushed. Saving to memory goes
	// on in the background, so the owner can read the answer and reply at once.
	finished := false
	wo.OnWorkDone = func(res workflow.Result, werr error) {
		finished = true
		out.Flush()
		info.Flush()
		m.applyName(j, res.Name)
		m.finish(ctx, spec.ID, werr, secrets)
	}
	res, err := workflow.Run(ctx, rn, spec, wo)
	out.Flush()
	info.Flush()
	if j.memory && ctx.Err() == nil {
		m.saveMemory(j, res, err)
	}
	if !finished { // it failed before the work was done
		m.applyName(j, res.Name)
		m.finish(ctx, spec.ID, err, secrets)
	}
}

// applyName gives the first message of a conversation the title its agent asked for.
func (m *Manager) applyName(j job, name string) {
	if name == "" || j.parent != "" || name == j.spec.Task {
		return
	}
	if m.st.SetTask(j.spec.ID, name) == nil {
		_ = m.st.AddEvent(j.spec.ID, "log", "the agent named this conversation: "+name)
	}
}

// waitSaved waits (for a few minutes at most) until an earlier turn has saved its memory.
func (m *Manager) waitSaved(ctx context.Context, sessionID string) {
	m.mu.Lock()
	saved, ok := m.saving[sessionID]
	m.mu.Unlock()
	if !ok {
		return
	}
	select {
	case <-saved:
	case <-ctx.Done():
	case <-time.After(3 * time.Minute):
	}
}

// Saving reports whether a session is still saving its memory.
func (m *Manager) Saving(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.saving[sessionID]
	return ok
}

// workspaceName is the volume that holds a conversation's repos between turns.
func workspaceName(root string) string { return "unire-ws-" + root }

// rootOf is the first session of the conversation a new session belongs to.
func (m *Manager) rootOf(id, parent string) string {
	if parent == "" {
		return id
	}
	root := parent
	for i := 0; i < 100; i++ {
		s, ok, err := m.st.Session(root)
		if err != nil || !ok || s.ParentID == "" {
			break
		}
		root = s.ParentID
	}
	return root
}

// ForgetWorkspace removes a conversation's workspace when the conversation is deleted.
func (m *Manager) ForgetWorkspace(root string) {
	if k, ok := m.config().Runner.(runner.WorkspaceKeeper); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = k.RemoveWorkspace(ctx, workspaceName(root))
	}
}

// memoryPlan loads what earlier sessions learned about this session's repos.
func (m *Manager) memoryPlan(j job) *workflow.MemoryPlan {
	var briefs []memory.RepoBrief
	var past []store.Summary
	seen := map[string]bool{}
	for i, repo := range j.spec.Repos {
		key := store.RepoKey(j.refs[i])
		b := memory.RepoBrief{Dir: repo.Name, FullName: j.refs[i].FullName}
		if rm, ok, _ := m.st.RepoMemory(key); ok {
			b.Brief = rm.Brief
		}
		briefs = append(briefs, b)
		if list, err := m.st.RepoSummaries(key, memory.RecentSessions); err == nil {
			for _, s := range list {
				if !seen[s.SessionID] {
					seen[s.SessionID] = true
					past = append(past, s)
				}
			}
		}
	}
	sort.Slice(past, func(a, b int) bool { return past[a].CreatedAt.After(past[b].CreatedAt) })
	var ps []memory.PastSession
	for _, s := range past {
		ps = append(ps, memory.PastSession{Task: s.Task, Agent: s.Agent, Branch: s.Branch, Status: s.Status, Summary: s.Summary, When: s.CreatedAt})
	}
	return &workflow.MemoryPlan{Context: memory.BuildContext(briefs, ps), Repos: briefs}
}

// saveMemory stores the session's summary and any updated briefs.
func (m *Manager) saveMemory(j job, res workflow.Result, runErr error) {
	if res.Summary == "" {
		return
	}
	status := store.StatusDone
	if runErr != nil {
		status = store.StatusFailed
	}
	var keys []string
	for _, ref := range j.refs {
		keys = append(keys, store.RepoKey(ref))
	}
	err := m.st.SaveSummary(store.Summary{
		SessionID: j.spec.ID, Task: j.spec.Task, Agent: j.spec.Agent, Branch: j.spec.Branch(),
		Status: status, Summary: res.Summary, Source: res.SummarySource, Repos: keys,
	})
	if err != nil {
		_ = m.st.AddEvent(j.spec.ID, "log", "memory: could not save the summary")
		return
	}
	for i, repo := range j.spec.Repos {
		brief, ok := res.Briefs[repo.Name]
		if !ok {
			continue
		}
		if err := m.st.SetRepoMemory(store.RepoKey(j.refs[i]), brief, j.spec.ID); err == nil {
			_ = m.st.AddEvent(j.spec.ID, "log", "memory: updated the brief for "+j.refs[i].FullName)
		}
	}
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
	parent  string // the session this one continues, or empty
	root    string // the first session of the conversation: its id names the workspace

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

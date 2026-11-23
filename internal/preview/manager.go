package preview

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// State is where a preview is in its life.
type State string

const (
	StateDownloading State = "downloading" // pulling the recipe's image
	StateStarting    State = "starting"    // container running, app not answering yet
	StateReady       State = "ready"
	StateFailed      State = "failed"
	StateStopped     State = "stopped"
)

// Status is what the UI shows for one preview.
type Status struct {
	State   State
	Message string // short, for people: "Downloading ghcr.io/..." or why it failed
	Logs    string // tail of the container's output; set when it failed
	// Target is host:port of the app on the container network, for the proxy.
	// Empty until the container has been created.
	Target string
}

// RunSpec describes the preview container.
type RunSpec struct {
	Name      string
	Image     string
	Network   string
	Workspace string // docker volume holding the repos, mounted at /workspace
	WorkDir   string // absolute path inside the container
	User      string
	Command   []string
	Env       map[string]string
	Memory    string
	CPUs      string
}

// Engine runs containers. The Docker implementation is in docker.go; tests use a fake.
type Engine interface {
	HaveImage(ctx context.Context, image string) bool
	Pull(ctx context.Context, image string) error
	Run(ctx context.Context, s RunSpec) error
	Running(ctx context.Context, name string) bool
	Logs(ctx context.Context, name string, tail int) string
	Remove(ctx context.Context, name string) error
}

// StartOpts is what the manager needs to know about the session.
type StartOpts struct {
	Network   string
	Workspace string // docker volume with the session's repos
	RepoDir   string // the repo's folder under /workspace
	Memory    string
	CPUs      string
	// User runs the app as the same user as the agent container, so files it
	// writes into the shared workspace stay editable by the agent. Default 1000:1000.
	User string
}

// Manager starts, watches and stops previews. It is safe for concurrent use.
type Manager struct {
	Engine Engine
	// HTTPGet returns the status code of a GET. Tests replace it.
	HTTPGet      func(ctx context.Context, url string) (int, error)
	PollEvery    time.Duration // default 1s
	ReadyTimeout time.Duration // default 10 minutes: a first Flutter build is slow

	mu    sync.Mutex
	items map[string]*item
}

type item struct {
	status  Status
	name    string
	cancel  context.CancelFunc
	done    chan struct{}
	stopped bool
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// ContainerName is the container name for a preview key (session id and preview name).
func ContainerName(key string) string {
	return "unire-preview-" + unsafeName.ReplaceAllString(key, "-")
}

// Start begins a preview in the background and returns at once. Calling it for
// a preview that is already downloading, starting or ready changes nothing.
func (m *Manager) Start(key string, p Preview, o StartOpts) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items == nil {
		m.items = map[string]*item{}
	}
	if it, ok := m.items[key]; ok {
		switch it.status.State {
		case StateDownloading, StateStarting, StateReady:
			return it.status
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	it := &item{
		status: Status{State: StateDownloading, Message: "Preparing " + p.Image},
		name:   ContainerName(key),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	m.items[key] = it
	go m.run(ctx, it, p, o)
	return it.status
}

// Status returns the current status of a preview.
func (m *Manager) Status(key string) (Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[key]
	if !ok {
		return Status{}, false
	}
	return it.status, true
}

// Logs returns the tail of the preview container's output.
func (m *Manager) Logs(ctx context.Context, key string, tail int) string {
	m.mu.Lock()
	it, ok := m.items[key]
	m.mu.Unlock()
	if !ok {
		return ""
	}
	return m.Engine.Logs(ctx, it.name, tail)
}

// Stop ends a preview and removes its container. It waits for the background
// work to finish. Stopping an unknown or already stopped preview is fine.
func (m *Manager) Stop(ctx context.Context, key string) error {
	m.mu.Lock()
	it, ok := m.items[key]
	if ok {
		it.stopped = true
		it.status = Status{State: StateStopped, Message: "Stopped"}
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	it.cancel()
	select {
	case <-it.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return m.Engine.Remove(ctx, it.name)
}

func (m *Manager) set(it *item, st Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if it.stopped {
		return // Stop wins over a late update from the background work
	}
	it.status = st
}

func (m *Manager) fail(it *item, msg string) {
	logs := m.Engine.Logs(context.Background(), it.name, 40)
	_ = m.Engine.Remove(context.Background(), it.name)
	m.set(it, Status{State: StateFailed, Message: msg, Logs: logs})
}

func (m *Manager) run(ctx context.Context, it *item, p Preview, o StartOpts) {
	defer close(it.done)
	e := m.Engine
	if !e.HaveImage(ctx, p.Image) {
		m.set(it, Status{State: StateDownloading, Message: "Downloading " + p.Image + ". The first time can take several minutes."})
		if err := e.Pull(ctx, p.Image); err != nil {
			if ctx.Err() == nil {
				m.fail(it, "Could not download "+p.Image+": "+err.Error())
			}
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	target := fmt.Sprintf("%s:%d", it.name, p.Port)
	m.set(it, Status{State: StateStarting, Message: "Starting " + p.Name, Target: target})
	_ = e.Remove(ctx, it.name) // a leftover from an earlier run of the same preview
	user := o.User
	if user == "" {
		user = "1000:1000"
	}
	spec := RunSpec{
		Name: it.name, Image: p.Image, Network: o.Network, Workspace: o.Workspace,
		WorkDir: path.Join("/workspace", o.RepoDir, p.Dir), User: user,
		Command: p.Command, Memory: o.Memory, CPUs: o.CPUs,
		// The app runs as a user with no home folder of its own.
		Env: map[string]string{"HOME": "/tmp"},
	}
	if err := e.Run(ctx, spec); err != nil {
		if ctx.Err() == nil {
			m.fail(it, "Could not start the preview: "+err.Error())
		}
		return
	}
	m.waitReady(ctx, it, p, target)
}

func (m *Manager) waitReady(ctx context.Context, it *item, p Preview, target string) {
	every, limit := m.PollEvery, m.ReadyTimeout
	if every <= 0 {
		every = time.Second
	}
	if limit <= 0 {
		limit = 10 * time.Minute
	}
	deadline := time.Now().Add(limit)
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if !m.Engine.Running(ctx, it.name) {
			if ctx.Err() == nil {
				m.fail(it, "The preview stopped on its own. See the log.")
			}
			return
		}
		if m.ready(ctx, it.name, p, target) {
			m.set(it, Status{State: StateReady, Message: "Running", Target: target})
			return
		}
		if time.Now().After(deadline) {
			m.fail(it, fmt.Sprintf("The preview did not start answering within %s.", limit))
			return
		}
	}
}

func (m *Manager) ready(ctx context.Context, name string, p Preview, target string) bool {
	if p.Ready.LogContains != "" {
		return strings.Contains(m.Engine.Logs(ctx, name, 200), p.Ready.LogContains)
	}
	get := m.HTTPGet
	if get == nil {
		get = httpGet
	}
	rp := p.Ready.HTTPPath
	if !strings.HasPrefix(rp, "/") {
		rp = "/" + rp
	}
	code, err := get(ctx, "http://"+target+rp)
	return err == nil && code > 0 && code < 500
}

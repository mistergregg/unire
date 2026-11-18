package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mistergregg/unire/internal/agent"
	"github.com/mistergregg/unire/internal/safe"
	"github.com/mistergregg/unire/internal/sessions"
	"github.com/mistergregg/unire/internal/store"
)

// The health page answers "why is it not working?" without a terminal: each check says
// what it looked at, whether that is fine, and (when it is not) what to do.

const (
	healthOK      = "ok"
	healthWarn    = "warn"
	healthBad     = "bad"
	healthUnknown = "unknown"
)

type healthItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"` // ok, warn, bad or unknown
	Summary string `json:"summary"`
	Fix     string `json:"fix,omitempty"` // what to do, when it is not ok
}

// healthProbe is how the page asks Docker about the host. Tests replace it.
type healthProbe interface {
	DockerVersion(ctx context.Context) (string, error)
	ImageCreated(ctx context.Context, image string) (time.Time, bool, error) // found=false when the host does not have it
	VolumeExists(ctx context.Context, name string) (bool, error)
	SandboxContainers(ctx context.Context) (int, error)
}

type dockerProbe struct{}

func dockerOut(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (dockerProbe) DockerVersion(ctx context.Context) (string, error) {
	v, err := dockerOut(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return "", fmt.Errorf("%s", shorten(v, 200))
	}
	return v, nil
}

func (dockerProbe) ImageCreated(ctx context.Context, image string) (time.Time, bool, error) {
	out, err := dockerOut(ctx, "image", "inspect", "--format", "{{.Created}}", image)
	if err != nil {
		if strings.Contains(out, "No such image") || strings.Contains(out, "no such image") {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("%s", shorten(out, 200))
	}
	t, perr := time.Parse(time.RFC3339Nano, out)
	if perr != nil {
		return time.Time{}, true, nil // there, but its age is unknown
	}
	return t, true, nil
}

func (dockerProbe) VolumeExists(ctx context.Context, name string) (bool, error) {
	out, err := dockerOut(ctx, "volume", "inspect", name)
	if err != nil {
		if strings.Contains(strings.ToLower(out), "no such volume") {
			return false, nil
		}
		return false, fmt.Errorf("%s", shorten(out, 200))
	}
	return true, nil
}

func (dockerProbe) SandboxContainers(ctx context.Context) (int, error) {
	out, err := dockerOut(ctx, "ps", "-q", "--filter", "label=unire.session")
	if err != nil {
		return 0, fmt.Errorf("%s", shorten(out, 200))
	}
	return len(strings.Fields(out)), nil
}

// lastCleanup is what the cleanup loop last did.
type lastCleanup struct {
	mu         sync.Mutex
	at         time.Time
	logs       int64
	workspaces int
}

func humanBytes(n uint64) string {
	const k = 1024.0
	f := float64(n)
	switch {
	case f >= k*k*k*k:
		return fmt.Sprintf("%.1f TB", f/(k*k*k*k))
	case f >= k*k*k:
		return fmt.Sprintf("%.1f GB", f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%.0f MB", f/(k*k))
	}
	return fmt.Sprintf("%.0f KB", f/k)
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

// healthChecks runs every check, the slow ones at the same time, and returns them in a
// fixed order.
func (s *Server) healthChecks(ctx context.Context) []healthItem {
	sb := s.sandbox()
	var mu sync.Mutex
	var items []healthItem
	order := map[string]int{}
	add := func(i healthItem) {
		mu.Lock()
		defer mu.Unlock()
		order[i.ID] = len(order)
		items = append(items, i)
	}
	var wg sync.WaitGroup
	run := func(name string, f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer safe.Recover("health check "+name, func(err error) {
				add(healthItem{ID: name, Name: name, Status: healthUnknown, Summary: "the check itself failed: " + err.Error()})
			})
			f()
		}()
	}

	// Unire itself.
	version := "development build"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range bi.Settings {
			if kv.Key == "vcs.revision" && len(kv.Value) >= 7 {
				version = "build " + kv.Value[:7]
			}
		}
	}
	add(healthItem{ID: "unire", Name: "Unire", Status: healthOK, Summary: version})

	// Docker, the image and the login volume.
	run("docker", func() {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		v, err := s.probe.DockerVersion(cctx)
		if err != nil {
			add(healthItem{ID: "docker", Name: "Docker", Status: healthBad, Summary: "Docker is not reachable: " + err.Error(),
				Fix: "Unire needs the Docker socket (/var/run/docker.sock) mounted into its container, and the Docker service running."})
			return
		}
		add(healthItem{ID: "docker", Name: "Docker", Status: healthOK, Summary: "running, version " + v})
	})
	run("image", func() {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		created, found, err := s.probe.ImageCreated(cctx, sb.Image)
		switch {
		case err != nil:
			add(healthItem{ID: "image", Name: "Sandbox image", Status: healthUnknown, Summary: "could not check " + sb.Image + ": " + err.Error()})
		case !found:
			add(healthItem{ID: "image", Name: "Sandbox image", Status: healthWarn, Summary: sb.Image + " is not on this host yet",
				Fix: "The first chat downloads it, which can take several minutes. Pull it now in a terminal to avoid the wait."})
		case !created.IsZero() && time.Since(created) > 45*24*time.Hour:
			add(healthItem{ID: "image", Name: "Sandbox image", Status: healthWarn, Summary: sb.Image + " was built " + humanAge(time.Since(created)),
				Fix: "An old image may have outdated agents. Update the sandbox container image."})
		case created.IsZero():
			add(healthItem{ID: "image", Name: "Sandbox image", Status: healthOK, Summary: sb.Image})
		default:
			add(healthItem{ID: "image", Name: "Sandbox image", Status: healthOK, Summary: sb.Image + ", built " + humanAge(time.Since(created))})
		}
	})
	run("volume", func() {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ok, err := s.probe.VolumeExists(cctx, sb.CredsVolume)
		switch {
		case err != nil:
			add(healthItem{ID: "volume", Name: "Agent logins", Status: healthUnknown, Summary: "could not check volume " + sb.CredsVolume + ": " + err.Error()})
		case !ok:
			add(healthItem{ID: "volume", Name: "Agent logins", Status: healthWarn, Summary: "the login volume " + sb.CredsVolume + " does not exist yet",
				Fix: "Sign in to an agent on the Agents page; that creates it."})
		default:
			add(healthItem{ID: "volume", Name: "Agent logins", Status: healthOK, Summary: "volume " + sb.CredsVolume})
		}
	})

	// Sandboxes still running against what the database says is running.
	list, _ := s.store.Sessions(500)
	running, queued := 0, 0
	var oldest time.Time
	for _, x := range list {
		switch x.Status {
		case store.StatusRunning:
			running++
			if oldest.IsZero() || (!x.StartedAt.IsZero() && x.StartedAt.Before(oldest)) {
				oldest = x.StartedAt
			}
		case store.StatusQueued:
			queued++
		}
	}
	run("containers", func() {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		n, err := s.probe.SandboxContainers(cctx)
		switch {
		case err != nil:
			add(healthItem{ID: "containers", Name: "Sandbox containers", Status: healthUnknown, Summary: "could not list them: " + err.Error()})
		case n > running+1: // one may be starting or just ending
			add(healthItem{ID: "containers", Name: "Sandbox containers", Status: healthWarn,
				Summary: fmt.Sprintf("%d sandbox containers are running but only %d chat(s) are", n, running),
				Fix:     "They may be left over from a crash. They are removed when Unire restarts."})
		default:
			add(healthItem{ID: "containers", Name: "Sandbox containers", Status: healthOK, Summary: fmt.Sprintf("%d running, matching the chats that are", n)})
		}
	})
	chat := healthItem{ID: "chats", Name: "Chats", Status: healthOK, Summary: fmt.Sprintf("%d running, %d waiting for a slot (up to %d at once)", running, queued, sessions.MaxConcurrent)}
	if lim := s.limits(); lim.MaxRunMinutes > 0 && !oldest.IsZero() && time.Since(oldest) > time.Duration(lim.MaxRunMinutes)*time.Minute*8/10 {
		chat.Status, chat.Fix = healthWarn, "The longest-running chat is close to the run time limit and will be stopped."
		chat.Summary += fmt.Sprintf("; the oldest has run %d min", int(time.Since(oldest).Minutes()))
	}
	if queued > 0 && running >= sessions.MaxConcurrent {
		chat.Status, chat.Fix = healthWarn, "All slots are busy: new chats wait until one finishes. Stop one that is stuck."
	}
	add(chat)

	// Egress protection.
	if s.egressInfo.Available {
		add(healthItem{ID: "egress", Name: "Network protection", Status: healthOK, Summary: "available, on network " + s.egressInfo.Network})
	} else {
		add(healthItem{ID: "egress", Name: "Network protection", Status: healthWarn, Summary: "not available: " + s.egressInfo.Reason,
			Fix: "Chats with protected network modes cannot start; chats with full network access still can."})
	}

	// Disk and the database.
	if free, total, err := diskFree(s.dataDir); err == nil && total > 0 {
		pct := float64(free) / float64(total) * 100
		st, fix := healthOK, ""
		switch {
		case free < 1<<30:
			st, fix = healthBad, "Almost out of space: runs and the database will start to fail. Free up space on this disk."
		case free < 5<<30 || pct < 10:
			st, fix = healthWarn, "Getting low. Old workspaces are removed automatically (Settings, Limits), or free some space."
		}
		add(healthItem{ID: "disk", Name: "Disk (Unire's data)", Status: st, Summary: fmt.Sprintf("%s free of %s", humanBytes(free), humanBytes(total)), Fix: fix})
	}
	dbBytes := uint64(0)
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(filepath.Join(s.dataDir, store.DBFileName+suffix)); err == nil {
			dbBytes += uint64(fi.Size())
		}
	}
	if c, err := s.store.Counts(); err != nil {
		add(healthItem{ID: "database", Name: "Database", Status: healthBad, Summary: "cannot read it: " + err.Error()})
	} else if q, err := s.store.QuickCheck(); err != nil || q != "ok" {
		add(healthItem{ID: "database", Name: "Database", Status: healthBad, Summary: "the integrity check says: " + q,
			Fix: "Download a backup now (Settings), then restore from an earlier one."})
	} else {
		st, fix := healthOK, ""
		if c.Events > 2_000_000 {
			st, fix = healthWarn, "Lots of activity lines. Lower 'Keep activity lines' in Settings, Limits."
		}
		add(healthItem{ID: "database", Name: "Database", Status: st, Fix: fix,
			Summary: fmt.Sprintf("%s, %d chats, %d activity lines, %d memory summaries; integrity ok", humanBytes(dbBytes), c.Sessions, c.Events, c.Summaries)})
	}

	// Limits and the last cleanup.
	lim := s.limits()
	limits := healthItem{ID: "limits", Name: "Limits", Status: healthOK,
		Summary: fmt.Sprintf("run %s · activity %s · workspaces %s · memory %s", mins(lim.MaxRunMinutes), days(lim.KeepLogsDays), days(lim.KeepWorkspaceDays), memText(sb))}
	if lim.MaxRunMinutes == 0 {
		limits.Status, limits.Fix = healthWarn, "A stuck run is never stopped by itself. Set a longest run time in Settings, Limits."
	}
	add(limits)
	s.cleanup.mu.Lock()
	lc := lastCleanup{at: s.cleanup.at, logs: s.cleanup.logs, workspaces: s.cleanup.workspaces}
	s.cleanup.mu.Unlock()
	if lc.at.IsZero() {
		add(healthItem{ID: "cleanup", Name: "Cleanup", Status: healthOK, Summary: "has not run yet (a couple of minutes after start-up, then every six hours)"})
	} else {
		add(healthItem{ID: "cleanup", Name: "Cleanup", Status: healthOK, Summary: fmt.Sprintf("ran %s: removed %d activity lines, looked at %d idle workspaces", humanAge(time.Since(lc.at)), lc.logs, lc.workspaces)})
	}

	// Each agent's sign-in, and each git server's token.
	for _, info := range agent.Infos() {
		info := info
		run("agent-"+info.ID, func() {
			ad, err := agent.Get(info.ID)
			if err != nil {
				return
			}
			cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			e := s.agentStatus(cctx, ad)
			it := healthItem{ID: "agent-" + info.ID, Name: info.Label}
			switch {
			case e.known && e.loggedIn:
				it.Status, it.Summary = healthOK, "signed in"
			case e.known:
				it.Status, it.Summary, it.Fix = healthWarn, "not signed in", "Sign in on the Agents page."
			default:
				it.Status, it.Summary = healthUnknown, "sign-in status unknown"
				if e.note != "" {
					it.Summary += ": " + e.note
				}
			}
			add(it)
		})
	}
	servers, _ := s.store.GitServers()
	for _, g := range servers {
		g := g
		run("server-"+g.ID, func() {
			it := healthItem{ID: "server-" + g.ID, Name: "Git server " + g.Name}
			p, err := s.provider(g)
			if err != nil {
				it.Status, it.Summary, it.Fix = healthBad, err.Error(), "Replace the token on the Git servers page."
				add(it)
				return
			}
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if user, err := testWith(cctx, p); err != nil {
				it.Status, it.Summary, it.Fix = healthBad, shorten(err.Error(), 200), "Check the server is reachable and the token is valid (Git servers page)."
			} else {
				it.Status, it.Summary = healthOK, "connected as "+user
			}
			add(it)
		})
	}
	wg.Wait()

	sort.SliceStable(items, func(i, j int) bool { return order[items[i].ID] < order[items[j].ID] })
	return items
}

func mins(n int) string {
	if n == 0 {
		return "no limit"
	}
	return fmt.Sprintf("%d min", n)
}

func days(n int) string {
	if n == 0 {
		return "kept for ever"
	}
	return fmt.Sprintf("%d days", n)
}

func memText(sb Sandbox) string {
	if m := sb.EffectiveMemory(); m != "" {
		return m
	}
	return "no limit"
}

// getHealth runs the checks and says, overall, how things look.
func (s *Server) getHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	items := s.healthChecks(ctx)
	overall := healthOK
	for _, it := range items {
		switch {
		case it.Status == healthBad:
			overall = healthBad
		case (it.Status == healthWarn || it.Status == healthUnknown) && overall == healthOK:
			overall = healthWarn
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": overall, "checks": items})
}

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mistergregg/unire/addons"
	"github.com/mistergregg/unire/internal/runner"
)

// Add-on images are built on this host: one image FROM the sandbox image with
// every add-on that is on (addons.Dockerfile). Any combination works, and a new
// add-on needs no published image. An image is tagged
//
//	unire-sandbox-addons:<set>-<build>  the exact build (add-ons + base image id)
//	unire-sandbox-addons:<set>          the newest build of that set
//
// where <set> hashes the add-ons' files and the base image name. When the base
// image is updated, the previous build of the set keeps being used while the
// new one is built in the background, so only the very first build makes a
// chat wait.

const addonRepo = "unire-sandbox-addons"

// Docker calls, replaced in tests.
var (
	dockerImageID = func(ctx context.Context, image string) (string, error) {
		out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
		return strings.TrimSpace(string(out)), err
	}
	dockerHasImage = func(ctx context.Context, image string) bool {
		return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil
	}
	dockerBuild = func(ctx context.Context, tags []string, base string, tarball []byte, log io.Writer) error {
		args := []string{"build", "--build-arg", "BASE=" + base}
		for _, t := range tags {
			args = append(args, "-t", t)
		}
		cmd := exec.CommandContext(ctx, "docker", append(args, "-")...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(tarball), log, log
		return cmd.Run()
	}
	dockerUntagOld = func(ctx context.Context, setTag, keep string) {
		out, err := exec.CommandContext(ctx, "docker", "image", "ls", addonRepo, "--format", "{{.Repository}}:{{.Tag}}").Output()
		if err != nil {
			return
		}
		for _, t := range strings.Fields(string(out)) {
			if strings.HasPrefix(t, setTag+"-") && t != keep {
				_ = exec.CommandContext(ctx, "docker", "image", "rm", t).Run() // fails harmlessly while a sandbox uses it
			}
		}
	}
	addonBuildTimeout = 60 * time.Minute
)

var errAddonBuilding = errors.New("the add-on image is being built")

// addonBuild is one build of a set of add-ons, as Settings shows it.
type addonBuild struct {
	Addons   string    `json:"addons"`
	Tag      string    `json:"tag"`
	State    string    `json:"state"` // building, ready, failed
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	Error    string    `json:"error,omitempty"`
	Log      string    `json:"log,omitempty"` // the end of docker build's output

	done chan struct{}
	out  *tailBuffer
}

type addonBuilder struct {
	mu   sync.Mutex
	sets map[string]*addonBuild // newest build per set
}

func addonSetKey(base, contextHash string) string {
	h := sha256.Sum256([]byte(base + "\n" + contextHash))
	return hex.EncodeToString(h[:])[:12]
}

// status is the newest build of ids on base, or nil if none ran since start.
func (b *addonBuilder) status(base string, ids []string) *addonBuild {
	_, ch, err := addons.Context(ids)
	if err != nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	cur := b.sets[addonSetKey(base, ch)]
	if cur == nil {
		return nil
	}
	view := *cur
	if view.State == "building" || view.State == "failed" {
		view.Log = cur.out.String()
	}
	return &view
}

// image returns the image to run for ids on base: base itself with no add-ons,
// the build for the current base image, or the set's previous build while a
// new one is made. With wait, a chat waits for a first build (telling log);
// without, it only starts the build. bg is the context builds run in.
func (b *addonBuilder) image(ctx, bg context.Context, wg *sync.WaitGroup, base string, ids []string, wait bool, log io.Writer) (string, error) {
	ids = addons.Clean(ids)
	if len(ids) == 0 {
		return base, nil
	}
	tarball, ch, err := addons.Context(ids)
	if err != nil {
		return "", err
	}
	set := addonSetKey(base, ch)
	runner.EnsureFresh(ctx, base) // the build starts FROM it, so it must be on this host
	baseID, err := dockerImageID(ctx, base)
	if err != nil || baseID == "" {
		err := fmt.Errorf("the sandbox image %s is not on this host (is Docker reachable?), so the add-ons cannot be added to it", base)
		b.mu.Lock()
		if b.sets == nil {
			b.sets = map[string]*addonBuild{}
		}
		if cur := b.sets[set]; cur == nil || cur.State != "building" {
			now := time.Now()
			b.sets[set] = &addonBuild{Addons: strings.Join(ids, ","), State: "failed", Started: now, Finished: now, Error: err.Error(), out: &tailBuffer{max: 4000}}
		}
		b.mu.Unlock()
		return "", err
	}
	setTag := addonRepo + ":" + set
	h := sha256.Sum256([]byte(set + "\n" + baseID))
	tag := setTag + "-" + hex.EncodeToString(h[:])[:12]
	if dockerHasImage(ctx, tag) {
		return tag, nil
	}

	b.mu.Lock()
	if b.sets == nil {
		b.sets = map[string]*addonBuild{}
	}
	cur := b.sets[set]
	if cur == nil || cur.Tag != tag || cur.State == "failed" {
		cur = &addonBuild{Addons: strings.Join(ids, ","), Tag: tag, State: "building", Started: time.Now(), done: make(chan struct{}), out: &tailBuffer{max: 4000}}
		b.sets[set] = cur
		wg.Add(1)
		go func(cur *addonBuild) {
			defer wg.Done()
			bctx, cancel := context.WithTimeout(bg, addonBuildTimeout)
			defer cancel()
			err := dockerBuild(bctx, []string{tag, setTag}, base, tarball, cur.out)
			if err == nil {
				dockerUntagOld(bctx, setTag, tag)
			}
			b.mu.Lock()
			cur.Finished = time.Now()
			if err != nil {
				cur.State, cur.Error = "failed", err.Error()
			} else {
				cur.State = "ready"
			}
			b.mu.Unlock()
			close(cur.done)
		}(cur)
	}
	b.mu.Unlock()

	if dockerHasImage(ctx, setTag) {
		return setTag, nil // an earlier build of these add-ons, on an older base image
	}
	if !wait {
		return "", errAddonBuilding
	}
	if log != nil {
		fmt.Fprintf(log, "building the sandbox image with the add-ons %s on this host: the first time takes several minutes\n", strings.Join(ids, ", "))
	}
	t := time.Now()
	select {
	case <-cur.done:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if cur.State != "ready" {
		return "", fmt.Errorf("building the add-on image failed (see Settings, Add-ons): %s", cur.Error)
	}
	if log != nil {
		fmt.Fprintf(log, "add-on image built (%.0fs)\n", time.Since(t).Seconds())
	}
	return tag, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// addonRunner starts sandboxes in the add-on image, building it first if needed.
type addonRunner struct {
	s   *Server
	sb  Sandbox
	ids []string
}

func (r addonRunner) Start(ctx context.Context, spec runner.StartSpec) (runner.Sandbox, error) {
	image, err := r.s.addonBuilds.image(ctx, r.s.bgCtx, &r.s.bgWG, r.sb.Image, r.ids, true, spec.Log)
	if err != nil {
		return nil, err
	}
	sb := r.sb
	sb.Image = image
	return r.s.runnerFactory(sb).Start(ctx, spec)
}

// RemoveWorkspace: workspaces do not depend on the image.
func (r addonRunner) RemoveWorkspace(ctx context.Context, name string) error {
	if k, ok := r.s.runnerFactory(r.sb).(runner.WorkspaceKeeper); ok {
		return k.RemoveWorkspace(ctx, name)
	}
	return nil
}

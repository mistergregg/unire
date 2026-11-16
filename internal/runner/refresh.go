package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// The sandbox image is pulled once by Docker and then never refreshed, so after
// Unire is updated the host can keep an old image that lacks a newer agent.
// Refresh pulls the image when it is held in a registry. When the image is
// already current, a pull is a quick check that downloads nothing.

const refreshEvery = 30 * time.Minute

var (
	refreshMu   sync.Mutex // one pull at a time; callers wait for one already running
	refreshedAt = map[string]time.Time{}
	pullImage   = func(ctx context.Context, image string) error {
		return exec.CommandContext(ctx, "docker", "pull", "--quiet", image).Run()
	}
	now = time.Now
)

// fromRegistry reports whether image names a registry (ghcr.io/owner/name):
// a first part with a dot or a port. A plain local name such as "unire-sandbox"
// has nothing to pull.
func fromRegistry(image string) bool {
	first, _, ok := strings.Cut(image, "/")
	return ok && (strings.ContainsAny(first, ".:") || first == "localhost")
}

// Refresh brings the image up to date, at most once per refreshEvery. A failed
// pull (no internet, registry down) is not an error: the local copy is used.
func Refresh(ctx context.Context, image string) {
	if !fromRegistry(image) {
		return
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if t, ok := refreshedAt[image]; ok && now().Sub(t) < refreshEvery {
		return
	}
	if err := pullImage(ctx, image); err != nil {
		fmt.Fprintf(os.Stderr, "could not update the sandbox image %s (using the copy on this host): %v\n", image, err)
		return
	}
	refreshedAt[image] = now()
}

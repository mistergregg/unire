package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mistergregg/unire/internal/safe"
)

// The sandbox image is pulled once by Docker and then never refreshed, so after
// Unire is updated the host can keep an old image that lacks a newer agent.
// Refresh pulls the image when it is held in a registry. When the image is
// already current, a pull is a quick check that downloads nothing; when it is
// not, it is a download of a few gigabytes, so nothing the owner is waiting on
// (a launch, a sign-in check) may wait for it: see EnsureFresh.

const refreshEvery = 30 * time.Minute

var (
	pullMu      sync.Mutex // one pull at a time; callers of Refresh wait for one already running
	stateMu     sync.Mutex // guards refreshedAt and inflight
	refreshedAt = map[string]time.Time{}
	inflight    = map[string]bool{}
	pullImage   = func(ctx context.Context, image string) error {
		return exec.CommandContext(ctx, "docker", "pull", "--quiet", image).Run()
	}
	haveImage = func(ctx context.Context, image string) bool {
		return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil
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

func refreshedRecently(image string) bool {
	stateMu.Lock()
	defer stateMu.Unlock()
	t, ok := refreshedAt[image]
	return ok && now().Sub(t) < refreshEvery
}

// Refresh brings the image up to date, at most once per refreshEvery, and waits
// for the pull. A failed pull (no internet, registry down) is not an error: the
// local copy is used.
func Refresh(ctx context.Context, image string) {
	if !fromRegistry(image) {
		return
	}
	pullMu.Lock()
	defer pullMu.Unlock()
	if refreshedRecently(image) {
		return
	}
	if err := pullImage(ctx, image); err != nil {
		fmt.Fprintf(os.Stderr, "could not update the sandbox image %s (using the copy on this host): %v\n", image, err)
		return
	}
	stateMu.Lock()
	refreshedAt[image] = now()
	stateMu.Unlock()
}

// EnsureFresh is what a launch or a check calls before using the image. If the
// host already has the image it returns at once and updates it in the
// background (the next sandbox gets the new one), so a pull never leaves the
// owner waiting. Only when there is no copy at all does it wait for the download.
func EnsureFresh(ctx context.Context, image string) {
	if !fromRegistry(image) {
		return
	}
	if !haveImage(ctx, image) {
		Refresh(ctx, image)
		return
	}
	if refreshedRecently(image) {
		return
	}
	stateMu.Lock()
	if inflight[image] {
		stateMu.Unlock()
		return
	}
	inflight[image] = true
	stateMu.Unlock()
	go func() {
		defer safe.Recover("refresh the sandbox image", nil)
		Refresh(context.Background(), image)
		stateMu.Lock()
		delete(inflight, image)
		stateMu.Unlock()
	}()
}

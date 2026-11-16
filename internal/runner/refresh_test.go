package runner

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fakePull(t *testing.T) *[]string {
	t.Helper()
	var pulled []string
	oldPull, oldNow := pullImage, now
	clock := time.Unix(1000, 0)
	pullImage = func(_ context.Context, image string) error { pulled = append(pulled, image); return nil }
	now = func() time.Time { return clock }
	refreshedAt = map[string]time.Time{}
	oldHave := haveImage
	inflight = map[string]bool{}
	t.Cleanup(func() {
		pullImage, now, haveImage = oldPull, oldNow, oldHave
		refreshedAt = map[string]time.Time{}
		inflight = map[string]bool{}
	})
	return &pulled
}

func TestRefreshPullsRegistryImagesOnlyOnce(t *testing.T) {
	pulled := fakePull(t)
	Refresh(context.Background(), "ghcr.io/x/unire-sandbox:latest")
	Refresh(context.Background(), "ghcr.io/x/unire-sandbox:latest")
	if len(*pulled) != 1 {
		t.Fatalf("pulled %v, want exactly one pull", *pulled)
	}
}

func TestRefreshChecksAgainAfterAWhile(t *testing.T) {
	pulled := fakePull(t)
	Refresh(context.Background(), "ghcr.io/x/s")
	oldNow := now
	now = func() time.Time { return oldNow().Add(refreshEvery + time.Second) }
	Refresh(context.Background(), "ghcr.io/x/s")
	if len(*pulled) != 2 {
		t.Fatalf("pulled %v, want two pulls", *pulled)
	}
}

func TestRefreshSkipsLocalImages(t *testing.T) {
	pulled := fakePull(t)
	Refresh(context.Background(), "unire-sandbox")
	Refresh(context.Background(), "myuser/unire-sandbox") // no dot or port in the first part: not a registry host
	if len(*pulled) != 0 {
		t.Fatalf("pulled %v, want none", *pulled)
	}
}

func TestFailedPullIsRetriedNextTime(t *testing.T) {
	pulled := fakePull(t)
	fail := true
	pullImage = func(_ context.Context, image string) error {
		*pulled = append(*pulled, image)
		if fail {
			return errors.New("offline")
		}
		return nil
	}
	Refresh(context.Background(), "ghcr.io/x/s")
	fail = false
	Refresh(context.Background(), "ghcr.io/x/s")
	if len(*pulled) != 2 {
		t.Fatalf("pulled %v, want a retry after the failure", *pulled)
	}
}

func TestAnImageOnTheHostIsNeverWaitedFor(t *testing.T) {
	fakePull(t)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	pullImage = func(context.Context, string) error {
		started <- struct{}{}
		<-release // a slow download
		return nil
	}
	haveImage = func(context.Context, string) bool { return true }
	done := make(chan struct{})
	go func() { EnsureFresh(context.Background(), "ghcr.io/x/s"); close(done) }()
	select {
	case <-done: // returned without waiting for the pull
	case <-time.After(2 * time.Second):
		t.Fatal("EnsureFresh waited for the download although the image is on the host")
	}
	<-started // the update is going on in the background
	// A second call while it runs does not start another pull or wait.
	EnsureFresh(context.Background(), "ghcr.io/x/s")
	close(release)
}

func TestWithNoImageAtAllItWaitsForTheDownload(t *testing.T) {
	pulled := fakePull(t)
	haveImage = func(context.Context, string) bool { return false }
	EnsureFresh(context.Background(), "ghcr.io/x/s")
	if len(*pulled) != 1 {
		t.Errorf("pulled %v: with no copy of the image the pull must finish before EnsureFresh returns", *pulled)
	}
}

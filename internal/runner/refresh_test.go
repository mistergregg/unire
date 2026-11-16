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
	t.Cleanup(func() { pullImage, now = oldPull, oldNow; refreshedAt = map[string]time.Time{} })
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

package api

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFixedWindowSourceArtifactLimiterSeparatesActionsAndCleansEntries(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	limiter := newFixedWindowSourceArtifactLimiter(2, 3, 2, func() time.Time { return now })
	for attempt := 1; attempt <= 3; attempt++ {
		allowed, retry := limiter.Allow("tenant\x00user", sourceArtifactActionCreate)
		if (attempt <= 2) != allowed {
			t.Fatalf("create attempt %d allowed=%t", attempt, allowed)
		}
		if !allowed && retry <= 0 {
			t.Fatal("rejected request has no retry delay")
		}
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if allowed, _ := limiter.Allow("tenant\x00user", sourceArtifactActionComplete); !allowed {
			t.Fatalf("complete attempt %d unexpectedly shared create limit", attempt)
		}
	}
	now = now.Add(time.Minute)
	if allowed, _ := limiter.Allow("tenant\x00user", sourceArtifactActionCreate); !allowed {
		t.Fatal("expired create window was not reset")
	}
	_, _ = limiter.Allow("tenant\x00other", sourceArtifactActionCreate)
	_, _ = limiter.Allow("tenant\x00third", sourceArtifactActionCreate)
	if limiter.entryCount() > 2 {
		t.Fatalf("limiter entries grew beyond bound: %d", limiter.entryCount())
	}
}

func TestFixedWindowSourceArtifactLimiterIsConcurrent(t *testing.T) {
	limiter := newFixedWindowSourceArtifactLimiter(20, 60, 100, time.Now)
	var allowed atomic.Int64
	var group sync.WaitGroup
	for index := 0; index < 100; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if ok, _ := limiter.Allow("tenant\x00user", sourceArtifactActionCreate); ok {
				allowed.Add(1)
			}
		}()
	}
	group.Wait()
	if allowed.Load() != 20 {
		t.Fatalf("allowed=%d, want 20", allowed.Load())
	}
}

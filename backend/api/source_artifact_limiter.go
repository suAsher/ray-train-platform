package api

import (
	"sync"
	"time"
)

type SourceArtifactAction string

type sourceArtifactAction = SourceArtifactAction

const (
	sourceArtifactActionCreate   sourceArtifactAction = "create"
	sourceArtifactActionComplete sourceArtifactAction = "complete"
)

type SourceArtifactLimiter interface {
	Allow(string, SourceArtifactAction) (bool, time.Duration)
}

type sourceArtifactRateWindow struct {
	count    int
	resetsAt time.Time
}

type fixedWindowSourceArtifactLimiter struct {
	mu            sync.Mutex
	entries       map[string]sourceArtifactRateWindow
	createLimit   int
	completeLimit int
	maxEntries    int
	now           func() time.Time
}

func newFixedWindowSourceArtifactLimiter(createLimit, completeLimit, maxEntries int, now func() time.Time) *fixedWindowSourceArtifactLimiter {
	if now == nil {
		now = time.Now
	}
	return &fixedWindowSourceArtifactLimiter{
		entries: make(map[string]sourceArtifactRateWindow), createLimit: createLimit,
		completeLimit: completeLimit, maxEntries: maxEntries, now: now,
	}
}

func newDefaultSourceArtifactLimiter() SourceArtifactLimiter {
	return newFixedWindowSourceArtifactLimiter(20, 60, 10000, time.Now)
}

func (limiter *fixedWindowSourceArtifactLimiter) Allow(principalKey string, action sourceArtifactAction) (bool, time.Duration) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now().UTC()
	limiter.removeExpired(now)
	key := string(action) + "\x00" + principalKey
	window, exists := limiter.entries[key]
	if !exists {
		if len(limiter.entries) >= limiter.maxEntries {
			return false, time.Minute
		}
		window = sourceArtifactRateWindow{resetsAt: now.Add(time.Minute)}
	}
	limit := limiter.createLimit
	if action == sourceArtifactActionComplete {
		limit = limiter.completeLimit
	}
	if window.count >= limit {
		return false, window.resetsAt.Sub(now)
	}
	window.count++
	limiter.entries[key] = window
	return true, 0
}

func (limiter *fixedWindowSourceArtifactLimiter) removeExpired(now time.Time) {
	for key, window := range limiter.entries {
		if !now.Before(window.resetsAt) {
			delete(limiter.entries, key)
		}
	}
}

func (limiter *fixedWindowSourceArtifactLimiter) entryCount() int {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return len(limiter.entries)
}

package rayapi

import (
	"sync"
	"time"
)

const (
	defaultUploadRateLimit  = 20
	defaultUploadMaxEntries = 10000
)

type UploadLimiter interface {
	Allow(string) (bool, time.Duration)
}

type uploadRateWindow struct {
	count    int
	resetsAt time.Time
}

type fixedWindowUploadLimiter struct {
	mu         sync.Mutex
	entries    map[string]uploadRateWindow
	limit      int
	maxEntries int
	now        func() time.Time
}

func newFixedWindowUploadLimiter(limit, maxEntries int, now func() time.Time) *fixedWindowUploadLimiter {
	if now == nil {
		now = time.Now
	}
	return &fixedWindowUploadLimiter{entries: make(map[string]uploadRateWindow), limit: limit, maxEntries: maxEntries, now: now}
}

func (limiter *fixedWindowUploadLimiter) Allow(principalKey string) (bool, time.Duration) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now().UTC()
	for key, window := range limiter.entries {
		if !now.Before(window.resetsAt) {
			delete(limiter.entries, key)
		}
	}
	window, exists := limiter.entries[principalKey]
	if !exists {
		if len(limiter.entries) >= limiter.maxEntries {
			return false, time.Minute
		}
		window = uploadRateWindow{resetsAt: now.Add(time.Minute)}
	}
	if window.count >= limiter.limit {
		return false, window.resetsAt.Sub(now)
	}
	window.count++
	limiter.entries[principalKey] = window
	return true, 0
}

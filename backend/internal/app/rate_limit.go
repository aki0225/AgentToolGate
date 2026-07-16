package app

import (
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

var errRateLimited = errorRateLimited("rate limit exceeded")

type errorRateLimited string

func (e errorRateLimited) Error() string {
	return string(e)
}

func (a *App) allowWorkspaceToolCall(workspaceID string) bool {
	limitPerMinute := a.cfg.RateLimitPerMinute
	if limitPerMinute <= 0 {
		return true
	}

	limiter := a.workspaceRateLimiter(workspaceID, limitPerMinute)
	return limiter.Allow()
}

func (a *App) workspaceRateLimiter(workspaceID string, limitPerMinute int) *rate.Limiter {
	now := time.Now().UTC()
	if value, ok := a.rateLimiters.Load(workspaceID); ok {
		if entry, ok := value.(*workspaceRateLimiterEntry); ok {
			entry.touch(now)
			return entry.limiter
		}
	}

	entry := newWorkspaceRateLimiterEntry(limitPerMinute, now)
	actual, loaded := a.rateLimiters.LoadOrStore(workspaceID, entry)
	if loaded {
		if stored, ok := actual.(*workspaceRateLimiterEntry); ok {
			stored.touch(now)
			return stored.limiter
		}
	}
	return entry.limiter
}

func (a *App) reapExpiredWorkspaceRateLimiters(now time.Time) int {
	idleTimeout := time.Duration(a.cfg.RateLimitIdleTimeoutSec) * time.Second
	if idleTimeout <= 0 {
		return 0
	}

	removed := 0
	a.rateLimiters.Range(func(key, value any) bool {
		entry, ok := value.(*workspaceRateLimiterEntry)
		if !ok {
			return true
		}
		lastUsed := entry.lastUsedTime()
		if lastUsed.IsZero() || now.Sub(lastUsed) <= idleTimeout {
			return true
		}
		a.rateLimiters.Delete(key)
		removed++
		return true
	})
	return removed
}

func newWorkspaceLimiterMap() sync.Map {
	return sync.Map{}
}

func newWorkspaceRateLimiterEntry(limitPerMinute int, now time.Time) *workspaceRateLimiterEntry {
	interval := time.Minute / time.Duration(limitPerMinute)
	if interval <= 0 {
		interval = time.Second
	}

	entry := &workspaceRateLimiterEntry{
		limiter: rate.NewLimiter(rate.Every(interval), limitPerMinute),
	}
	entry.touch(now)
	return entry
}

type workspaceRateLimiterEntry struct {
	limiter  *rate.Limiter
	lastUsed atomic.Int64
}

func (e *workspaceRateLimiterEntry) touch(now time.Time) {
	e.lastUsed.Store(now.UTC().UnixNano())
}

func (e *workspaceRateLimiterEntry) lastUsedTime() time.Time {
	nanos := e.lastUsed.Load()
	if nanos <= 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos).UTC()
}

// Package ratelimit enforces per-installation quotas (ADR-0035 "Abuse is a
// cost vector", Jira MOD-86): every job already carries an installation
// identity, so one tenant's runaway webhooks cannot loop the service past a
// free tier. A fixed window per installation is deliberately simple — the
// goal is a blast-radius cap, not fairness shaping.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a fixed-window per-installation rate limiter.
type Limiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	counts    map[int64]*windowCount
	now       func() time.Time
	lastSweep time.Time
}

type windowCount struct {
	windowStart time.Time
	count       int
}

// New returns a Limiter allowing limit accepted deliveries per installation
// per window. limit <= 0 disables limiting (every Allow returns true).
func New(limit int, window time.Duration) *Limiter {
	return &Limiter{
		limit:  limit,
		window: window,
		counts: map[int64]*windowCount{},
		now:    time.Now,
	}
}

// SetClock overrides the clock, for tests.
func (l *Limiter) SetClock(now func() time.Time) { l.now = now }

// Allow reports whether this installation is within quota, counting the
// call when it is. Over-quota deliveries are dropped by the caller.
func (l *Limiter) Allow(installationID int64) bool {
	if l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)

	wc := l.counts[installationID]
	if wc == nil || now.Sub(wc.windowStart) >= l.window {
		wc = &windowCount{windowStart: now}
		l.counts[installationID] = wc
	}
	if wc.count >= l.limit {
		return false
	}
	wc.count++
	return true
}

// sweepLocked removes every installation's entry whose window has fully
// expired, so counts stays bounded by the number of installations active
// within the last window rather than growing forever with every distinct
// installation this Limiter has ever seen across its process lifetime. It
// runs at most once per window — a full scan on every Allow call would
// defeat the point of a fixed-window limiter's O(1) hot path — so this is
// amortized O(1) per call. l.mu must already be held.
func (l *Limiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	for id, wc := range l.counts {
		if now.Sub(wc.windowStart) >= l.window {
			delete(l.counts, id)
		}
	}
}

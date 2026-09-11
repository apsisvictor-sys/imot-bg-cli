package mcpserver

import (
	"context"
	"sync"
	"time"
)

// Limiter enforces the two politeness rules for live scraping: a bounded number
// of simultaneous operations, and a minimum gap between them. Cached answers
// bypass it entirely, which is what keeps a busy day of colleague questions
// from turning into a busy day of imot.bg requests.
type Limiter struct {
	sem        chan struct{}
	minSpacing time.Duration

	mu   sync.Mutex
	last time.Time
}

// NewLimiter creates a limiter allowing maxConcurrent live operations spaced at
// least minSpacing apart.
func NewLimiter(maxConcurrent int, minSpacing time.Duration) *Limiter {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Limiter{
		sem:        make(chan struct{}, maxConcurrent),
		minSpacing: minSpacing,
	}
}

// Acquire blocks until a live slot is free and the minimum spacing has elapsed.
// The returned release function is safe to call more than once.
func (l *Limiter) Acquire(ctx context.Context) (func(), error) {
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	release := sync.OnceFunc(func() { <-l.sem })

	// Reserve the next start time while holding the lock, then wait outside it
	// so concurrent waiters queue predictably instead of serializing on sleep.
	l.mu.Lock()
	now := time.Now()
	start := now
	if l.minSpacing > 0 && now.Before(l.last.Add(l.minSpacing)) {
		start = l.last.Add(l.minSpacing)
	}
	l.last = start
	l.mu.Unlock()

	if wait := time.Until(start); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	return release, nil
}

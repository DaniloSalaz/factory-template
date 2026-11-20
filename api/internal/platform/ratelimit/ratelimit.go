// Package ratelimit is the port for request quotas (design, section 6) and its
// in-memory implementation. A shared store (Redis, Postgres) can implement the same port
// once the API runs more than one replica.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Limiter decides whether one more request under key fits in its quota.
type Limiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

// Memory is a fixed-window limiter held in process memory: at most limit requests per key
// in each window. The whole table is dropped when a window ends, so its size is bounded
// by the keys seen in one window. Each replica counts on its own.
type Memory struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu     sync.Mutex
	start  time.Time
	counts map[string]int
}

// NewMemory builds a Memory limiter. now is the clock; nil means time.Now.
func NewMemory(limit int, window time.Duration, now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{limit: limit, window: window, now: now, counts: map[string]int{}}
}

// Allow implements Limiter.
func (m *Memory) Allow(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if now.Sub(m.start) >= m.window || now.Before(m.start) {
		m.start = now
		clear(m.counts)
	}
	if m.counts[key] >= m.limit {
		return false, nil
	}
	m.counts[key]++
	return true, nil
}

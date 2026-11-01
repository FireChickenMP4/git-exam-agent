package httpapi

import (
	"sync"
	"time"
)

// limiter 是简单的令牌桶限流器：burst 次突发，按 per 窗口匀速补充。
type limiter struct {
	mu      sync.Mutex
	entries map[string]*limitEntry
	rate    float64
	burst   float64
	now     func() time.Time
}

type limitEntry struct {
	tokens float64
	last   time.Time
}

func newLimiter(burst int, per time.Duration) *limiter {
	return &limiter{
		entries: make(map[string]*limitEntry),
		burst:   float64(burst),
		rate:    float64(burst) / per.Seconds(),
		now:     time.Now,
	}
}

// Allow 判断某个 key 当前是否允许一次请求。
func (l *limiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.entries) > 10000 {
		for k, e := range l.entries {
			if now.Sub(e.last) > 10*time.Minute {
				delete(l.entries, k)
			}
		}
	}

	e, ok := l.entries[key]
	if !ok {
		e = &limitEntry{tokens: l.burst, last: now}
		l.entries[key] = e
	}
	e.tokens += now.Sub(e.last).Seconds() * l.rate
	if e.tokens > l.burst {
		e.tokens = l.burst
	}
	e.last = now
	if e.tokens < 1 {
		return false
	}
	e.tokens--
	return true
}

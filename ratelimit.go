// Package sikasa: ratelimit.go
// Purpose: Provides an in-memory sliding window rate limiter for commands and keywords.
package sikasa

import (
	"sync"
	"time"
)

// RateLimitConfig represents the configuration for a rate limit.
// It specifies how many times an action can be performed within a given duration.
type RateLimitConfig struct {
	Count    int
	Duration time.Duration
}

// RateLimitInterval is a helper to construct a RateLimitConfig.
//
//	params:
//	      count:    maximum number of allowed requests
//	      interval: time window in which requests are counted
//	returns:
//	      RateLimitConfig
func RateLimitInterval(count int, interval time.Duration) RateLimitConfig {
	return RateLimitConfig{Count: count, Duration: interval}
}

// rateLimiter implements a simple sliding window per user ID.
type rateLimiter struct {
	mu     sync.Mutex
	config RateLimitConfig
	// maps userID to a slice of timestamps when they fired the rule
	history map[string][]time.Time
}

func newRateLimiter(cfg RateLimitConfig) *rateLimiter {
	return &rateLimiter{
		config:  cfg,
		history: make(map[string][]time.Time),
	}
}

// Allow checks if the given user is allowed to proceed based on the rate limit.
// It cleans up old timestamps outside the current window.
func (rl *rateLimiter) Allow(userID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-rl.config.Duration)

	// Filter timestamps that are still within the window
	var valid []time.Time
	for _, t := range rl.history[userID] {
		if t.After(windowStart) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.config.Count {
		// User has hit the limit
		rl.history[userID] = valid // save cleanup
		return false
	}

	// Allowed, record this request
	valid = append(valid, now)
	rl.history[userID] = valid
	return true
}

// wrapMsgHandler wraps a MsgHandler with rate limiting logic.
func wrapMsgHandler(h MsgHandler, limits []RateLimitConfig) MsgHandler {
	if len(limits) == 0 {
		return h
	}

	// We only take the first RateLimitConfig to keep it simple.
	rl := newRateLimiter(limits[0])

	return func(ctx *MsgCtx) error {
		if !rl.Allow(ctx.Author().ID.String()) {
			// Rate limited! We could optionally reply with a warning here,
			// but silently dropping spam is usually better for keywords.
			return nil
		}
		return h(ctx)
	}
}

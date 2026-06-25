// Package sikasa: voice_recovery.go
// Purpose: Watches the slog stream for DAVE/voice errors that indicate the
// session has desynchronized ("no active epoch") and triggers a Reconnect()
// on the affected VoiceCtx. The watcher is layered on top of the user's
// own slog handler so log output is unchanged.
//
// Key Components:
//   - recoveryHandler:  slog.Handler that wraps another handler, sniffs error
//     records, and dispatches recoveries
//   - installRecovery:  installs the wrapper around the bot's logger
//
// Note: Reconnect is rate-limited per guild (one attempt every 30s) so a
// flood of "no active epoch" errors does not stack reconnect attempts on top
// of each other.
package sikasa

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// recoveryHandler wraps another slog.Handler and watches for voice errors
// that warrant an automatic reconnect.
type recoveryHandler struct {
	inner slog.Handler
	bot   *Bot

	mu          sync.Mutex
	lastAttempt map[string]time.Time
}

// recoveryDebounce is the minimum interval between reconnect attempts for the
// same guild. Discord usually finishes a fresh DAVE handshake in well under
// this window, so 30s is enough headroom to avoid stacking attempts while
// still being snappy enough that a transient blip is recovered before the
// listener really notices.
const recoveryDebounce = 30 * time.Second

// recoveryTriggers are substrings that, when seen in a log error message,
// trigger a reconnect attempt. Matched case-insensitively. Kept as a small
// list so future DAVE edge cases can be added without code changes.
var recoveryTriggers = []string{
	"no active epoch",
	"failed to encrypt packet",
	"shard is not ready",
	"session is no longer valid",
}

func newRecoveryHandler(inner slog.Handler, b *Bot) *recoveryHandler {
	return &recoveryHandler{
		inner:       inner,
		bot:         b,
		lastAttempt: make(map[string]time.Time),
	}
}

func (h *recoveryHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.inner.Enabled(ctx, lvl)
}

func (h *recoveryHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level == slog.LevelDebug {
		low := strings.ToLower(r.Message)
		isVerbose := strings.Contains(low, "payload") ||
			strings.Contains(low, "heartbeat") ||
			strings.Contains(low, "websocket") ||
			strings.Contains(low, "write") ||
			strings.Contains(low, "read") ||
			strings.Contains(low, "send") ||
			strings.Contains(low, "recv") ||
			strings.Contains(low, "receive")

		if isVerbose {
			r.Level = slog.LevelDebug - 4
		}
	}
	err := h.inner.Handle(ctx, r)
	if r.Level >= slog.LevelError {
		h.maybeRecover(r)
	}
	return err
}

func (h *recoveryHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &recoveryHandler{
		inner:       h.inner.WithAttrs(attrs),
		bot:         h.bot,
		lastAttempt: h.lastAttempt,
	}
}

func (h *recoveryHandler) WithGroup(name string) slog.Handler {
	return &recoveryHandler{
		inner:       h.inner.WithGroup(name),
		bot:         h.bot,
		lastAttempt: h.lastAttempt,
	}
}

// maybeRecover scans a single log record for a recovery trigger. When a match
// is found and the per-guild debounce window has elapsed, kicks off a
// Reconnect in a goroutine. The decision uses the message body and any "err"
// attribute, since disgo logs the underlying error in either place.
func (h *recoveryHandler) maybeRecover(r slog.Record) {
	body := r.Message
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "err" || a.Key == "error" {
			body += " " + a.Value.String()
		}
		return true
	})
	low := strings.ToLower(body)
	matched := false
	for _, trig := range recoveryTriggers {
		if strings.Contains(low, trig) {
			matched = true
			break
		}
	}
	if !matched {
		return
	}

	// Without a guild attribute we cannot pick a connection. Try every active
	// session: in practice there is usually only one, and reconnecting an
	// unaffected session is harmless (it just restarts the current track).
	h.bot.voicesMu.Lock()
	targets := make([]*VoiceCtx, 0, len(h.bot.voices))
	for _, v := range h.bot.voices {
		targets = append(targets, v)
	}
	h.bot.voicesMu.Unlock()

	now := time.Now()
	for _, v := range targets {
		key := v.guildID.String()
		h.mu.Lock()
		last := h.lastAttempt[key]
		if now.Sub(last) < recoveryDebounce {
			h.mu.Unlock()
			continue
		}
		h.lastAttempt[key] = now
		h.mu.Unlock()

		go func(vctx *VoiceCtx) {
			if err := vctx.Reconnect(); err != nil {
				vctx.log.Error("voice: auto-reconnect failed", "err", err)
			}
		}(v)
	}
}

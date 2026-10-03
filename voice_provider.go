// Package sikasa: voice_provider.go
// Purpose: Bridges a pure-Go Opus packet source (WebM/Opus from YouTube or
// Ogg/Opus from a local file) into disgo's voice.OpusFrameProvider interface.
// The provider is owned by a VoiceCtx and replaced on every track.
//
// Key Components:
//   - streamProvider: implements voice.OpusFrameProvider for one track
//
// Dependencies:
//   - github.com/disgoorg/disgo/voice: SilenceAudioFrame, OpusFrameProvider
//
// Note: disgo's internal AudioSender pulls a frame every 20ms. While paused we
// return SilenceAudioFrame so the sender keeps ticking and Discord does not drop
// the speaking session. On EOF we return io.EOF and the AudioSender stops
// calling us; the next track swap re-arms a fresh provider.
package sikasa

import (
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/voice"
)

// streamProvider feeds opus frames from an opusSource into disgo's audio sender.
//
// Key Fields:
//   - src:    the container reader; closed in cleanup()
//   - paused: when true, ProvideOpusFrame returns silence
//   - done:   when true, ProvideOpusFrame returns io.EOF (streaming finished)
//   - closed: when true, the source has already been closed
//   - onDone: optional callback fired exactly once on natural EOF; suppressed
//     when Close() is what ended the stream (so swap-driven teardowns do not
//     chain into the next track)
//
// Note: done and closed are separate. Natural EOF sets done=true so the audio
// sender stops pulling frames, and also triggers cleanup so the source is
// released. Without separating these, Close() short-circuits on the second call
// and the body is never released.
type streamProvider struct {
	src         opusSource
	paused      atomic.Bool
	done        atomic.Bool
	closed      atomic.Bool
	natural     atomic.Bool
	onDone      func()
	logger      *slog.Logger
	frameCount  uint64
	logInterval time.Duration
	lastLogTime time.Time
}

// newStreamProvider wraps an opusSource in a streamProvider, ready to be passed
// to voice.Conn.SetOpusFrameProvider.
func newStreamProvider(src opusSource, logger *slog.Logger, logInterval time.Duration) *streamProvider {
	return &streamProvider{
		src:         src,
		logger:      logger,
		logInterval: logInterval,
		lastLogTime: time.Now(),
	}
}

/*
ProvideOpusFrame returns the next Opus frame, a silence frame while paused, or
io.EOF when the stream has ended or been closed.

	returns:
	      []byte: a single Opus packet (20ms at 48kHz stereo)
	      error:  io.EOF when finished, or any underlying reader error
*/
func (p *streamProvider) ProvideOpusFrame() ([]byte, error) {
	if p.done.Load() {
		return nil, io.EOF
	}
	if p.paused.Load() {
		return voice.SilenceAudioFrame, nil
	}

	p.frameCount++

	start := time.Now()
	frame, err := p.src.ReadPacket()
	elapsed := time.Since(start)

	// An underrun happens when frame retrieval takes longer than the 20ms window.
	if elapsed > 20*time.Millisecond && p.logger != nil {
		p.logger.Warn("audio underrun detected",
			"elapsed", elapsed.String(),
			"frame", p.frameCount,
		)
	}

	// Periodically log progress based on the configured log interval (default 5s).
	if p.logInterval > 0 && time.Since(p.lastLogTime) >= p.logInterval && p.logger != nil {
		p.logger.Debug("music stream status",
			"frame", p.frameCount,
		)
		p.lastLogTime = time.Now()
	}

	if errors.Is(err, io.EOF) {
		p.finishNatural()
		return nil, io.EOF
	}
	if err != nil {
		p.finishNatural()
		return nil, err
	}
	return frame, nil
}

// finishNatural marks the stream as finished due to upstream EOF or read error
// and fires onDone exactly once. Used by ProvideOpusFrame so the callback is
// only triggered for natural completion, not explicit Close.
func (p *streamProvider) finishNatural() {
	if !p.natural.CompareAndSwap(false, true) {
		return
	}
	p.done.Store(true)
	p.cleanup()
	if p.onDone != nil {
		go p.onDone()
	}
}

/*
Close marks the provider as done and releases the source. Safe to call more than
once; subsequent calls are no-ops.
*/
func (p *streamProvider) Close() {
	p.done.Store(true)
	p.cleanup()
}

// cleanup closes the source exactly once. Called on both natural EOF and
// explicit Close (Stop, swapProvider, Leave).
func (p *streamProvider) cleanup() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	if p.src != nil {
		_ = p.src.Close()
	}
}

// SetPaused toggles the paused flag. While paused, the provider returns
// SilenceAudioFrame instead of advancing the source.
func (p *streamProvider) SetPaused(v bool) { p.paused.Store(v) }

// IsPaused reports whether playback is currently paused.
func (p *streamProvider) IsPaused() bool { return p.paused.Load() }

// IsDone reports whether the stream has ended (either by EOF or by Close).
func (p *streamProvider) IsDone() bool { return p.done.Load() }

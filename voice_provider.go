// Package sikasa: voice_provider.go
// Purpose: Bridges the FFmpeg-driven Ogg-Opus pipeline (library-agnostic) into
// disgo's voice.OpusFrameProvider interface. The provider is owned by a
// VoiceCtx and replaced on every PlayFile / PlayYouTube call.
//
// Key Components:
//   - streamProvider: implements voice.OpusFrameProvider for one ffmpeg run
//
// Dependencies:
//   - github.com/disgoorg/disgo/voice: SilenceAudioFrame, OpusFrameProvider
//
// Note: disgo's internal AudioSender pulls a frame every 20ms. While paused
// we return SilenceAudioFrame so the sender keeps ticking and Discord does
// not drop the speaking session. On EOF we return io.EOF and the AudioSender
// stops calling us; the next PlayX swap re-arms a fresh provider.
package sikasa

import (
	"errors"
	"io"
	"sync/atomic"

	"github.com/disgoorg/disgo/voice"
)

// streamProvider feeds opus frames from an Ogg parser into disgo's audio sender.
//
// Key Fields:
//   - parser: pulls 20ms Opus frames from FFmpeg's stdout
//   - proc:   the FFmpeg subprocess; reaped in cleanup()
//   - paused: when true, ProvideOpusFrame returns silence
//   - done:   when true, ProvideOpusFrame returns io.EOF (streaming finished)
//   - closed: when true, the FFmpeg subprocess has already been killed/reaped
//
// Note: done and closed are separate. Natural EOF sets done=true so the audio
// sender stops pulling frames, and also triggers cleanup so FFmpeg does not
// linger. Without separating these, Close() short-circuits on the second call
// and FFmpeg is never reaped — visible as multiple ffmpeg processes after
// playing several tracks back to back.
type streamProvider struct {
	parser *oggPageParser
	proc   *ffmpegProcess
	paused atomic.Bool
	done   atomic.Bool
	closed atomic.Bool
}

// newStreamProvider wraps an ffmpegProcess in a streamProvider, ready to be
// passed to voice.Conn.SetOpusFrameProvider.
func newStreamProvider(proc *ffmpegProcess) *streamProvider {
	return &streamProvider{
		proc:   proc,
		parser: newOggParser(proc.Stdout()),
	}
}

/*
ProvideOpusFrame returns the next 20ms Opus frame, a silence frame while
paused, or io.EOF when the stream has ended or been closed.

	returns:
	      []byte: a single Opus packet (20ms at 48kHz stereo)
	      error:  io.EOF when finished, or any underlying parser error
*/
func (p *streamProvider) ProvideOpusFrame() ([]byte, error) {
	if p.done.Load() {
		return nil, io.EOF
	}
	if p.paused.Load() {
		return voice.SilenceAudioFrame, nil
	}
	frame, err := p.parser.NextFrame()
	if errors.Is(err, io.EOF) {
		p.done.Store(true)
		p.cleanup()
		return nil, io.EOF
	}
	if err != nil {
		p.done.Store(true)
		p.cleanup()
		return nil, err
	}
	return frame, nil
}

/*
Close marks the provider as done and tears down the FFmpeg subprocess. Safe
to call more than once; subsequent calls are no-ops.
*/
func (p *streamProvider) Close() {
	p.done.Store(true)
	p.cleanup()
}

// cleanup kills and reaps the FFmpeg subprocess exactly once. Called on both
// natural EOF (so processes do not linger after a track finishes) and explicit
// Close (Stop, swapProvider, Leave).
func (p *streamProvider) cleanup() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	if p.proc != nil {
		p.proc.Kill()
	}
}

// SetPaused toggles the paused flag. While paused, the provider returns
// SilenceAudioFrame instead of advancing the parser.
func (p *streamProvider) SetPaused(v bool) { p.paused.Store(v) }

// IsPaused reports whether playback is currently paused.
func (p *streamProvider) IsPaused() bool { return p.paused.Load() }

// IsDone reports whether the stream has ended (either by EOF or by Close).
func (p *streamProvider) IsDone() bool { return p.done.Load() }

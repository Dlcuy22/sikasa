// Package sikasa: voice.go
// Purpose: High-level API for joining Discord voice channels and playing
// audio sources (local files, YouTube URLs) on top of disgo's voice manager.
//
// Key Components:
//   - VoiceManager: factory accessed via bot.Voice(); holds per-guild contexts
//   - VoiceCtx:     handle returned by Join(); supports PlayFile, PlayYouTube,
//                   Pause, Resume, Stop, Leave
//   - PlaybackState: enum for the current state machine
//
// Dependencies:
//   - github.com/disgoorg/disgo/voice: Conn, OpusFrameProvider, SpeakingFlags
//   - voice_ffmpeg.go, voice_ogg.go, voice_youtube.go: pipeline pieces
//   - voice_provider.go:                bridges the pipeline to disgo
//
// Note: Pause/Resume now happen at the OpusFrameProvider layer. The provider
// returns voice.SilenceAudioFrame while paused, so disgo's AudioSender keeps
// ticking and Discord does not drop the speaking session.
package sikasa

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// PlaybackState describes the current state of a VoiceCtx.
type PlaybackState int32

const (
	StateIdle PlaybackState = iota
	StatePlaying
	StatePaused
	StateStopped
)

// VoiceManager is returned by Bot.Voice(). It is the entry point for joining
// voice channels and tracks active VoiceCtx values per guild.
type VoiceManager struct {
	bot *Bot
}

/*
Join connects to a voice channel and returns a VoiceCtx for playback control.
If the bot is already in a voice channel for the same guild, that connection
is moved to the new channel via UpdateVoiceState instead of opening a second
one.

	params:
	      guildID:   the guild snowflake the voice channel belongs to
	      channelID: the voice channel to join
	returns:
	      *VoiceCtx: a handle for playback control
	      error:     if the gateway voice handshake fails
*/
func (m *VoiceManager) Join(guildID, channelID string) (*VoiceCtx, error) {
	if m.bot.client == nil {
		return nil, ErrBotNotStarted
	}
	gid, err := snowflake.Parse(guildID)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrInvalidGuildID, guildID, err)
	}
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrInvalidChannelID, channelID, err)
	}

	log := m.bot.vlog().With("guild_id", gid.String(), "channel_id", cid.String())

	m.bot.voicesMu.Lock()
	if existing, ok := m.bot.voices[gid]; ok {
		m.bot.voicesMu.Unlock()
		// Already connected: move to the new channel via gateway voice state
		// update instead of stacking a second connection.
		log.Debug("voice: already connected, switching channel")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.bot.client.UpdateVoiceState(ctx, gid, &cid, false, false); err != nil {
			log.Error("voice: switch channel failed", "err", err)
			return nil, fmt.Errorf("sikasa: switch voice channel: %w", err)
		}
		return existing, nil
	}
	m.bot.voicesMu.Unlock()

	log.Debug("voice: creating connection")
	conn := m.bot.client.VoiceManager.CreateConn(gid)

	log.Debug("voice: opening connection (waits for VoiceServerUpdate)")
	openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOpen()
	if err := conn.Open(openCtx, cid, false, false); err != nil {
		log.Error("voice: open failed", "err", err)
		m.bot.client.VoiceManager.RemoveConn(gid)
		return nil, fmt.Errorf("sikasa: join voice: %w", err)
	}

	log.Debug("voice: connection open, setting speaking flag")
	speakCtx, cancelSpeak := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSpeak()
	if err := conn.SetSpeaking(speakCtx, voice.SpeakingFlagMicrophone); err != nil {
		log.Error("voice: set speaking failed", "err", err)
		conn.Close(context.Background())
		m.bot.client.VoiceManager.RemoveConn(gid)
		return nil, fmt.Errorf("sikasa: set speaking: %w", err)
	}

	vctx := &VoiceCtx{
		bot:     m.bot,
		conn:    conn,
		guildID: gid,
		log:     log,
	}
	vctx.state.Store(int32(StateIdle))

	m.bot.voicesMu.Lock()
	m.bot.voices[gid] = vctx
	m.bot.voicesMu.Unlock()
	log.Info("voice: joined")
	return vctx, nil
}

// Get returns the active VoiceCtx for a guild, or nil if the bot is not
// connected to any voice channel there.
func (m *VoiceManager) Get(guildID string) *VoiceCtx {
	gid, err := snowflake.Parse(guildID)
	if err != nil {
		return nil
	}
	m.bot.voicesMu.Lock()
	defer m.bot.voicesMu.Unlock()
	return m.bot.voices[gid]
}

// VoiceCtx is the per-guild voice handle. All playback control flows through it.
//
// Key Fields:
//   - conn:     the underlying disgo voice.Conn
//   - state:    atomic state (Idle / Playing / Paused / Stopped)
//   - provider: the active OpusFrameProvider; swapped on every PlayX call
//   - source:   human-readable description of what is currently loaded
//
// Note: Methods are safe to call concurrently. The provider field is guarded
// by the mutex; state is atomic so quick reads (IsPlaying, State) are lock-free.
type VoiceCtx struct {
	bot     *Bot
	conn    voice.Conn
	guildID snowflake.ID
	log     *slog.Logger

	mu       sync.Mutex
	source   string
	state    atomic.Int32
	provider *streamProvider
}

// Bot returns the parent *Bot. Mainly for symmetry with CmdCtx / MsgCtx.
func (v *VoiceCtx) Bot() *Bot { return v.bot }

// Disgo returns the underlying voice.Conn as an escape hatch for advanced
// operations (custom OpusFrameReceiver, raw UDP writes, etc).
func (v *VoiceCtx) Disgo() voice.Conn { return v.conn }

// State returns the current playback state.
func (v *VoiceCtx) State() PlaybackState {
	return PlaybackState(v.state.Load())
}

// IsPlaying reports whether audio is actively being streamed.
func (v *VoiceCtx) IsPlaying() bool {
	return v.State() == StatePlaying
}

// Source returns a human-readable description of the current source
// (file path or URL); empty if nothing has been played yet.
func (v *VoiceCtx) Source() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.source
}

/*
PlayFile streams a local audio file. The codec is detected from the file
extension: .opus and .ogg use stream-copy passthrough (no re-encoding);
anything else (mp3, wav, flac, aac, m4a, ...) goes through FFmpeg's libopus
encoder.

	params:
	      path: filesystem path to the audio file
	returns:
	      error: if the file cannot be opened by FFmpeg
*/
func (v *VoiceCtx) PlayFile(path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	var (
		proc *ffmpegProcess
		err  error
	)
	switch ext {
	case ".opus", ".ogg":
		proc, err = spawnPassthrough(path)
	default:
		proc, err = spawnTranscode(path)
	}
	if err != nil {
		return err
	}
	v.swapProvider(proc, path)
	return nil
}

/*
PlayYouTube fetches an audio stream via yt-dlp and pipes it into FFmpeg for
Opus encoding. yt-dlp must be installed and on PATH.

	params:
	      url: any URL yt-dlp can resolve
	returns:
	      error: if the pipeline cannot be started
*/
func (v *VoiceCtx) PlayYouTube(url string) error {
	proc, err := spawnYouTube(url)
	if err != nil {
		return err
	}
	v.swapProvider(proc, url)
	return nil
}

/*
Pause stops sending audio frames. The FFmpeg process and parser stay alive,
so Resume() picks up exactly where playback left off.

	returns:
	      error: if no audio is currently playing
*/
func (v *VoiceCtx) Pause() error {
	v.mu.Lock()
	p := v.provider
	v.mu.Unlock()
	if p == nil || p.IsDone() {
		return ErrNoAudio
	}
	p.SetPaused(true)
	v.state.Store(int32(StatePaused))
	return nil
}

/*
Resume continues a paused stream.

	returns:
	      error: if the state is not Paused
*/
func (v *VoiceCtx) Resume() error {
	v.mu.Lock()
	p := v.provider
	v.mu.Unlock()
	if p == nil || !p.IsPaused() {
		return ErrNotPaused
	}
	p.SetPaused(false)
	v.state.Store(int32(StatePlaying))
	return nil
}

/*
Stop halts playback and tears down the FFmpeg pipeline. The voice connection
itself stays open so the next Play* call can reuse it.

	returns:
	      error: always nil currently; reserved for future error paths
*/
func (v *VoiceCtx) Stop() error {
	v.mu.Lock()
	p := v.provider
	v.provider = nil
	v.source = ""
	v.mu.Unlock()
	if p != nil {
		p.Close()
	}
	v.state.Store(int32(StateStopped))
	return nil
}

/*
Leave closes the voice connection and removes this context from the bot's
registry. After calling Leave, this VoiceCtx must not be reused.

	returns:
	      error: always nil currently; reserved for future error paths
*/
func (v *VoiceCtx) Leave() error {
	_ = v.Stop()

	if v.conn != nil {
		v.conn.Close(context.TODO())
	}
	if v.bot.client != nil {
		v.bot.client.VoiceManager.RemoveConn(v.guildID)
	}

	v.bot.voicesMu.Lock()
	delete(v.bot.voices, v.guildID)
	v.bot.voicesMu.Unlock()
	return nil
}

// swapProvider replaces the active OpusFrameProvider with a fresh one wrapping
// the given FFmpeg process. The old provider is closed so its FFmpeg child is
// reaped before the new one starts producing frames.
func (v *VoiceCtx) swapProvider(proc *ffmpegProcess, source string) {
	newProv := newStreamProvider(proc)

	v.mu.Lock()
	old := v.provider
	v.provider = newProv
	v.source = source
	v.mu.Unlock()

	if old != nil {
		old.Close()
	}
	v.conn.SetOpusFrameProvider(newProv)
	v.state.Store(int32(StatePlaying))
}

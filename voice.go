// Package sikasa: voice.go
// Purpose: High-level API for joining Discord voice channels and playing
// audio sources (local files, YouTube URLs) on top of disgo's voice manager.
//
// Key Components:
//   - VoiceManager: factory accessed via bot.Voice(); holds per-guild contexts
//   - VoiceCtx:     handle returned by Join(); supports PlayFile, PlayYouTube,
//     Pause, Resume, Stop, Leave
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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// RemuxMode describes the method used to remux/convert YouTube stream to Ogg-Opus.
type RemuxMode string

const (
	// RemuxFFmpeg executes external ffmpeg processes.
	// Deprecated: Use RemuxNativeGo instead. This mode is kept for compatibility
	// but may be removed in a future release.
	RemuxFFmpeg RemuxMode = "ffmpeg"
	// RemuxNative runs the native library remuxer using purego.
	// Deprecated: Use RemuxNativeGo instead. This mode is kept for compatibility
	// but may be removed in a future release.
	RemuxNative RemuxMode = "native"
	// RemuxNativeGo runs the pure-Go remuxer using go-mkvparse + mccoy.space/g/ogg.
	// This is the recommended and default remux mode.
	RemuxNativeGo RemuxMode = "native-go"
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
		bot:            m.bot,
		conn:           conn,
		guildID:        gid,
		log:            log,
		queue:          newQueue(),
		remuxMode:      m.bot.remuxMode,
		jsRuntimeName:  m.bot.jsRuntimeName,
		jsRuntimePath:  m.bot.jsRuntimePath,
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
//   - conn:               the underlying disgo voice.Conn
//   - state:              atomic state (Idle / Playing / Paused / Stopped)
//   - provider:           the active OpusFrameProvider; swapped on track change
//   - source:             human-readable description of what is currently loaded
//   - queue:              per-guild track list and cursor; auto-advances on EOF
//   - announceChannelID:  optional channel where auto-advance announcements go;
//     zero means announcements are disabled
//
// Note: Methods are safe to call concurrently. The provider, source, queue,
// and announceChannelID fields are guarded by the mutex; state is atomic so
// quick reads (IsPlaying, State) are lock-free. Each guild gets its own
// VoiceCtx via Bot.voices, so queues are naturally isolated per session.
type VoiceCtx struct {
	bot     *Bot
	conn    voice.Conn
	guildID snowflake.ID
	log     *slog.Logger

	mu                sync.Mutex
	source            string
	state             atomic.Int32
	provider          *streamProvider
	queue             *queue
	announceChannelID snowflake.ID
	remuxMode         RemuxMode
	jsRuntimeName    string
	jsRuntimePath    string
}

// Bot returns the parent *Bot. Mainly for symmetry with CmdCtx / MsgCtx.
func (v *VoiceCtx) Bot() *Bot { return v.bot }

/*
SetAnnounceChannel routes auto-advance announcements ("next track: ...",
"queue ended") to the given Discord channel ID. Pass "" to disable. Manual
Skip/Prev/Jump do not announce here, since the prefix handler that triggered
them already replies in-place.

	params:
	      channelID: snowflake of the text channel to post to, or "" to disable
	returns:
	      *VoiceCtx: receiver, for chaining
*/
/*
WithRemuxMode configures the remuxing strategy for this voice connection.
Accepted values: "ffmpeg" (deprecated), "native" (deprecated), or "native-go" (default).

    params:
          mode: the remuxing mode
    returns:
          *VoiceCtx: receiver, for chaining
*/
func (v *VoiceCtx) WithRemuxMode(mode string) *VoiceCtx {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch RemuxMode(mode) {
	case RemuxFFmpeg:
		v.remuxMode = RemuxFFmpeg
	case RemuxNative:
		v.remuxMode = RemuxNative
	case RemuxNativeGo:
		v.remuxMode = RemuxNativeGo
	default:
		v.remuxMode = RemuxNativeGo
	}
	return v
}

/*
WithJSRuntime selects a JavaScript runtime for yt-dlp's signature decryption
on this specific voice connection. See Bot.WithJSRuntime for accepted values.

    params:
          name: runtime name ("bun", "deno", "quickjs", or "" to disable)
          path: optional absolute path to the runtime binary
    returns:
          *VoiceCtx: receiver, for chaining
*/
func (v *VoiceCtx) WithJSRuntime(name string, path ...string) *VoiceCtx {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.jsRuntimeName = name
	if len(path) > 0 {
		v.jsRuntimePath = path[0]
	} else {
		v.jsRuntimePath = ""
	}
	return v
}

func (v *VoiceCtx) SetAnnounceChannel(channelID string) *VoiceCtx {
	if channelID == "" {
		v.mu.Lock()
		v.announceChannelID = 0
		v.mu.Unlock()
		return v
	}
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		v.log.Debug("voice: ignoring invalid announce channel", "channel_id", channelID, "err", err)
		return v
	}
	v.mu.Lock()
	v.announceChannelID = cid
	v.mu.Unlock()
	return v
}

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
PlayFile enqueues a local audio file. If nothing is currently playing the
track starts immediately; otherwise it lands at the tail of the queue and
auto-plays when the current track ends. Codec detection (.opus / .ogg
passthrough vs. transcode) happens at spawn time, not enqueue time.

	params:
	      path: filesystem path to the audio file
	returns:
	      position: 0-based index of the track in the queue
	      started:  true if this call began playback (queue was idle)
	      error:    only if the queue manipulation fails (currently never)
*/
func (v *VoiceCtx) PlayFile(path string) (position int, started bool, err error) {
	return v.Enqueue(Track{Kind: TrackFile, Source: path})
}

/*
PlayYouTube enqueues a yt-dlp-resolvable URL. Same enqueue-or-play semantics
as PlayFile, with one extra wrinkle: playlist URLs are expanded into N tracks
via yt-dlp --flat-playlist, so a single PlayYouTube call can append many
queue entries at once. Probe failure is non-fatal; on error the raw URL is
enqueued as a single track and the caller will see it as the bare link.

	params:
	      url: any URL yt-dlp can resolve (single video, playlist, channel, ...)
	returns:
	      firstPos: 0-based queue index of the first track added
	      added:    number of tracks appended (1 for a single video, N for
	                a playlist)
	      started:  true if this call began playback (queue was idle and the
	                first new track is now playing)
	      error:    only if the queue manipulation fails (currently never)
*/
func (v *VoiceCtx) PlayYouTube(url string) (firstPos, added int, started bool, err error) {
	if v.bot.cacheEnabled {
		cachePath := v.bot.getCachePath(url)
		if _, err := os.Stat(cachePath); err == nil {
			v.log.Info("voice: play youtube found in local cache", "url", url, "path", cachePath)
			pos, started, err := v.Enqueue(Track{Kind: TrackYouTube, Source: url})
			return pos, 1, started, err
		}
	}

	tracks, perr := probeYouTubeEntries(url)
	if perr != nil || len(tracks) == 0 {
		// Probe failed or returned nothing. Fall back to enqueuing the raw
		// URL so the user still gets playback, just without a pretty label.
		pos, started, err := v.Enqueue(Track{Kind: TrackYouTube, Source: url})
		return pos, 1, started, err
	}

	v.mu.Lock()
	firstPos = v.queue.Len()
	for _, t := range tracks {
		v.queue.Add(t)
	}
	idle := v.provider == nil || v.provider.IsDone()
	v.mu.Unlock()

	if !idle {
		v.triggerPrefetch()
		return firstPos, len(tracks), false, nil
	}
	if err := v.advanceAndPlay(); err != nil {
		return firstPos, len(tracks), false, err
	}
	v.triggerPrefetch()
	return firstPos, len(tracks), true, nil
}

/*
Enqueue adds a Track to the queue. If nothing is currently playing the queue
is advanced to this track and playback begins. Otherwise the track simply
waits its turn.

	params:
	      t: the track to enqueue
	returns:
	      position: 0-based index of the new track in the queue
	      started:  true if this call kicked off playback
	      error:    spawn error if started==true and the pipeline fails
*/
func (v *VoiceCtx) Enqueue(t Track) (position int, started bool, err error) {
	v.mu.Lock()
	pos := v.queue.Add(t)
	idle := v.provider == nil || v.provider.IsDone()
	v.mu.Unlock()

	if !idle {
		v.triggerPrefetch()
		return pos, false, nil
	}
	if err := v.advanceAndPlay(); err != nil {
		return pos, false, err
	}
	v.triggerPrefetch()
	return pos, true, nil
}

/*
InsertNext inserts a track immediately after the currently playing one,
shifting any later tracks down by one. When nothing is playing yet, the
track lands at index 0 and playback starts. Useful for "play next" UX
without disturbing what is currently audible.

	params:
	      t: track to insert
	returns:
	      position: 0-based index of the newly inserted track
	      started:  true if this call kicked off playback (queue was idle)
	      error:    spawn error when started==true and the pipeline fails
*/
func (v *VoiceCtx) InsertNext(t Track) (position int, started bool, err error) {
	v.mu.Lock()
	idle := v.provider == nil || v.provider.IsDone()
	cursor := v.queue.Cursor()
	pos := v.queue.InsertAfter(cursor, t)
	v.mu.Unlock()

	if !idle {
		v.triggerPrefetch()
		return pos, false, nil
	}
	if err := v.advanceAndPlay(); err != nil {
		return pos, false, err
	}
	v.triggerPrefetch()
	return pos, true, nil
}

/*
InsertNextYouTube probes a yt-dlp-resolvable URL (single video, playlist,
channel) and inserts every resulting track immediately after the currently
playing one in playlist order. Mirror of PlayYouTube's expand-then-enqueue
flow but inserts instead of appending. Probe failure falls back to
inserting the raw URL as a single track.

	params:
	      url: any URL yt-dlp can resolve
	returns:
	      firstPos: index of the first inserted track
	      added:    number of tracks inserted (>=1)
	      started:  true if the call kicked off playback (queue was idle)
	      error:    spawn error when started==true and the pipeline fails
*/
func (v *VoiceCtx) InsertNextYouTube(url string) (firstPos, added int, started bool, err error) {
	if v.bot.cacheEnabled {
		cachePath := v.bot.getCachePath(url)
		if _, err := os.Stat(cachePath); err == nil {
			v.log.Info("voice: insert next youtube found in local cache", "url", url, "path", cachePath)
			pos, started, err := v.InsertNext(Track{Kind: TrackYouTube, Source: url})
			return pos, 1, started, err
		}
	}

	tracks, perr := probeYouTubeEntries(url)
	if perr != nil || len(tracks) == 0 {
		pos, started, err := v.InsertNext(Track{Kind: TrackYouTube, Source: url})
		return pos, 1, started, err
	}

	v.mu.Lock()
	idle := v.provider == nil || v.provider.IsDone()
	cursor := v.queue.Cursor()
	first := v.queue.InsertBatchAfter(cursor, tracks)
	v.mu.Unlock()

	if !idle {
		v.triggerPrefetch()
		return first, len(tracks), false, nil
	}
	if err := v.advanceAndPlay(); err != nil {
		return first, len(tracks), false, err
	}
	v.triggerPrefetch()
	return first, len(tracks), true, nil
}

/*
Skip stops the current track and plays the next one in the queue. If the
queue has no further tracks the connection stays open but playback ends.

	returns:
	      Track: the track that started playing (zero value if queue exhausted)
	      bool:  true if a new track started, false if the queue is exhausted
	      error: spawn error if the next track fails to start
*/
func (v *VoiceCtx) Skip() (Track, bool, error) {
	v.mu.Lock()
	hasNext := v.queue.HasNext()
	v.mu.Unlock()
	if !hasNext {
		_ = v.Stop()
		return Track{}, false, nil
	}
	if err := v.advanceAndPlay(); err != nil {
		return Track{}, false, err
	}
	now, _ := v.Now()
	return now, true, nil
}

// Next is an alias for Skip provided for callers that prefer the queue verb.
func (v *VoiceCtx) Next() (Track, bool, error) { return v.Skip() }

/*
JumpTo moves the queue cursor to a specific 0-based index and plays that
track. Out-of-range indices return ErrQueueEmpty without changing state.

	params:
	      index: 0-based queue position to jump to
	returns:
	      Track: the track that started playing
	      error: ErrQueueEmpty if index is out of range, or a spawn error
*/
func (v *VoiceCtx) JumpTo(index int) (Track, error) {
	v.mu.Lock()
	t, ok := v.queue.Jump(index)
	v.mu.Unlock()
	if !ok {
		return Track{}, ErrQueueEmpty
	}
	if err := v.playLoaded(t); err != nil {
		return Track{}, err
	}
	return t, nil
}

/*
Prev rewinds the queue cursor by one and plays that track. If the queue has
been exhausted (cursor sits past the last track because the last track
finished naturally), Prev jumps back to the last track instead of going all
the way to the first. Returns ErrNoPrevious only when the queue is empty or
the cursor is genuinely at index 0.

	returns:
	      Track: the track that started playing
	      error: ErrNoPrevious or a spawn error
*/
func (v *VoiceCtx) Prev() (Track, error) {
	v.mu.Lock()
	exhausted := v.provider == nil || v.provider.IsDone()
	last := v.queue.Len() - 1
	cursor := v.queue.Cursor()
	var (
		t  Track
		ok bool
	)
	switch {
	case exhausted && last >= 0:
		// Queue ran out (or never started). "Previous" intuitively means the
		// last track that played, not index 0. Land cursor on the tail.
		t, ok = v.queue.Jump(last)
	case cursor <= 0:
		ok = false
	default:
		t, ok = v.queue.Rewind()
	}
	v.mu.Unlock()
	if !ok {
		return Track{}, ErrNoPrevious
	}
	if err := v.playLoaded(t); err != nil {
		return Track{}, err
	}
	return t, nil
}

// Queue returns a snapshot of the queued tracks. The current track (if any)
// is included at index Cursor().
func (v *VoiceCtx) Queue() []Track {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.queue.Tracks()
}

// Cursor returns the index of the currently playing track, or -1 if nothing
// has started yet.
func (v *VoiceCtx) Cursor() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.queue.Cursor()
}

// Now returns the currently loaded track and true, or a zero Track and false
// if the queue has not started yet.
func (v *VoiceCtx) Now() (Track, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.queue.Now()
}

/*
ClearQueue empties the queue without affecting the currently playing track.
Combine with Stop() to halt playback and reset state in one shot.
*/
func (v *VoiceCtx) ClearQueue() {
	v.mu.Lock()
	v.queue.Clear()
	v.mu.Unlock()
	v.triggerPrefetch()
}

/*
Shuffle randomizes the order of queued tracks after the currently playing one.
Returns ErrQueueEmpty if there are no tracks waiting in the queue.
*/
func (v *VoiceCtx) Shuffle() error {
	v.mu.Lock()
	if v.queue.Len() == 0 || v.queue.Cursor()+2 >= v.queue.Len() {
		v.mu.Unlock()
		return ErrQueueEmpty
	}
	v.queue.Shuffle()
	v.mu.Unlock()
	v.triggerPrefetch()
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
itself stays open so the next Play* call can reuse it. The queue is left
intact; call ClearQueue() too if you want a fresh session.

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
	v.mu.Lock()
	v.queue.Clear()
	v.mu.Unlock()

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

/*
Reconnect tears down the current voice connection and reopens one to the
same channel, then resumes playback from the current track. Used to recover
from DAVE epoch desync ("no active epoch" errors) where the connection
remains physically open but encryption fails on every packet. The queue and
cursor are preserved; only the audio handshake gets a fresh start.

	returns:
	      error: if the channel cannot be re-resolved or the new handshake fails

Note: Resume from mid-track is not supported. The current track restarts
from the beginning because FFmpeg has been torn down with the connection.
*/
func (v *VoiceCtx) Reconnect() error {
	if v.bot == nil || v.bot.client == nil {
		return ErrBotNotStarted
	}

	v.mu.Lock()
	announceCh := v.announceChannelID
	current, hadTrack := v.queue.Now()
	v.mu.Unlock()

	channelID := v.conn.ChannelID()
	if channelID == nil {
		return ErrNotInVoice
	}
	cid := *channelID

	v.log.Info("voice: reconnecting", "channel_id", cid.String())
	v.announce(announceCh, "voice connection lost, reconnecting…")

	_ = v.Stop()
	v.conn.Close(context.TODO())
	v.bot.client.VoiceManager.RemoveConn(v.guildID)

	conn := v.bot.client.VoiceManager.CreateConn(v.guildID)
	openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOpen()
	if err := conn.Open(openCtx, cid, false, false); err != nil {
		v.bot.client.VoiceManager.RemoveConn(v.guildID)
		return fmt.Errorf("sikasa: reconnect open: %w", err)
	}
	speakCtx, cancelSpeak := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSpeak()
	if err := conn.SetSpeaking(speakCtx, voice.SpeakingFlagMicrophone); err != nil {
		conn.Close(context.Background())
		v.bot.client.VoiceManager.RemoveConn(v.guildID)
		return fmt.Errorf("sikasa: reconnect set speaking: %w", err)
	}

	v.mu.Lock()
	v.conn = conn
	v.mu.Unlock()

	if !hadTrack {
		return nil
	}
	if err := v.playLoaded(current); err != nil {
		return fmt.Errorf("sikasa: reconnect resume: %w", err)
	}
	v.announce(announceCh, "reconnected, resuming: "+current.Label())
	return nil
}

// advanceAndPlay moves the queue cursor forward and starts the resulting
// track. Returns nil if the queue is exhausted (caller treats this as a
// graceful end of playback).
func (v *VoiceCtx) advanceAndPlay() error {
	v.mu.Lock()
	t, ok := v.queue.Advance()
	v.mu.Unlock()
	if !ok {
		v.state.Store(int32(StateStopped))
		return nil
	}
	return v.playLoaded(t)
}

// playLoaded spawns the appropriate pipeline for t and swaps it into the
// provider slot. Used by Enqueue (first track), Skip (manual advance), Prev
// (rewind), and the auto-advance callback.
func (v *VoiceCtx) playLoaded(t Track) error {
	proc, err := v.spawnTrack(t)
	if err != nil {
		return err
	}
	v.swapProvider(proc, t.Label())
	v.triggerPrefetch()
	return nil
}

// spawnTrack picks the right ffmpeg/yt-dlp recipe for a Track. Local files
// hit the codec switch (passthrough for opus/ogg, transcode otherwise);
// YouTube tracks go through local cache if available, else standard stream.
func (v *VoiceCtx) spawnTrack(t Track) (*ffmpegProcess, error) {
	switch t.Kind {
	case TrackYouTube:
		if v.bot.cacheEnabled {
			cachePath := v.bot.getCachePath(t.Source)
			if _, err := os.Stat(cachePath); err == nil {
				v.log.Info("voice: playing from local cache", "url", t.Source, "path", cachePath)
				return spawnPassthrough(cachePath)
			}
		}
		return spawnYouTube(t.Source, v.remuxMode, v.jsRuntimeName, v.jsRuntimePath)
	case TrackFile:
		ext := strings.ToLower(filepath.Ext(t.Source))
		switch ext {
		case ".opus", ".ogg":
			return spawnPassthrough(t.Source)
		default:
			return spawnTranscode(t.Source)
		}
	default:
		return nil, ErrInvalidArg
	}
}

// swapProvider replaces the active OpusFrameProvider with a fresh one wrapping
// the given FFmpeg process. The old provider is closed so its FFmpeg child is
// reaped before the new one starts producing frames. The new provider's
// onDone callback hooks into auto-advance so the queue moves forward when a
// track ends naturally.
func (v *VoiceCtx) swapProvider(proc *ffmpegProcess, source string) {
	newProv := newStreamProvider(proc, v.log, v.bot.musicLogInterval)
	newProv.onDone = v.onTrackDone

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

// onTrackDone is invoked by the provider's natural-EOF path. It tries to
// advance the queue; if that fails (queue exhausted), state lands on Stopped.
// When an announcement channel is configured, posts "next track: ..." for
// successful advances and "no more tracks" when the queue is exhausted.
// Errors are logged because there is no caller to surface them to.
func (v *VoiceCtx) onTrackDone() {
	v.mu.Lock()
	hasNext := v.queue.HasNext()
	announceCh := v.announceChannelID
	v.mu.Unlock()

	if !hasNext {
		v.state.Store(int32(StateStopped))
		v.announce(announceCh, "no more tracks")
		return
	}
	if err := v.advanceAndPlay(); err != nil {
		v.log.Error("voice: auto-advance failed", "err", err)
		v.announce(announceCh, "auto-advance failed: "+err.Error())
		return
	}
	if t, ok := v.Now(); ok {
		v.announce(announceCh, "next track: "+t.Label())
	}
}

// announce posts a one-line message to the configured announcement channel.
// No-ops when channelID is zero or the bot client has been torn down. Errors
// are logged because announcements are best-effort.
func (v *VoiceCtx) announce(channelID snowflake.ID, text string) {
	if channelID == 0 || v.bot == nil || v.bot.client == nil {
		return
	}
	_, err := v.bot.client.Rest.CreateMessage(channelID, discord.NewMessageCreate().WithContent(text))
	if err != nil {
		v.log.Debug("voice: announce failed", "channel_id", channelID.String(), "err", err)
	}
}

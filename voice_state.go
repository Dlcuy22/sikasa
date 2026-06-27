// Package sikasa: voice_state.go
// Purpose: Handles persistent saving and restoring of voice playback state
// to survive bot restarts and network reconnects.
//
// Key Components:
//   - PersistedState: Struct serialized to JSON representing the queue and playback state
//   - Persist(): Saves the current VoiceCtx state to disk
//   - deleteState(): Removes the saved state file when leaving a channel
//   - recoveryWorker(): Periodically checks and recovers lost voice connections
//   - runRecovery(): Reads disk state files and resumes playback or triggers reconnects
//
// Dependencies:
//   - encoding/json: Marshalling state to JSON format
//   - os: File and directory operations
//   - path/filepath: Building file paths
//
package sikasa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// PersistedState represents the saved state of a guild voice playback session.
type PersistedState struct {
	GuildID           string  `json:"guild_id"`
	ChannelID         string  `json:"channel_id"`
	AnnounceChannelID string  `json:"announce_channel_id,omitempty"`
	State             int32   `json:"state"`
	Cursor            int     `json:"cursor"`
	Tracks            []Track `json:"tracks"`
}

// stateDir returns the directory path where state files are saved.
func (b *Bot) stateDir() string {
	return filepath.Join(filepath.Dir(b.cacheDir), "state")
}

// statePath returns the path to the state JSON file for the given guild.
func (b *Bot) statePath(guildID snowflake.ID) string {
	return filepath.Join(b.stateDir(), guildID.String()+".json")
}

// Persist saves the current VoiceCtx playback state and queue to disk.
func (v *VoiceCtx) Persist() {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.channelID == 0 {
		return
	}

	state := PersistedState{
		GuildID:           v.guildID.String(),
		ChannelID:         v.channelID.String(),
		AnnounceChannelID: v.announceChannelID.String(),
		State:             v.state.Load(),
		Cursor:            v.queue.Cursor(),
		Tracks:            v.queue.Tracks(),
	}

	dir := v.bot.stateDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		v.log.Error("failed to create state directory", "err", err)
		return
	}

	path := v.bot.statePath(v.guildID)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		v.log.Error("failed to marshal state", "err", err)
		return
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		v.log.Error("failed to write temp state file", "err", err)
		return
	}

	if err := os.Rename(tmpPath, path); err != nil {
		v.log.Error("failed to commit state file", "err", err)
		_ = os.Remove(tmpPath)
	}
}

// deleteState removes the state file for this VoiceCtx.
func (v *VoiceCtx) deleteState() {
	path := v.bot.statePath(v.guildID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		v.log.Error("failed to delete state file", "err", err)
	}
}

// recoveryWorker runs a periodic loop checking health of voice connections.
func (b *Bot) recoveryWorker() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-b.recoveryCtx.Done():
			return
		case <-ticker.C:
			b.runRecovery()
		}
	}
}

// runRecovery scans the state directory and attempts to reconnect/rejoin guilds.
func (b *Bot) runRecovery() {
	dir := b.stateDir()
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		b.vlog().Error("recovery: failed to read state directory", "err", err)
		return
	}

	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, f.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			b.vlog().Error("recovery: failed to read state file", "path", path, "err", err)
			continue
		}

		var state PersistedState
		if err := json.Unmarshal(data, &state); err != nil {
			b.vlog().Error("recovery: failed to unmarshal state JSON", "path", path, "err", err)
			_ = os.Remove(path)
			continue
		}

		if state.State != int32(StatePlaying) && state.State != int32(StatePaused) {
			continue
		}

		guildID, err := snowflake.Parse(state.GuildID)
		if err != nil {
			b.vlog().Error("recovery: invalid guild ID in state", "guild_id", state.GuildID, "err", err)
			_ = os.Remove(path)
			continue
		}

		b.voicesMu.Lock()
		vctx, exists := b.voices[guildID]
		b.voicesMu.Unlock()

		if !exists {
			b.vlog().Info("recovery: restoring voice session", "guild_id", state.GuildID, "channel_id", state.ChannelID)
			
			go func(state PersistedState) {
				vctx, err := b.Voice().Join(state.GuildID, state.ChannelID)
				if err != nil {
					b.vlog().Error("recovery: failed to join channel", "guild_id", state.GuildID, "err", err)
					return
				}

				vctx.mu.Lock()
				if state.AnnounceChannelID != "" {
					if cid, err := snowflake.Parse(state.AnnounceChannelID); err == nil {
						vctx.announceChannelID = cid
					}
				}
				vctx.queue.tracks = state.Tracks
				vctx.queue.cursor = state.Cursor
				vctx.mu.Unlock()

				current, ok := vctx.Now()
				if !ok {
					b.vlog().Error("recovery: queue is empty but state was active", "guild_id", state.GuildID)
					vctx.deleteState()
					return
				}

				if err := vctx.playLoaded(current); err != nil {
					vctx.log.Error("recovery: failed to play track", "err", err)
				} else {
					if state.State == int32(StatePaused) {
						_ = vctx.Pause()
					} else {
						vctx.Persist()
					}
				}
			}(state)
		} else {
			status := voice.StatusUnconnected
			if vctx.conn != nil && vctx.conn.Gateway() != nil {
				status = vctx.conn.Gateway().Status()
			}

			if status != voice.StatusReady {
				if vctx.isReconnecting.CompareAndSwap(false, true) {
					b.vlog().Info("recovery: connection unhealthy, triggering reconnect", "guild_id", state.GuildID, "status", int(status))
					go func(v *VoiceCtx) {
						defer v.isReconnecting.Store(false)
						if err := v.Reconnect(); err != nil {
							v.log.Error("recovery: auto-reconnect failed", "err", err)
						}
					}(vctx)
				}
			}
		}
	}
}

// Package sikasa: voice_check.go
// Purpose: Shared "is the user allowed to control this voice session" guard
// used by prefix commands tagged with .RequireSameVoice() and by anyone who
// needs the same check from a slash handler.
//
// Key Components:
//   - Bot.checkSameVoice():  inspects the cached voice states for the guild
//                             and returns ErrNotSameChannel when the invoker
//                             is not co-located with the bot
//
// Note: When the bot is not connected to any voice channel in the guild the
// check passes. This lets initial-join commands like "play" still work; the
// command itself decides whether to join. Use this helper only after the
// caller has already checked for a guild context (DMs have no voice state).
package sikasa

import (
	"github.com/disgoorg/disgo/events"
)

// checkSameVoice returns nil when the message author is allowed to control
// the bot's current voice session. Reasons it returns nil:
//   - the bot is not currently in a voice channel for this guild (anyone may
//     issue commands that may join later)
//   - the author is in the same voice channel as the bot
//
// Returns ErrNotSameChannel otherwise. Returns nil for DMs (no guild) so the
// caller's existing GuildID()=="" guard in handlers stays in charge.
func (b *Bot) checkSameVoice(e *events.MessageCreate) error {
	if b.client == nil || e.GuildID == nil {
		return nil
	}
	gid := *e.GuildID

	// Bot side: voice manager is the source of truth. Caches.VoiceState may
	// not have a row for the bot until the next gateway tick, but the voice
	// connection itself is created synchronously in Join().
	conn := b.client.VoiceManager.GetConn(gid)
	if conn == nil {
		return nil
	}
	botChannelID := conn.ChannelID()
	if botChannelID == nil {
		return nil
	}

	userState, ok := b.client.Caches.VoiceState(gid, e.Message.Author.ID)
	if !ok || userState.ChannelID == nil || *userState.ChannelID != *botChannelID {
		return ErrNotSameChannel
	}
	return nil
}

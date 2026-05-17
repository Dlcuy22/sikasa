// Package sikasa: cmdctx.go
// Purpose: Defines CmdCtx, the per-invocation context passed to slash command
// handlers. Bundles the disgo handler.CommandEvent and SlashCommandInteractionData
// with reply helpers for text, embeds, and media so handlers stay one-liners.
//
// Key Components:
//   - CmdCtx:       per-invocation handler context
//   - newCmdCtx():  internal constructor; called by command.go's router glue
//   - Reply / ReplyEmbed / ReplyFile / ReplyURL: response helpers
//   - String / Int / Bool / User / Channel / Attachment: option accessors
//   - Defer / Followup: long-running command pattern
//
// Dependencies:
//   - github.com/disgoorg/disgo/discord:  message, embed, and option types
//   - github.com/disgoorg/disgo/handler:  CommandEvent, the response surface
//
// Note: Reply must be called within 3 seconds of the interaction firing;
// otherwise call Defer() first and finish with Followup().
package sikasa

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
)

// CmdCtx is the context passed to a slash command handler.
//
// Key Fields:
//   - bot:   the parent Bot, exposed via Bot()
//   - event: the disgo CommandEvent, exposed via Event()
//   - data:  the parsed slash command interaction data
type CmdCtx struct {
	bot   *Bot
	event *handler.CommandEvent
	data  discord.SlashCommandInteractionData
}

// newCmdCtx wires a CmdCtx for the given event/data pair.
func newCmdCtx(b *Bot, e *handler.CommandEvent, data discord.SlashCommandInteractionData) *CmdCtx {
	return &CmdCtx{bot: b, event: e, data: data}
}

// Bot returns the parent Bot.
func (c *CmdCtx) Bot() *Bot { return c.bot }

// Event returns the underlying disgo *handler.CommandEvent as an escape hatch
// for advanced response patterns (UpdateInteractionResponse, file followups,
// etc).
func (c *CmdCtx) Event() *handler.CommandEvent { return c.event }

// Data returns the parsed slash command interaction data, useful when the
// option helpers below are insufficient (e.g. iterating Resolved entities).
func (c *CmdCtx) Data() discord.SlashCommandInteractionData { return c.data }

// Author returns the user who invoked the command, regardless of whether
// the invocation happened in a guild or in DMs.
func (c *CmdCtx) Author() discord.User {
	return c.event.User()
}

// ChannelID returns the channel where the command was invoked.
func (c *CmdCtx) ChannelID() string {
	return c.event.Channel().ID().String()
}

// GuildID returns the guild snowflake as a string, or empty for DM invocations.
func (c *CmdCtx) GuildID() string {
	if id := c.event.GuildID(); id != nil {
		return id.String()
	}
	return ""
}

// String returns the string value for the named option, or "" if missing.
func (c *CmdCtx) String(name string) string { return c.data.String(name) }

// Int returns the integer value for the named option, or 0 if missing.
func (c *CmdCtx) Int(name string) int64 { return int64(c.data.Int(name)) }

// Bool returns the boolean value for the named option, or false if missing.
func (c *CmdCtx) Bool(name string) bool { return c.data.Bool(name) }

// User returns the picked user for the named option, or the zero User if missing.
func (c *CmdCtx) User(name string) discord.User { return c.data.User(name) }

// Channel returns the picked channel for the named option, or the zero
// ResolvedChannel if missing.
func (c *CmdCtx) Channel(name string) discord.ResolvedChannel { return c.data.Channel(name) }

// Attachment returns the uploaded attachment for the named option, or the
// zero Attachment if missing.
func (c *CmdCtx) Attachment(name string) discord.Attachment { return c.data.Attachment(name) }

/*
Reply sends a plain-text response to the interaction. Must run within
3 seconds of the command firing; otherwise use Defer + Followup.

	params:
	      text: the message body
	returns:
	      error: from disgo
*/
func (c *CmdCtx) Reply(text string) error {
	return c.event.CreateMessage(discord.NewMessageCreate().WithContent(text))
}

/*
ReplyEphemeral is like Reply but the response is visible only to the invoker.

	params:
	      text: the message body
	returns:
	      error: from disgo
*/
func (c *CmdCtx) ReplyEphemeral(text string) error {
	return c.event.CreateMessage(discord.NewMessageCreate().
		WithContent(text).
		WithFlags(discord.MessageFlagEphemeral))
}

/*
ReplyEmbed sends an embed response.

	params:
	      embed: a fully-built Embed
	returns:
	      error: from disgo
*/
func (c *CmdCtx) ReplyEmbed(embed discord.Embed) error {
	return c.event.CreateMessage(discord.NewMessageCreate().AddEmbeds(embed))
}

/*
ReplyFile sends a local file as a response. The file is opened, attached,
and closed by Discord after upload.

	params:
	      content:  optional message body
	      filePath: path on disk to the file to upload
	returns:
	      error: if the file cannot be opened or the response cannot be sent
*/
func (c *CmdCtx) ReplyFile(content, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("sikasa: open %s: %w", filePath, err)
	}
	defer f.Close()
	return c.event.CreateMessage(discord.NewMessageCreate().
		WithContent(content).
		AddFile(filepath.Base(filePath), "", f))
}

/*
ReplyURL streams a remote file directly into the response without writing
to disk. Useful for image/media APIs.

	params:
	      content:  optional message body
	      url:      remote file URL to fetch via HTTP GET
	      fileName: name shown to the recipient
	returns:
	      error: if the fetch fails or the upstream returns non-200
*/
func (c *CmdCtx) ReplyURL(content, url, fileName string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("sikasa: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sikasa: fetch %s: status %d", url, resp.StatusCode)
	}
	return c.event.CreateMessage(discord.NewMessageCreate().
		WithContent(content).
		AddFile(fileName, "", resp.Body))
}

/*
Defer acknowledges the interaction with a "Bot is thinking…" placeholder.
Use this when the handler needs more than 3 seconds to produce a result.
After deferring, finish the response with Followup or by editing the
interaction response directly.

	params:
	      ephemeral: if true, the eventual response is visible only to the invoker
	returns:
	      error: from disgo
*/
func (c *CmdCtx) Defer(ephemeral bool) error {
	return c.event.DeferCreateMessage(ephemeral)
}

/*
Followup sends a follow-up message after a Defer. Discord allows
follow-ups for up to 15 minutes after the original interaction.

	params:
	      text: the follow-up body
	returns:
	      error: from disgo
*/
func (c *CmdCtx) Followup(text string) error {
	_, err := c.event.CreateFollowupMessage(discord.NewMessageCreate().WithContent(text))
	return err
}

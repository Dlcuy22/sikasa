// Package sikasa: msgctx.go
// Purpose: Defines MsgCtx, the per-message context passed to keyword/regex
// handlers. Bundles the disgo MessageCreate event with reply helpers for
// text, embeds, and media.
//
// Key Components:
//   - MsgCtx:       per-message handler context
//   - newMsgCtx():  internal constructor; called by bot.go's keyword dispatcher
//   - Reply / Send / ReplyFile / ReplyURL: response helpers
//   - AuthorMention / Author / ChannelID / GuildID: shortcuts to common fields
//
// Dependencies:
//   - github.com/disgoorg/disgo/discord:  message and reference types
//   - github.com/disgoorg/disgo/events:   MessageCreate event
//
// Note: Reply uses Discord's inline reply (message reference) so the bot's
// reply links back to the user's message; Send posts to the channel without
// the reference link.
package sikasa

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

// MsgCtx is the context passed to a keyword or regex handler.
//
// Key Fields:
//   - bot:   the parent Bot, exposed via Bot()
//   - event: the disgo MessageCreate event, exposed via Event()
type MsgCtx struct {
	bot   *Bot
	event *events.MessageCreate
}

func newMsgCtx(b *Bot, e *events.MessageCreate) *MsgCtx {
	return &MsgCtx{bot: b, event: e}
}

// Bot returns the parent Bot.
func (c *MsgCtx) Bot() *Bot { return c.bot }

// Event returns the underlying disgo *events.MessageCreate as an escape hatch.
func (c *MsgCtx) Event() *events.MessageCreate { return c.event }

// Message returns the raw discord.Message.
func (c *MsgCtx) Message() discord.Message { return c.event.Message }

// Content returns the raw message text.
func (c *MsgCtx) Content() string { return c.event.Message.Content }

// Author returns the user who sent the message.
func (c *MsgCtx) Author() discord.User { return c.event.Message.Author }

// AuthorMention returns the formatted mention string for the author.
func (c *MsgCtx) AuthorMention() string {
	return c.event.Message.Author.Mention()
}

// ChannelID returns the channel where the message was sent.
func (c *MsgCtx) ChannelID() string { return c.event.ChannelID.String() }

// GuildID returns the guild snowflake as a string, or empty for DMs.
func (c *MsgCtx) GuildID() string {
	if id := c.event.GuildID; id != nil {
		return id.String()
	}
	return ""
}

// reference builds the MessageReference used by all Reply* helpers so the
// bot's response is rendered as an inline reply to the user's message.
func (c *MsgCtx) reference() *discord.MessageReference {
	msgID := c.event.MessageID
	chID := c.event.ChannelID
	ref := &discord.MessageReference{
		MessageID: &msgID,
		ChannelID: &chID,
	}
	if c.event.GuildID != nil {
		gid := *c.event.GuildID
		ref.GuildID = &gid
	}
	return ref
}

/*
Reply sends a plain-text inline reply to the user's message.

	params:
	      text: the message body
	returns:
	      error: from disgo
*/
func (c *MsgCtx) Reply(text string) error {
	_, err := c.bot.client.Rest.CreateMessage(c.event.ChannelID, discord.NewMessageCreate().
		WithContent(text).
		WithMessageReference(c.reference()))
	return err
}

/*
Send posts a plain-text message to the same channel without referencing
the user's message. Use this when the response is informational rather
than a direct reply.

	params:
	      text: the message body
	returns:
	      error: from disgo
*/
func (c *MsgCtx) Send(text string) error {
	_, err := c.bot.client.Rest.CreateMessage(c.event.ChannelID, discord.NewMessageCreate().
		WithContent(text))
	return err
}

/*
ReplyEmbed sends an embed as an inline reply. Accepts either a built
discord.Embed or a sikasa *EmbedBuilder; pass the builder directly without
calling .Build() yourself.

	params:
	      embed: a discord.Embed or *EmbedBuilder
	returns:
	      error: from disgo, or an error if the embed type is unsupported
*/
func (c *MsgCtx) ReplyEmbed(embed any) error {
	e, err := toEmbed(embed)
	if err != nil {
		return err
	}
	_, err = c.bot.client.Rest.CreateMessage(c.event.ChannelID, discord.NewMessageCreate().
		AddEmbeds(e).
		WithMessageReference(c.reference()))
	return err
}

/*
SendEmbed posts an embed to the same channel without referencing the user's
message. Use it for informational announcements (queue listing, status
panels) where an inline reply marker would be visual noise. Accepts either
a discord.Embed or a *EmbedBuilder.

	params:
	      embed: a discord.Embed or *EmbedBuilder
	returns:
	      error: from disgo, or an error if the embed type is unsupported
*/
func (c *MsgCtx) SendEmbed(embed any) error {
	e, err := toEmbed(embed)
	if err != nil {
		return err
	}
	_, err = c.bot.client.Rest.CreateMessage(c.event.ChannelID, discord.NewMessageCreate().
		AddEmbeds(e))
	return err
}

/*
NewEmbed returns a fluent EmbedBuilder. Chain its setters and pass the
result to ReplyEmbed / SendEmbed; you do not need to call .Build().

	returns:
	      *EmbedBuilder: ready for .Title()/.Description()/.Field()/...
*/
func (c *MsgCtx) NewEmbed() *EmbedBuilder { return NewEmbed() }

/*
SendEmbedWithButtons posts an embed plus a single ActionRow of interactive
components (typically buttons returned from BuildYTSearchEmbed or any
custom picker). For multi-row component layouts, use Event() and build
the message manually.

	params:
	      embed:      a discord.Embed or *EmbedBuilder
	      components: interactive components (max 5 per Discord ActionRow)
	returns:
	      error: from disgo or an unsupported-embed-type error
*/
func (c *MsgCtx) SendEmbedWithButtons(embed any, components ...discord.InteractiveComponent) error {
	e, err := toEmbed(embed)
	if err != nil {
		return err
	}
	msg := discord.NewMessageCreate().AddEmbeds(e)
	if len(components) > 0 {
		msg = msg.AddActionRow(components...)
	}
	_, err = c.bot.client.Rest.CreateMessage(c.event.ChannelID, msg)
	return err
}

// toEmbed normalizes whatever the caller passed (raw struct or *EmbedBuilder)
// into a discord.Embed. Centralizing the type switch keeps the public
// signatures forgiving without forcing every caller to remember .Build().
func toEmbed(v any) (discord.Embed, error) {
	switch x := v.(type) {
	case discord.Embed:
		return x, nil
	case *EmbedBuilder:
		if x == nil {
			return discord.Embed{}, fmt.Errorf("sikasa: nil EmbedBuilder")
		}
		return x.Build(), nil
	default:
		return discord.Embed{}, fmt.Errorf("sikasa: unsupported embed type %T", v)
	}
}

/*
ReplyFile sends a local file as an inline reply. The file is opened,
attached, and closed automatically.

	params:
	      content:  optional message body sent alongside the file
	      filePath: path on disk to the file to upload
	returns:
	      error: if the file cannot be opened or the message cannot be sent
*/
func (c *MsgCtx) ReplyFile(content, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("sikasa: open %s: %w", filePath, err)
	}
	defer f.Close()
	_, err = c.bot.client.Rest.CreateMessage(c.event.ChannelID, discord.NewMessageCreate().
		WithContent(content).
		AddFile(filepath.Base(filePath), "", f).
		WithMessageReference(c.reference()))
	return err
}

/*
ReplyURL streams a remote file directly into the reply without writing
to disk.

	params:
	      content:  optional message body
	      url:      remote file URL to fetch via HTTP GET
	      fileName: name shown to the recipient
	returns:
	      error: if the fetch fails or the upstream returns non-200
*/
func (c *MsgCtx) ReplyURL(content, url, fileName string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("sikasa: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sikasa: fetch %s: status %d", url, resp.StatusCode)
	}
	_, err = c.bot.client.Rest.CreateMessage(c.event.ChannelID, discord.NewMessageCreate().
		WithContent(content).
		AddFile(fileName, "", resp.Body).
		WithMessageReference(c.reference()))
	return err
}

/*
React adds a reaction emoji to the user's message.

	params:
	      emoji: a unicode emoji or a custom emoji in "name:id" format
	returns:
	      error: from disgo
*/
func (c *MsgCtx) React(emoji string) error {
	return c.bot.client.Rest.AddReaction(c.event.ChannelID, c.event.MessageID, emoji)
}

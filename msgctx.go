// Package sikasa: msgctx.go
// Purpose: Defines MsgCtx, the per-message context passed to keyword/regex
// handlers. Bundles the underlying session and message with reply helpers
// for text, embeds, and media.
//
// Key Components:
//   - MsgCtx:       per-message handler context
//   - newMsgCtx():  internal constructor; called by the bot's dispatcher
//   - Reply / Send / ReplyFile / ReplyURL: response helpers
//   - AuthorMention / Author / ChannelID / GuildID: shortcuts to common fields
//
// Dependencies:
//   - github.com/bwmarrin/discordgo: Message, MessageReference types
//   - net/http, os, path/filepath: file-attachment helpers
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

	"github.com/bwmarrin/discordgo"
)

// MsgCtx is the context passed to a keyword or regex handler.
//
// Key Fields:
//   - s: the live session, exposed via DiscordGo()
//   - m: the underlying MessageCreate event, exposed via Message()
type MsgCtx struct {
	s *discordgo.Session
	m *discordgo.MessageCreate
}

func newMsgCtx(s *discordgo.Session, m *discordgo.MessageCreate) *MsgCtx {
	return &MsgCtx{s: s, m: m}
}

// DiscordGo returns the underlying *discordgo.Session as an escape hatch.
func (c *MsgCtx) DiscordGo() *discordgo.Session { return c.s }

// Message returns the raw *discordgo.MessageCreate event.
func (c *MsgCtx) Message() *discordgo.MessageCreate { return c.m }

// Content returns the raw message text.
func (c *MsgCtx) Content() string { return c.m.Content }

// Author returns the user who sent the message.
func (c *MsgCtx) Author() *discordgo.User { return c.m.Author }

// AuthorMention returns the formatted mention string for the author.
func (c *MsgCtx) AuthorMention() string {
	if c.m.Author == nil {
		return ""
	}
	return c.m.Author.Mention()
}

// ChannelID returns the channel where the message was sent.
func (c *MsgCtx) ChannelID() string { return c.m.ChannelID }

// GuildID returns the guild snowflake, or empty string for DMs.
func (c *MsgCtx) GuildID() string { return c.m.GuildID }

// reference builds the MessageReference used by all Reply* helpers so the
// bot's response is rendered as an inline reply to the user's message.
func (c *MsgCtx) reference() *discordgo.MessageReference {
	return &discordgo.MessageReference{
		MessageID: c.m.ID,
		ChannelID: c.m.ChannelID,
		GuildID:   c.m.GuildID,
	}
}

/*
Reply sends a plain-text inline reply to the user's message.

	params:
	      text: the message body
	returns:
	      error: from discordgo
*/
func (c *MsgCtx) Reply(text string) error {
	_, err := c.s.ChannelMessageSendComplex(c.m.ChannelID, &discordgo.MessageSend{
		Content:   text,
		Reference: c.reference(),
	})
	return err
}

/*
Send posts a plain-text message to the same channel without referencing
the user's message. Use this when the response is informational rather
than a direct reply.

	params:
	      text: the message body
	returns:
	      error: from discordgo
*/
func (c *MsgCtx) Send(text string) error {
	_, err := c.s.ChannelMessageSend(c.m.ChannelID, text)
	return err
}

/*
ReplyEmbed sends an embed as an inline reply.

	params:
	      embed: a fully-built MessageEmbed
	returns:
	      error: from discordgo
*/
func (c *MsgCtx) ReplyEmbed(embed *discordgo.MessageEmbed) error {
	_, err := c.s.ChannelMessageSendComplex(c.m.ChannelID, &discordgo.MessageSend{
		Embeds:    []*discordgo.MessageEmbed{embed},
		Reference: c.reference(),
	})
	return err
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
	_, err = c.s.ChannelMessageSendComplex(c.m.ChannelID, &discordgo.MessageSend{
		Content: content,
		Files: []*discordgo.File{{
			Name:   filepath.Base(filePath),
			Reader: f,
		}},
		Reference: c.reference(),
	})
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
	_, err = c.s.ChannelMessageSendComplex(c.m.ChannelID, &discordgo.MessageSend{
		Content: content,
		Files: []*discordgo.File{{
			Name:   fileName,
			Reader: resp.Body,
		}},
		Reference: c.reference(),
	})
	return err
}

/*
React adds a reaction emoji to the user's message.

	params:
	      emoji: a unicode emoji or a custom emoji in "name:id" format
	returns:
	      error: from discordgo
*/
func (c *MsgCtx) React(emoji string) error {
	return c.s.MessageReactionAdd(c.m.ChannelID, c.m.ID, emoji)
}

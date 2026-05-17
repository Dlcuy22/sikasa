// Package sikasa: cmdctx.go
// Purpose: Defines CmdCtx, the per-invocation context passed to slash command
// handlers. Bundles the underlying session and interaction with reply helpers
// for text, embeds, and media so handlers stay one-liners.
//
// Key Components:
//   - CmdCtx:       per-invocation handler context
//   - newCmdCtx():  internal constructor; called by the bot's dispatcher
//   - Reply / ReplyEmbed / ReplyFile / ReplyURL: response helpers
//   - String / Int / Bool / User / Channel / Attachment: option accessors
//   - Defer / Followup: long-running command pattern
//
// Dependencies:
//   - github.com/bwmarrin/discordgo: Interaction, InteractionResponse types
//   - net/http, os, path/filepath: file-attachment helpers
package sikasa

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bwmarrin/discordgo"
)

// CmdCtx is the context passed to a slash command handler.
//
// Key Fields:
//   - s: the live session, exposed via Session() for advanced use
//   - i: the underlying interaction, exposed via Interaction()
//
// Note: Reply must be called within 3 seconds of the interaction firing.
// For longer work, call Defer() first and use Followup() afterwards.
type CmdCtx struct {
	s    *discordgo.Session
	i    *discordgo.InteractionCreate
	opts map[string]*discordgo.ApplicationCommandInteractionDataOption
}

// newCmdCtx builds a CmdCtx and pre-indexes the option list for O(1) lookup.
func newCmdCtx(s *discordgo.Session, i *discordgo.InteractionCreate) *CmdCtx {
	opts := make(map[string]*discordgo.ApplicationCommandInteractionDataOption)
	for _, o := range i.ApplicationCommandData().Options {
		opts[o.Name] = o
	}
	return &CmdCtx{s: s, i: i, opts: opts}
}

// Session returns the underlying *discordgo.Session as an escape hatch.
func (c *CmdCtx) Session() *discordgo.Session { return c.s }

// Interaction returns the raw *discordgo.Interaction as an escape hatch.
func (c *CmdCtx) Interaction() *discordgo.Interaction { return c.i.Interaction }

// Author returns the user who invoked the command, regardless of whether
// the invocation happened in a guild (Member.User) or in DMs (User).
func (c *CmdCtx) Author() *discordgo.User {
	if c.i.Member != nil && c.i.Member.User != nil {
		return c.i.Member.User
	}
	return c.i.User
}

// ChannelID returns the channel where the command was invoked.
func (c *CmdCtx) ChannelID() string { return c.i.ChannelID }

// GuildID returns the guild snowflake, or empty string for DM invocations.
func (c *CmdCtx) GuildID() string { return c.i.GuildID }

// String returns the string value for the named option, or "" if missing.
func (c *CmdCtx) String(name string) string {
	if o, ok := c.opts[name]; ok {
		return o.StringValue()
	}
	return ""
}

// Int returns the integer value for the named option, or 0 if missing.
func (c *CmdCtx) Int(name string) int64 {
	if o, ok := c.opts[name]; ok {
		return o.IntValue()
	}
	return 0
}

// Bool returns the boolean value for the named option, or false if missing.
func (c *CmdCtx) Bool(name string) bool {
	if o, ok := c.opts[name]; ok {
		return o.BoolValue()
	}
	return false
}

// User returns the picked user for the named option, resolved against
// the interaction's resolved-data, or nil if missing.
func (c *CmdCtx) User(name string) *discordgo.User {
	if o, ok := c.opts[name]; ok {
		return o.UserValue(c.s)
	}
	return nil
}

// Channel returns the picked channel for the named option, or nil if missing.
func (c *CmdCtx) Channel(name string) *discordgo.Channel {
	if o, ok := c.opts[name]; ok {
		return o.ChannelValue(c.s)
	}
	return nil
}

// Attachment returns the uploaded attachment for the named option, resolved
// from the interaction's Resolved.Attachments map, or nil if missing.
func (c *CmdCtx) Attachment(name string) *discordgo.MessageAttachment {
	o, ok := c.opts[name]
	if !ok {
		return nil
	}
	id, _ := o.Value.(string)
	if id == "" {
		return nil
	}
	resolved := c.i.ApplicationCommandData().Resolved
	if resolved == nil || resolved.Attachments == nil {
		return nil
	}
	return resolved.Attachments[id]
}

/*
Reply sends a plain-text response to the interaction. Must run within
3 seconds of the command firing; otherwise use Defer + Followup.

	params:
	      text: the message body
	returns:
	      error: from discordgo
*/
func (c *CmdCtx) Reply(text string) error {
	return c.s.InteractionRespond(c.i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: text},
	})
}

/*
ReplyEphemeral is like Reply but the response is visible only to the invoker.

	params:
	      text: the message body
	returns:
	      error: from discordgo
*/
func (c *CmdCtx) ReplyEphemeral(text string) error {
	return c.s.InteractionRespond(c.i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: text,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

/*
ReplyEmbed sends an embed response.

	params:
	      embed: a fully-built MessageEmbed
	returns:
	      error: from discordgo
*/
func (c *CmdCtx) ReplyEmbed(embed *discordgo.MessageEmbed) error {
	return c.s.InteractionRespond(c.i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

/*
ReplyFile sends a local file as a response. The file is opened, attached,
and closed automatically.

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
	return c.s.InteractionRespond(c.i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Files: []*discordgo.File{{
				Name:   filepath.Base(filePath),
				Reader: f,
			}},
		},
	})
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
	return c.s.InteractionRespond(c.i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Files: []*discordgo.File{{
				Name:   fileName,
				Reader: resp.Body,
			}},
		},
	})
}

/*
Defer acknowledges the interaction with a "Bot is thinking…" placeholder.
Use this when the handler needs more than 3 seconds to produce a result.
After deferring, finish the response with Followup or by editing the
interaction response directly.

	params:
	      ephemeral: if true, the eventual response is visible only to the invoker
	returns:
	      error: from discordgo
*/
func (c *CmdCtx) Defer(ephemeral bool) error {
	data := &discordgo.InteractionResponseData{}
	if ephemeral {
		data.Flags = discordgo.MessageFlagsEphemeral
	}
	return c.s.InteractionRespond(c.i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: data,
	})
}

/*
Followup sends a follow-up message after a Defer. Discord allows
follow-ups for up to 15 minutes after the original interaction.

	params:
	      text: the follow-up body
	returns:
	      error: from discordgo
*/
func (c *CmdCtx) Followup(text string) error {
	_, err := c.s.FollowupMessageCreate(c.i.Interaction, true, &discordgo.WebhookParams{
		Content: text,
	})
	return err
}

// Package sikasa: buttonctx.go
// Purpose: Defines ButtonCtx, the per-interaction context passed to button
// click handlers registered via Bot.OnButton. Provides reply, update, and
// defer helpers that mirror MsgCtx / CmdCtx so handler code stays uniform.
//
// Key Components:
//   - ButtonCtx:    handler context for a single button interaction
//   - Reply:        sends a fresh ephemeral or public response
//   - Update:       edits the message that hosted the button
//   - Defer:        acks the interaction without producing a visible reply
//
// Dependencies:
//   - github.com/disgoorg/disgo/discord:  message and component types
//   - github.com/disgoorg/disgo/handler:  ComponentEvent (interaction wrapper)
//   - github.com/disgoorg/disgo/events:   underlying event type
//
// Note: Discord requires a response within 3 seconds or the interaction
// fails. Reply, Update, and Defer all satisfy that requirement; long-running
// work should call Defer first and then Followup later.
package sikasa

import (
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
)

// ButtonCtx is the context passed to a Bot.OnButton handler.
//
// Key Fields:
//   - bot:      parent Bot, exposed via Bot()
//   - event:    the disgo *handler.ComponentEvent, exposed via Event()
//   - data:     the button interaction data
//   - vars:     path variables extracted from the customID pattern
//   - deferred: tracks whether the handler has called DeferUpdate, so
//     subsequent Update/UpdateEmbed/ClearComponents go through the
//     followup-style UpdateInteractionResponse instead of trying to
//     respond a second time
//
// Note: ButtonCtx values are constructed by the dispatcher; do not build
// them yourself. The embedded *handler.ComponentEvent is exposed as an
// escape hatch for advanced use (followups, file uploads, modals).
type ButtonCtx struct {
	bot      *Bot
	event    *handler.ComponentEvent
	data     discord.ButtonInteractionData
	vars     map[string]string
	deferred bool
}

// Bot returns the parent Bot.
func (c *ButtonCtx) Bot() *Bot { return c.bot }

// Event returns the underlying *handler.ComponentEvent for advanced flows.
func (c *ButtonCtx) Event() *handler.ComponentEvent { return c.event }

// Data returns the underlying button interaction data.
func (c *ButtonCtx) Data() discord.ButtonInteractionData { return c.data }

// CustomID returns the raw customID string of the clicked button.
func (c *ButtonCtx) CustomID() string { return c.data.CustomID() }

// Vars returns path variables parsed from the customID pattern. For
// "/sikasa/ytsearch/{session}/{idx}" with customID
// "/sikasa/ytsearch/abcd/2", Vars()["session"] is "abcd" and Vars()["idx"]
// is "2".
func (c *ButtonCtx) Vars() map[string]string { return c.vars }

// Var is a convenience for Vars()[name].
func (c *ButtonCtx) Var(name string) string {
	if c.vars == nil {
		return ""
	}
	return c.vars[name]
}

// Author returns the user who clicked the button.
func (c *ButtonCtx) Author() discord.User { return c.event.User() }

// AuthorID returns the snowflake of the user who clicked the button.
// Convenient for permission checks against a stored invokerID.
func (c *ButtonCtx) AuthorID() string { return c.event.User().ID.String() }

// GuildID returns the guild snowflake as a string, or empty for DMs.
func (c *ButtonCtx) GuildID() string {
	if id := c.event.GuildID(); id != nil {
		return id.String()
	}
	return ""
}

// ChannelID returns the channel where the button lives.
func (c *ButtonCtx) ChannelID() string { return c.event.Channel().ID().String() }

/*
Reply sends a new message in response to the button click. The message is
ephemeral (only visible to the clicker) by default, since most button
flows already have visible context in the parent message.

	params:
	      text: the message body
	returns:
	      error: from disgo
*/
func (c *ButtonCtx) Reply(text string) error {
	return c.event.CreateMessage(discord.NewMessageCreate().
		WithContent(text).
		WithFlags(discord.MessageFlagEphemeral))
}

/*
ReplyPublic sends a non-ephemeral message in response to the click.

	params:
	      text: the message body
	returns:
	      error: from disgo
*/
func (c *ButtonCtx) ReplyPublic(text string) error {
	return c.event.CreateMessage(discord.NewMessageCreate().
		WithContent(text))
}

/*
ReplyEmbed sends an ephemeral embed reply. Accepts a discord.Embed or a
*EmbedBuilder; pass the builder directly without calling .Build().

	params:
	      embed: a discord.Embed or *EmbedBuilder
	returns:
	      error: from disgo or an unsupported-type error
*/
func (c *ButtonCtx) ReplyEmbed(embed any) error {
	e, err := toEmbed(embed)
	if err != nil {
		return err
	}
	return c.event.CreateMessage(discord.NewMessageCreate().
		AddEmbeds(e).
		WithFlags(discord.MessageFlagEphemeral))
}

/*
Update edits the message that owns the clicked button. Useful for
swapping a picker into a confirmation. Pass an empty string to clear the
content; existing embeds and components are preserved unless cleared
explicitly via Event().UpdateMessage with the appropriate ClearX calls.
After DeferUpdate has been called, this method edits the deferred response
instead of issuing a fresh one.

	params:
	      text: new message body
	returns:
	      error: from disgo
*/
func (c *ButtonCtx) Update(text string) error {
	upd := discord.NewMessageUpdate().WithContent(text)
	if c.deferred {
		_, err := c.event.UpdateInteractionResponse(upd)
		return err
	}
	return c.event.UpdateMessage(upd)
}

/*
UpdateEmbed edits the message to display the given embed and clears all
component rows so the picker disappears once a choice is made. Accepts a
discord.Embed or a *EmbedBuilder. After DeferUpdate has been called, the
edit is dispatched against the deferred response.

	params:
	      embed: a discord.Embed or *EmbedBuilder
	returns:
	      error: from disgo or an unsupported-type error
*/
func (c *ButtonCtx) UpdateEmbed(embed any) error {
	e, err := toEmbed(embed)
	if err != nil {
		return err
	}
	upd := discord.NewMessageUpdate().
		WithEmbeds(e).
		ClearComponents()
	if c.deferred {
		_, err = c.event.UpdateInteractionResponse(upd)
		return err
	}
	return c.event.UpdateMessage(upd)
}

/*
ClearComponents edits the message to drop every action row, leaving the
content and embeds untouched. Use it when the buttons should disappear but
the surrounding context stays visible.

	returns:
	      error: from disgo
*/
func (c *ButtonCtx) ClearComponents() error {
	upd := discord.NewMessageUpdate().ClearComponents()
	if c.deferred {
		_, err := c.event.UpdateInteractionResponse(upd)
		return err
	}
	return c.event.UpdateMessage(upd)
}

/*
DeferUpdate acks the click within Discord's 3 second window without
visibly changing the message. Use it before doing slow work (yt-dlp probe,
voice handshake, etc.) that would otherwise leave the user staring at
"this interaction failed". After this call, Update / UpdateEmbed /
ClearComponents transparently target the deferred response.

	returns:
	      error: from disgo
*/
func (c *ButtonCtx) DeferUpdate() error {
	if c.deferred {
		return nil
	}
	if err := c.event.DeferUpdateMessage(); err != nil {
		return err
	}
	c.deferred = true
	return nil
}

/*
Defer acks the interaction without sending a visible reply. Use this when
the handler will follow up later (more than 3 seconds out).

	params:
	      ephemeral: when true, any later followup is only shown to the clicker
	returns:
	      error: from disgo
*/
func (c *ButtonCtx) Defer(ephemeral bool) error {
	if ephemeral {
		return c.event.DeferCreateMessage(true)
	}
	return c.event.DeferCreateMessage(false)
}

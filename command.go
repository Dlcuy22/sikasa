// Package sikasa: command.go
// Purpose: Provides a fluent CommandBuilder for declaring slash commands
// alongside their handlers in one place. Builders translate to disgo's
// discord.SlashCommandCreate at Start() time, and their handlers are wired
// into the disgo handler.Router for dispatch.
//
// Key Components:
//   - CommandBuilder:  fluent type for one slash command
//   - Bot.Command():   entry point that returns a fresh builder
//   - StringArg/IntArg/BoolArg/UserArg/ChannelArg/AttachmentArg: option helpers
//   - CmdHandler:      function signature for command handlers
//
// Dependencies:
//   - github.com/disgoorg/disgo/discord:  ApplicationCommandCreate types
//   - github.com/disgoorg/disgo/handler:  router and CommandEvent
package sikasa

import (
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
)

// CmdHandler is the signature every slash command handler must satisfy.
// Returning an error logs it via the bot's logger; it does not propagate
// further, since by the time the handler runs the user has already been
// shown the bot's response.
type CmdHandler func(ctx *CmdCtx) error

// CommandBuilder accumulates a slash command's definition and handler.
//
// Key Fields:
//   - name, desc: surfaced to Discord
//   - opts:       command options in the order added
//   - handler:    invoked on InteractionApplicationCommand
//
// Note: Builders are not thread-safe; finish configuration before Start().
type CommandBuilder struct {
	name    string
	desc    string
	opts    []discord.ApplicationCommandOption
	handler CmdHandler
}

/*
Command registers a new slash command and returns its builder. Chain
arg helpers and Handle() to finish the definition.

	params:
	      name: the command name as it appears after the slash
	      description: the user-visible description
	returns:
	      *CommandBuilder: builder for further configuration
*/
func (b *Bot) Command(name, description string) *CommandBuilder {
	cb := &CommandBuilder{name: name, desc: description}
	b.cmds = append(b.cmds, cb)
	return cb
}

/*
StringArg adds a string option to the command.

	params:
	      name:        option name shown in the command picker
	      description: option description
	      required:    if true, Discord rejects invocations missing this arg
	returns:
	      *CommandBuilder: receiver, for chaining
*/
func (c *CommandBuilder) StringArg(name, description string, required bool) *CommandBuilder {
	c.opts = append(c.opts, discord.ApplicationCommandOptionString{
		Name:        name,
		Description: description,
		Required:    required,
	})
	return c
}

/*
IntArg adds an integer option to the command.

	params:
	      name, description, required: see StringArg
	returns:
	      *CommandBuilder: receiver, for chaining
*/
func (c *CommandBuilder) IntArg(name, description string, required bool) *CommandBuilder {
	c.opts = append(c.opts, discord.ApplicationCommandOptionInt{
		Name:        name,
		Description: description,
		Required:    required,
	})
	return c
}

/*
BoolArg adds a boolean option to the command.

	params:
	      name, description, required: see StringArg
	returns:
	      *CommandBuilder: receiver, for chaining
*/
func (c *CommandBuilder) BoolArg(name, description string, required bool) *CommandBuilder {
	c.opts = append(c.opts, discord.ApplicationCommandOptionBool{
		Name:        name,
		Description: description,
		Required:    required,
	})
	return c
}

/*
UserArg adds a user-picker option to the command. The picked user is
resolvable via ctx.User(name).

	params:
	      name, description, required: see StringArg
	returns:
	      *CommandBuilder: receiver, for chaining
*/
func (c *CommandBuilder) UserArg(name, description string, required bool) *CommandBuilder {
	c.opts = append(c.opts, discord.ApplicationCommandOptionUser{
		Name:        name,
		Description: description,
		Required:    required,
	})
	return c
}

/*
ChannelArg adds a channel-picker option to the command.

	params:
	      name, description, required: see StringArg
	returns:
	      *CommandBuilder: receiver, for chaining
*/
func (c *CommandBuilder) ChannelArg(name, description string, required bool) *CommandBuilder {
	c.opts = append(c.opts, discord.ApplicationCommandOptionChannel{
		Name:        name,
		Description: description,
		Required:    required,
	})
	return c
}

/*
AttachmentArg adds a file attachment option to the command. The uploaded
file is resolvable via ctx.Attachment(name).

	params:
	      name, description, required: see StringArg
	returns:
	      *CommandBuilder: receiver, for chaining
*/
func (c *CommandBuilder) AttachmentArg(name, description string, required bool) *CommandBuilder {
	c.opts = append(c.opts, discord.ApplicationCommandOptionAttachment{
		Name:        name,
		Description: description,
		Required:    required,
	})
	return c
}

/*
Handle attaches the handler that runs when the command is invoked. This
finalizes the builder.

	params:
	      h: the handler invoked with a *CmdCtx
	returns:
	      *CommandBuilder: receiver, for chaining
*/
func (c *CommandBuilder) Handle(h CmdHandler) *CommandBuilder {
	c.handler = h
	return c
}

// build converts the builder to disgo's discord.SlashCommandCreate value
// expected by handler.SyncCommands.
func (c *CommandBuilder) build() discord.ApplicationCommandCreate {
	return discord.SlashCommandCreate{
		Name:        c.name,
		Description: c.desc,
		Options:     c.opts,
	}
}

// register wires the builder's handler into the disgo router. Called by
// Bot.Start() after the router has been created. Commands without handlers
// are silently skipped so users can declare them without binding logic yet.
func (c *CommandBuilder) register(b *Bot) {
	if c.handler == nil {
		return
	}
	cmd := c
	b.router.SlashCommand("/"+c.name, func(data discord.SlashCommandInteractionData, e *handler.CommandEvent) error {
		ctx := newCmdCtx(b, e, data)
		return cmd.handler(ctx)
	})
}

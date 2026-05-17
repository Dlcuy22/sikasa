// Package sikasa: command.go
// Purpose: Provides a fluent CommandBuilder for declaring slash commands
// alongside their handlers in one place, eliminating the discordgo split
// between command definition and interaction routing.
//
// Key Components:
//   - CommandBuilder:  fluent type for one slash command
//   - Bot.Command():   entry point that returns a fresh builder
//   - StringArg/IntArg/BoolArg/UserArg/ChannelArg/AttachmentArg: option helpers
//   - CmdHandler:      function signature for command handlers
//
// Dependencies:
//   - github.com/bwmarrin/discordgo: ApplicationCommand types
package sikasa

import "github.com/bwmarrin/discordgo"

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
	opts    []*discordgo.ApplicationCommandOption
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
	c.opts = append(c.opts, &discordgo.ApplicationCommandOption{
		Type:        discordgo.ApplicationCommandOptionString,
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
	c.opts = append(c.opts, &discordgo.ApplicationCommandOption{
		Type:        discordgo.ApplicationCommandOptionInteger,
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
	c.opts = append(c.opts, &discordgo.ApplicationCommandOption{
		Type:        discordgo.ApplicationCommandOptionBoolean,
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
	c.opts = append(c.opts, &discordgo.ApplicationCommandOption{
		Type:        discordgo.ApplicationCommandOptionUser,
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
	c.opts = append(c.opts, &discordgo.ApplicationCommandOption{
		Type:        discordgo.ApplicationCommandOptionChannel,
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
	c.opts = append(c.opts, &discordgo.ApplicationCommandOption{
		Type:        discordgo.ApplicationCommandOptionAttachment,
		Name:        name,
		Description: description,
		Required:    required,
	})
	return c
}

/*
Handle attaches the handler that runs when the command is invoked. This
finalizes the builder; further arg calls after Handle still work but are
unusual since builders are typically configured top-to-bottom.

	params:
	      h: the handler invoked with a *CmdCtx
	returns:
	      *CommandBuilder: receiver, for chaining
*/
func (c *CommandBuilder) Handle(h CmdHandler) *CommandBuilder {
	c.handler = h
	return c
}

// build converts the builder to the *discordgo.ApplicationCommand value
// expected by ApplicationCommandBulkOverwrite.
func (c *CommandBuilder) build() *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:        c.name,
		Description: c.desc,
		Options:     c.opts,
	}
}

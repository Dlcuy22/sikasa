// Package sikasa: group.go
// Purpose: Implements logical command grouping to support clean modularization
// of bot commands, prefix commands, keywords, and buttons.
//
// Key Components:
//   - Group:      a logical namespace and routing wrapper
//   - Bot.Group(): entry point for registering a group
//
// Dependencies:
//   - None
package sikasa

// Group represents a logical namespace or category of bot commands,
// prefix commands, keywords, and buttons.
//
// Key Fields:
//   - bot:  the underlying Bot instance
//   - name: the group's name, useful for logging or debugging
type Group struct {
	bot  *Bot
	name string
}

/*
Bot returns the parent Bot instance of the Group.

	returns:
	      *Bot: the underlying bot
*/
func (g *Group) Bot() *Bot {
	return g.bot
}

/*
Name returns the name of the Group.

	returns:
	      string: group name
*/
func (g *Group) Name() string {
	return g.name
}

/*
Group registers a sub-group of commands, keywords, and handlers.
The setup function is called immediately with the new Group.

	params:
	      name:  the group's name
	      setup: registration callback
	returns:
	      *Bot:  the bot instance, for chaining
*/
func (b *Bot) Group(name string, setup func(*Group)) *Bot {
	g := &Group{
		bot:  b,
		name: name,
	}
	setup(g)
	return b
}

/*
Command registers a new slash command on the group's Bot and returns its builder.

	params:
	      name:        the slash command name
	      description: description of the slash command
	returns:
	      *CommandBuilder: slash command builder
*/
func (g *Group) Command(name, description string) *CommandBuilder {
	return g.bot.Command(name, description)
}

/*
OnKeyword registers a substring-based keyword rule on the group's Bot.

	params:
	      terms: keyword strings to match
	returns:
	      *KeywordBuilder: keyword builder
*/
func (g *Group) OnKeyword(terms ...string) *KeywordBuilder {
	return g.bot.OnKeyword(terms...)
}

/*
OnRegex registers a regex-based message rule on the group's Bot.

	params:
	      pattern: regex pattern to match
	returns:
	      *KeywordBuilder: keyword builder
*/
func (g *Group) OnRegex(pattern string) *KeywordBuilder {
	return g.bot.OnRegex(pattern)
}

/*
OnPrefix registers a prefix-based text command on the group's Bot.

	params:
	      name:        prefix command name
	      description: description of prefix command
	returns:
	      *PrefixBuilder: prefix builder
*/
func (g *Group) OnPrefix(name, description string) *PrefixBuilder {
	return g.bot.OnPrefix(name, description)
}

/*
OnButton registers a button handler for a specific custom ID pattern on the group's Bot.

	params:
	      pattern: path pattern for custom ID
	      h:       handler for the button
	returns:
	      *Group:  the Group instance, for chaining
*/
func (g *Group) OnButton(pattern string, h ButtonHandler) *Group {
	g.bot.OnButton(pattern, h)
	return g
}

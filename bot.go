// Package sikasa: bot.go
// Purpose: Defines the top-level Bot type, lifecycle (New, Start, Stop),
// intent helpers, and the registries that command/keyword builders write into.
//
// Key Components:
//   - Bot:           wraps *discordgo.Session and owns the registries
//   - New():         constructs a Bot with sensible defaults
//   - WithIntents(): fluent setter for gateway intents
//   - Start()/Stop(): lifecycle, opens the websocket and registers commands
//
// Dependencies:
//   - github.com/bwmarrin/discordgo: underlying low-level binding
//
// Error Types:
//   - none package-specific; errors are surfaced from discordgo verbatim
package sikasa

import (
	"fmt"
	"log"

	"github.com/bwmarrin/discordgo"
)

// Intent re-exports for ergonomic use without importing discordgo directly
// in caller code. Sikasa does not redefine the bitmask; these are aliases.
const (
	IntentsAll                 = discordgo.IntentsAll
	IntentsAllWithoutPrivileged = discordgo.IntentsAllWithoutPrivileged
	IntentsNone                = discordgo.IntentsNone
)

// Bot is the high-level wrapper around discordgo.Session.
//
// Key Fields:
//   - session: the underlying discordgo session, exposed via Session()
//   - guildID: optional dev-guild for instant command sync; empty means global
//   - cmds:    registered command builders, flushed to Discord on Start()
//   - kws:     registered keyword matchers, evaluated on every MessageCreate
//
// Note: Not safe for concurrent registration; build all commands and keywords
// before calling Start(). After Start, the session itself is goroutine-safe.
type Bot struct {
	session *discordgo.Session
	guildID string
	cmds    []*CommandBuilder
	kws     []*KeywordBuilder
	logger  *log.Logger
}

/*
New constructs a Bot with the given token. The token is automatically
prefixed with "Bot " if not already prefixed; pass a raw bot token here.

	params:
	      token: the Discord bot token from the Developer Portal
	returns:
	      *Bot:  a configured Bot ready for command/keyword registration
	      error: if discordgo cannot construct the underlying session
*/
func New(token string) (*Bot, error) {
	if len(token) > 4 && token[:4] != "Bot " && token[:7] != "Bearer " {
		token = "Bot " + token
	}
	s, err := discordgo.New(token)
	if err != nil {
		return nil, fmt.Errorf("sikasa: %w", err)
	}
	return &Bot{
		session: s,
		logger:  log.Default(),
	}, nil
}

/*
WithIntents sets the gateway intents on the session. Must be called before Start().

	params:
	      intents: bitmask of discordgo.Intent values
	returns:
	      *Bot: receiver, for chaining
*/
func (b *Bot) WithIntents(intents discordgo.Intent) *Bot {
	b.session.Identify.Intents = intents
	return b
}

/*
WithGuild scopes slash command registration to a single guild. Per-guild
commands sync instantly, which is ideal during development. Leave unset
for global commands (which can take up to an hour to propagate).

	params:
	      guildID: the Discord guild snowflake to scope commands to
	returns:
	      *Bot: receiver, for chaining
*/
func (b *Bot) WithGuild(guildID string) *Bot {
	b.guildID = guildID
	return b
}

/*
WithLogger swaps the default logger. Useful for routing bot logs through
slog or a structured logger.

	params:
	      l: standard library logger; pass nil to silence
	returns:
	      *Bot: receiver, for chaining
*/
func (b *Bot) WithLogger(l *log.Logger) *Bot {
	if l == nil {
		l = log.New(discardWriter{}, "", 0)
	}
	b.logger = l
	return b
}

/*
DiscordGo returns the underlying *discordgo.Session as an escape hatch for
features the wrapper does not cover (voice, audit log, sharding, etc).

	returns:
	      *discordgo.Session: the live session; safe to use after Start()
*/
func (b *Bot) DiscordGo() *discordgo.Session {
	return b.session
}

/*
Start opens the gateway connection, attaches dispatchers, and bulk-overwrites
all registered slash commands.

	returns:
	      error: if the gateway fails to open or command registration fails
	note: Blocks only briefly to bring the session online; events run in goroutines.
*/
func (b *Bot) Start() error {
	b.session.AddHandler(b.dispatchInteraction)
	b.session.AddHandler(b.dispatchMessage)

	if err := b.session.Open(); err != nil {
		return fmt.Errorf("sikasa: open gateway: %w", err)
	}

	if err := b.syncCommands(); err != nil {
		// Close to avoid leaking a half-initialized session on failure.
		_ = b.session.Close()
		return err
	}

	b.logger.Printf("sikasa: bot online as %s", b.session.State.User.String())
	return nil
}

/*
Stop closes the gateway connection. Always call this via defer after Start().

	returns:
	      error: if the underlying close fails
*/
func (b *Bot) Stop() error {
	return b.session.Close()
}

// syncCommands flushes all registered command builders to Discord using
// BulkOverwrite, which atomically replaces the command set and removes
// any stale commands from previous runs.
func (b *Bot) syncCommands() error {
	if len(b.cmds) == 0 {
		return nil
	}
	apps := make([]*discordgo.ApplicationCommand, 0, len(b.cmds))
	for _, c := range b.cmds {
		apps = append(apps, c.build())
	}
	_, err := b.session.ApplicationCommandBulkOverwrite(b.session.State.User.ID, b.guildID, apps)
	if err != nil {
		return fmt.Errorf("sikasa: register commands: %w", err)
	}
	b.logger.Printf("sikasa: registered %d command(s)", len(apps))
	return nil
}

// dispatchInteraction routes incoming interactions to the matching builder.
// Currently handles application commands; component / modal routing can be
// added later without breaking the existing API.
func (b *Bot) dispatchInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}
	name := i.ApplicationCommandData().Name
	for _, c := range b.cmds {
		if c.name == name && c.handler != nil {
			ctx := newCmdCtx(s, i)
			if err := c.handler(ctx); err != nil {
				b.logger.Printf("sikasa: command %q error: %v", name, err)
			}
			return
		}
	}
}

// dispatchMessage walks the keyword registry on every MessageCreate.
// Self-messages are ignored to prevent feedback loops.
func (b *Bot) dispatchMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.ID == s.State.User.ID {
		return
	}
	for _, kw := range b.kws {
		if kw.matches(m.Content) {
			ctx := newMsgCtx(s, m)
			if err := kw.fire(ctx); err != nil {
				b.logger.Printf("sikasa: keyword %v error: %v", kw.terms, err)
			}
			// Continue evaluating; multiple keyword rules can fire on one message.
		}
	}
}

// discardWriter is a no-op io.Writer used when the caller silences the logger.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

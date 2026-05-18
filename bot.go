// Package sikasa: bot.go
// Purpose: Defines the top-level Bot type, lifecycle (New, Start, Stop),
// intent helpers, and the registries that command/keyword builders write into.
//
// Key Components:
//   - Bot:           wraps *bot.Client (disgo) and owns the registries
//   - New():         constructs a Bot with sensible defaults; defers wiring
//     until Start() so options can be appended fluently
//   - WithIntents(): fluent setter for gateway intents
//   - Start()/Stop(): lifecycle, opens the gateway and syncs slash commands
//
// Dependencies:
//   - github.com/disgoorg/disgo:         high-level Discord bot framework
//   - github.com/disgoorg/disgo/voice:   voice connection manager + DAVE
//   - github.com/thomas-vilte/dave-go:   pure-Go DAVE/E2EE backend
//
// Note: discordgo has been retired in favour of disgo because the latter is
// the only major Go Discord library with native DAVE (E2EE) support, which
// Discord enforces in many voice regions (close code 4017 without it).
package sikasa

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"sync"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/thomas-vilte/dave-go/session"
)

// Intents is an alias of disgo's gateway.Intents so callers do not need to
// import the gateway package directly for common cases.
type Intents = gateway.Intents

// Re-exported intent bundles. Pass these to WithIntents() to control which
// gateway events the bot subscribes to.
//
// Note: disgo splits intents into privileged (members, presences, message
// content) and non-privileged groups. IntentsAll OR's both together so the
// bot subscribes to everything; this requires the privileged intents to be
// enabled in the Developer Portal.
const (
	IntentsAll           = gateway.IntentsAll
	IntentsNonPrivileged = gateway.IntentsNonPrivileged
	IntentsPrivileged    = gateway.IntentsPrivileged
	IntentsNone          = gateway.IntentsNone
)

// Bot is the high-level wrapper around disgo's bot.Client.
//
// Key Fields:
//   - token:    raw bot token; consumed only at Start()
//   - intents:  gateway intents bitmask
//   - guildID:  optional dev-guild for instant command sync; zero means global
//   - cmds:     registered command builders, flushed to Discord on Start()
//   - kws:      registered keyword matchers, evaluated on every MessageCreate
//   - prefix:   global text prefix that triggers PrefixBuilder dispatch;
//               empty string disables prefix routing entirely
//   - prefixes: registered prefix command builders
//   - prefixIndex: lookup table built at Start(); keys include both names
//                  and aliases (lower-cased)
//   - client:   the live disgo client; nil until Start() succeeds
//   - voices:   per-guild voice contexts, keyed by guild ID
//   - slog:     structured logger handed to disgo (gateway, voice, REST)
//
// Note: Not safe for concurrent registration; build all commands and keywords
// before calling Start(). After Start, the underlying client is goroutine-safe.
type Bot struct {
	token   string
	intents gateway.Intents
	guildID snowflake.ID
	cmds    []*CommandBuilder
	kws     []*KeywordBuilder
	logger  *log.Logger
	slog    *slog.Logger

	prefix      string
	prefixes    []*PrefixBuilder
	prefixIndex map[string]*PrefixBuilder

	buttonRoutes []buttonRoute

	ytSearches   map[string]*ytSearchSession
	ytSearchesMu sync.Mutex

	client *bot.Client
	router *handler.Mux

	voices   map[snowflake.ID]*VoiceCtx
	voicesMu sync.Mutex
}

/*
New constructs a Bot with the given token. The "Bot " prefix is added by
disgo internally, so pass the raw token from the Developer Portal.

	params:
	      token: the Discord bot token from the Developer Portal
	returns:
	      *Bot:  a configured Bot ready for command/keyword registration
	      error: reserved for future validation; currently always nil
*/
func New(token string) (*Bot, error) {
	if token == "" {
		return nil, ErrEmptyToken
	}
	return &Bot{
		token:   token,
		intents: gateway.IntentsNone,
		logger:  log.Default(),
		voices:  make(map[snowflake.ID]*VoiceCtx),
	}, nil
}

/*
WithIntents sets the gateway intents. Must be called before Start().

	params:
	      intents: bitmask of gateway.Intent values
	returns:
	      *Bot: receiver, for chaining
*/
func (b *Bot) WithIntents(intents gateway.Intents) *Bot {
	b.intents = intents
	return b
}

/*
WithGuild scopes slash command registration to a single guild. Per-guild
commands sync instantly, which is ideal during development. Leave unset
for global commands (which can take up to an hour to propagate).

	params:
	      guildID: the Discord guild snowflake; accepts the same string form
	               that the Developer Portal and Discord client display
	returns:
	      *Bot: receiver, for chaining
*/
func (b *Bot) WithGuild(guildID string) *Bot {
	if guildID == "" {
		b.guildID = 0
		return b
	}
	id, err := snowflake.Parse(guildID)
	if err != nil {
		b.logger.Printf("sikasa: invalid guild id %q: %v", guildID, err)
		return b
	}
	b.guildID = id
	return b
}

/*
WithLogger swaps the default logger. Pass nil to silence output.

	params:
	      l: standard library logger
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
WithSlog sets the structured logger that gets handed to disgo (gateway,
voice, REST). Use this to surface heartbeat warnings, voice state changes,
and DAVE/E2EE handshake details. Pass nil to disable.

	params:
	      l: a *slog.Logger; level controls verbosity
	returns:
	      *Bot: receiver, for chaining
*/
func (b *Bot) WithSlog(l *slog.Logger) *Bot {
	b.slog = l
	return b
}

/*
WithVerbose enables debug-level structured logging on stderr for both the
sikasa wrapper and the underlying disgo client. Useful when diagnosing
voice-region issues, slow interaction acks, or gateway zombies.

	returns:
	      *Bot: receiver, for chaining
*/
func (b *Bot) WithVerbose() *Bot {
	b.slog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:     slog.LevelDebug,
		AddSource: true,
	}))
	return b
}

/*
Disgo returns the underlying *bot.Client as an escape hatch for features the
wrapper does not cover (sharding, manual REST calls, advanced events, etc).
Returns nil before Start() has been called.

	returns:
	      *bot.Client: the live disgo client, or nil if the bot has not started
*/
func (b *Bot) Disgo() *bot.Client {
	return b.client
}

/*
Voice returns the bot's VoiceManager, the entry point for joining voice
channels and starting playback.

	returns:
	      *VoiceManager: helper for voice channel operations
*/
func (b *Bot) Voice() *VoiceManager {
	return &VoiceManager{bot: b}
}

/*
Start builds the disgo client, opens the gateway, syncs slash commands, and
wires up keyword/message dispatchers. Returns once the gateway handshake is
complete; events run in disgo-managed goroutines from there.

	returns:
	      error: if client construction, gateway open, or command sync fails
*/
func (b *Bot) Start() error {
	b.router = handler.New()

	for _, c := range b.cmds {
		if c.handler == nil {
			continue
		}
		c.register(b)
	}
	b.indexPrefixes()
	b.registerButtons()

	opts := []bot.ConfigOpt{
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(b.intents),
		),
		bot.WithCacheConfigOpts(
			cache.WithCaches(cache.FlagGuilds | cache.FlagVoiceStates),
		),
		bot.WithVoiceManagerConfigOpts(
			voice.WithDaveSessionCreateFunc(session.New),
		),
		// Async dispatch is critical for voice: conn.Open() blocks waiting
		// for VoiceServerUpdate from the gateway. Without async events the
		// gateway listener loop deadlocks against the slash command handler
		// that called conn.Open(), and Discord shows "did not respond".
		bot.WithEventManagerConfigOpts(
			bot.WithAsyncEventsEnabled(),
		),
		bot.WithEventListeners(b.router),
		bot.WithEventListenerFunc(b.dispatchMessage),
	}
	// Wrap the user's slog handler (or a discard handler if none was set) so
	// we can sniff for "no active epoch" errors and auto-reconnect the
	// affected VoiceCtx. The wrapper is transparent for everything else.
	inner := b.slog
	if inner == nil {
		inner = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{
			Level: slog.LevelError,
		}))
	}
	wrapped := slog.New(newRecoveryHandler(inner.Handler(), b))
	opts = append(opts, bot.WithLogger(wrapped))

	client, err := disgo.New(b.token, opts...)
	if err != nil {
		return fmt.Errorf("sikasa: build client: %w", err)
	}
	b.client = client

	if err := client.OpenGateway(context.TODO()); err != nil {
		client.Close(context.TODO())
		return fmt.Errorf("sikasa: open gateway: %w", err)
	}

	if err := b.syncCommands(); err != nil {
		client.Close(context.TODO())
		return err
	}

	b.logger.Printf("sikasa: bot online")
	return nil
}

/*
Stop closes voice connections and the gateway. Always call this via defer
after Start().

	returns:
	      error: reserved for future error paths; currently always nil
*/
func (b *Bot) Stop() error {
	b.voicesMu.Lock()
	for _, vctx := range b.voices {
		_ = vctx.Stop()
		if vctx.conn != nil {
			vctx.conn.Close(context.TODO())
		}
	}
	b.voices = make(map[snowflake.ID]*VoiceCtx)
	b.voicesMu.Unlock()

	if b.client != nil {
		b.client.Close(context.TODO())
	}
	return nil
}

// syncCommands flushes registered command builders to Discord. handler.SyncCommands
// performs an atomic bulk overwrite, so stale commands from previous runs are
// removed automatically.
func (b *Bot) syncCommands() error {
	if len(b.cmds) == 0 {
		return nil
	}
	cmds := make([]discord.ApplicationCommandCreate, 0, len(b.cmds))
	for _, c := range b.cmds {
		cmds = append(cmds, c.build())
	}
	var guildIDs []snowflake.ID
	if b.guildID != 0 {
		guildIDs = []snowflake.ID{b.guildID}
	}
	if err := handler.SyncCommands(b.client, cmds, guildIDs); err != nil {
		return fmt.Errorf("sikasa: sync commands: %w", err)
	}
	b.logger.Printf("sikasa: registered %d command(s)", len(cmds))
	return nil
}

// dispatchMessage routes inbound MessageCreate events. Self-messages and bot
// messages are filtered first; then prefix dispatch claims the message if
// applicable, otherwise keyword matchers run. The two paths are mutually
// exclusive so a single user message cannot trigger both a prefix command and
// an overlapping keyword reply.
func (b *Bot) dispatchMessage(e *events.MessageCreate) {
	if e.Message.Author.Bot {
		return
	}
	if b.client != nil && e.Message.Author.ID == b.client.ID() {
		return
	}
	if b.dispatchPrefix(e) {
		return
	}
	ctx := newMsgCtx(b, e)
	for _, kw := range b.kws {
		if kw.matches(e.Message.Content) {
			if err := kw.fire(ctx); err != nil {
				b.logger.Printf("sikasa: keyword %v error: %v", kw.terms, err)
			}
		}
	}
}

// discardWriter is a no-op io.Writer used when the caller silences the logger.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// vlog returns a structured logger for sikasa's own internal events. If the
// caller installed one via WithSlog/WithVerbose, that logger is used; otherwise
// a discard handler keeps things quiet.
func (b *Bot) vlog() *slog.Logger {
	if b.slog != nil {
		return b.slog
	}
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{
		Level: slog.LevelError + 1, // effectively off
	}))
}

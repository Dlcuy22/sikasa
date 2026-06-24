# Sikasa

Sikasa (see-KAH-sah) is a high-level, fluent wrapper around the modern [disgoorg/disgo](https://github.com/disgoorg/disgo) library. It eliminates boilerplate and simplifies common bot-building tasks while keeping disgo's full power one method call away.

## Why Sikasa?

Writing a bot in raw `disgo` still involves repetitive boilerplate for things that should be simple:

- Wiring slash command definitions, the `handler.Router`, and the option parser separately.
- Building `discord.MessageCreate` chains for every reply variant.
- Manually constructing `MessageReference` for inline replies.
- The 15-line `os.Open` + `defer Close()` + `AddFile` dance just to send an image.

Sikasa solves this by providing a **Builder pattern**, context helpers (`CmdCtx` and `MsgCtx`), and automatic command registration on top of disgo.

## Features

- **Fluent Command Builder**: Define slash commands, their arguments, and their handler in one place.
- **Auto-Sync**: Atomically bulk-overwrites slash commands on startup via `handler.SyncCommands`.
- **Prefix Commands**: Classic text-prefix routing (`!play`, `!ping`) with the same builder feel as slash commands.
- **Keyword & Regex Router**: Easily respond to specific words or regex patterns in messages.
- **Context Helpers**: One-liners for replying with text, embeds, local files, or fetching files from URLs.
- **Voice / Music with DAVE**: Join voice channels and stream local files or YouTube URLs with a single call. End-to-end encryption is wired in for you.
- **Per-guild Queue**: Built-in track queue with auto-advance, skip, prev, and clear. Each guild gets its own session, so multi-server playback is isolated by default.
- **Rate Limiting**: Optional sliding-window rate limit on keyword replies.
- **Sentinel Errors**: Exported `Err*` values so callers can branch on `errors.Is(err, sikasa.ErrXxx)`.
- **Escape Hatches**: Drop down to disgo via `.Disgo()`, `.Event()`, or `.Data()` if Sikasa doesn't cover your specific need.
- **Battery-Included Core Services**: Bundles common Discord bot features like high-performance voice and music playback out-of-the-box, with more common bot utility features planned for future releases.

## Requirements

- Go 1.26+
- **Automated Dependency Installer**: For voice and music playback features, Sikasa automatically detects, downloads, and installs any missing external binaries (`ffmpeg` shared libraries, `yt-dlp`, and `bun` for decryption acceleration) into a local sandbox directory (`~/.sikasa/bin`) on first startup. You do not need to install them manually.
- `ffmpeg` on `PATH` (only if you want to bypass the automated installer with your own system-wide installation)
- `yt-dlp` on `PATH` (only if you want to bypass the automated installer with your own system-wide installation)

Install on common platforms:

```bash
# Linux (Debian/Ubuntu)
sudo apt install ffmpeg && pipx install yt-dlp

# macOS
brew install ffmpeg yt-dlp

# Windows
winget install Gyan.FFmpeg
winget install yt-dlp.yt-dlp
```

## Installation

```bash
go get github.com/dlcuy22/sikasa
```

## Quick Start

```go
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/dlcuy22/sikasa"
)

func main() {
	// 1. Initialize Bot
	bot, err := sikasa.New(os.Getenv("DISCORD_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	bot.WithIntents(sikasa.IntentsAll)

	// 2. Slash Command
	bot.Command("echo", "Echoes back the text you provide").
		StringArg("text", "The text to echo", true).
		Handle(func(ctx *sikasa.CmdCtx) error {
			return ctx.Reply(ctx.String("text"))
		})

	// 3. Keyword Detection
	bot.OnKeyword("hello", "hi").
		Reply(func(ctx *sikasa.MsgCtx) error {
			return ctx.Reply("Hello there, " + ctx.AuthorMention() + "!")
		})

	// 4. One-liner File Reply
	bot.OnKeyword("sikasa").
		ReplyFile("ongo", "media/pp.jpg")

	// 5. Start Bot
	if err := bot.Start(); err != nil {
		log.Fatal(err)
	}
	defer bot.Stop()

	log.Println("bot is running, ctrl-c to exit")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
    bot.Shutdown()
}
```

## Core Concepts

### Slash Commands (`CmdCtx`)

Sikasa provides `CmdCtx` which holds the interaction context.

```go
bot.Command("avatar", "Get user avatar").
    UserArg("target", "User to get avatar for", false).
    Handle(func(ctx *sikasa.CmdCtx) error {
        user := ctx.User("target")
        if user.ID == 0 {
            user = ctx.Author() // Fallback to invoker
        }
        return ctx.ReplyURL("", user.EffectiveAvatarURL(), "avatar.png")
    })
```

**Context Helpers:**

- `ctx.Reply("text")`
- `ctx.ReplyEphemeral("secret text")`
- `ctx.ReplyFile("text", "path.png")`
- `ctx.ReplyURL("text", "https://...", "image.png")`
- `ctx.Defer(ephemeral)` and `ctx.Followup("text")` for slow operations.

### Keyword & Regex Routing (`MsgCtx`)

Instead of writing massive `switch` chains on every `MessageCreate`, register rules:

```go
// Matches any message containing "help" (case-insensitive)
bot.OnKeyword("help").
    ReplyText("I am a simple bot. Try `/echo`")

// Matches complex patterns
bot.OnRegex(`(?i)^ping\s+\d+$`).
    Reply(func(ctx *sikasa.MsgCtx) error {
        return ctx.React("🏓")
    })
```

**Context Helpers:**

- `ctx.Reply("text")` (Sends as an inline reply to the user)
- `ctx.Send("text")` (Sends a normal message to the channel)
- `ctx.ReplyFile()`, `ctx.ReplyURL()`, `ctx.React("👍")`
- `ctx.NewEmbed()` returns a fluent `*EmbedBuilder`; pass it to `ctx.ReplyEmbed()` or `ctx.SendEmbed()` directly (no `.Build()` needed)

```go
bot.OnKeyword("status").Reply(func(ctx *sikasa.MsgCtx) error {
    embed := ctx.NewEmbed().
        Title("Status").
        Color(0x57F287).
        Description("All systems nominal").
        Field("Region", "us-west", true).
        Field("Latency", "42ms", true).
        Footer("updated just now", "").
        Now()
    return ctx.SendEmbed(embed)
})
```

The builder mirrors Discord's embed shape: `Title / Description / URL / Color / Author / Footer / Thumbnail / Image / Field / Timestamp`. For long content, clamp with `sikasa.Truncate(s, sikasa.EmbedDescriptionMaxLen)` (or `EmbedFieldValueMaxLen` etc.) so a single overflow does not 400 the whole reply.

### Prefix Commands (`PrefixCtx`)

For classic text-trigger commands (`!play`, `!ping`, `?help`), set a global prefix and register builders. The API mirrors `bot.Command`, so the muscle memory carries over.

```go
bot.SetPrefix("!")

bot.OnPrefix("ping", "Replies pong").
    Handle(func(ctx *sikasa.PrefixCtx) error {
        return ctx.Reply("pong")
    })

bot.OnPrefix("echo", "Echoes back text").
    Aliases("e", "say").
    StringArg("text", "text to echo", true).
    Handle(func(ctx *sikasa.PrefixCtx) error {
        return ctx.Reply(ctx.String("text"))
    })

bot.OnPrefix("add", "Adds two integers").
    IntArg("a", "first number", true).
    IntArg("b", "second number", true).
    Handle(func(ctx *sikasa.PrefixCtx) error {
        sum := ctx.Int("a") + ctx.Int("b")
        return ctx.Reply(strconv.FormatInt(sum, 10))
    })
```

**Behavior:**

- The last `StringArg` consumes the entire remaining message tail, so `!echo hello world` puts `"hello world"` into the `text` arg.
- Aliases dispatch to the same handler. `!e` and `!say` both run the `echo` builder.
- Command name lookup is case-insensitive (`!Echo` works); the prefix itself is case-sensitive.
- When a message starts with the prefix, **keyword matchers do not fire** for that message, preventing double-responses.
- Unknown commands reply with `sikasa: unknown prefix command: !xxx`. Missing required args reply with `sikasa: missing required argument: name`.
- `RequireSameVoice()` gates a command on the invoker being in the same voice channel as the bot. When the bot is not in any voice channel for the guild the gate passes (so initial-join commands still work). On rejection the user gets `sikasa: you must be in the same voice channel as the bot`.

```go
bot.OnPrefix("skip", "Skip to the next track").
    RequireSameVoice().
    Handle(func(ctx *sikasa.PrefixCtx) error {
        // only reachable when the user is in the bot's voice channel
        return nil
    })
```

**Hybrid Argument Access:**

Builder validation gives you typed access via `ctx.String/Int/Bool`. For free-form parsing (variadic args, custom syntax) drop to the raw escape hatches:

```go
bot.OnPrefix("tag", "Tag multiple users").
    Handle(func(ctx *sikasa.PrefixCtx) error {
        // ctx.Args() every whitespace-separated token after the command name
        // ctx.Arg(i) i-th token, or ""
        // ctx.Rest() message tail with original whitespace preserved
        // ctx.Name() canonical command name (alias-resolved)
        return ctx.Reply("tagged: " + strings.Join(ctx.Args(), ", "))
    })
```

### Voice & Music (`VoiceCtx`)

Join a voice channel and stream audio in a few lines. DAVE encryption is set up automatically.

```go
bot.Command("play", "Play a song").
    StringArg("file", "path to audio", true).
    Handle(func(ctx *sikasa.CmdCtx) error {
        guildID := ctx.Event().GuildID()
        if guildID == nil {
            return ctx.Reply("voice commands only work in a server")
        }
        state, ok := ctx.Bot().Disgo().Caches.VoiceState(*guildID, ctx.Author().ID)
        if !ok || state.ChannelID == nil {
            return ctx.Reply("you must be in a voice channel first")
        }

        vctx, err := ctx.Bot().Voice().Join(guildID.String(), state.ChannelID.String())
        if err != nil {
            return ctx.Reply("join error: " + err.Error())
        }

        // Auto-detects file type. .opus and .ogg use stream-copy
        // passthrough (zero CPU); other formats are transcoded via FFmpeg.
        return vctx.PlayFile(ctx.String("file"))
    })
```

YouTube playback (requires `yt-dlp`):

```go
vctx.PlayYouTube("https://youtu.be/dQw4w9WgXcQ")
```

Playlists, channels, and any other multi-entry yt-dlp URL are expanded automatically. A single `PlayYouTube` call appends every entry to the queue in order, so passing `youtube.com/playlist?list=...` enqueues the whole list at once.

Free-text search via `SearchYouTube(query, n)` returns top-N candidate Tracks (`ytsearch<n>:<query>` under the hood). Pair it with `BuildYTSearchEmbed` and `Bot.OnButton` to give users an interactive picker:

```go
// In your prefix handler:
results, _ := sikasa.SearchYouTube(query, 3)
sessionID := bot.NewYTSearchSession(invokerID, guildID, results)
embed, buttons := sikasa.BuildYTSearchEmbed(query, sessionID, results)
ctx.SendEmbedWithButtons(embed, buttons...)

// Wire the click handler once at startup:
bot.OnButton("/sikasa/ytsearch/{session}/{idx}", func(ctx *sikasa.ButtonCtx) error {
    s := ctx.Bot().YTSearchSession(ctx.Var("session"))
    if s == nil || ctx.Author().ID != s.InvokerID() {
        return ctx.Reply("not allowed")
    }
    // s.Tracks()[idx], join voice, PlayYouTube, etc.
    return nil
})
```

Sessions live in memory for 5 minutes; only the original invoker can click results.

```go
firstPos, added, started, err := vctx.PlayYouTube(playlistURL)
// added = number of tracks appended (1 for a single video)
// started = true if the first track is now playing
// firstPos = index of the first appended track
```

Each Track in the queue carries `Title` and `Author` (uploader/channel) populated from yt-dlp, so reach for `track.Label()` (`"Title by Author"`) when displaying the queue. This keeps replies clean and avoids Discord's auto-embed spam on raw URLs.

`PlayFile` enqueues a local file and returns `(pos, started, err)`. Tracks auto-advance on natural EOF, so a multi-track queue plays straight through without manual prompting.

```go
pos, started, err := vctx.PlayFile("song.mp3")
if err != nil { /* ... */ }
if started {
    fmt.Println("playing now")
} else {
    fmt.Printf("queued at #%d\n", pos+1)
}
```

Queue control:

```go
vctx.Skip()         // advance to the next track (alias: Next)
vctx.Prev()         // rewind cursor by one and play that track
vctx.Now()          // (Track, ok) currently loaded track
vctx.Queue()        // []Track snapshot of the entire list
vctx.Cursor()       // index of the current track, -1 if none started
vctx.ClearQueue()   // empty the queue (current track keeps playing)
```

Per-track control:

```go
vctx.Pause()
vctx.Resume()
vctx.Stop()    // halts current track; queue is preserved
vctx.Leave()   // disconnects entirely (clears queue too)
vctx.Reconnect() // tear down and reopen, then resume current track
```

The bot watches its own log stream for DAVE/voice errors (`"no active epoch"`, `"failed to encrypt packet"`) and triggers `Reconnect()` automatically with a 30-second per-guild debounce. The current track is restarted from the beginning because FFmpeg is torn down with the connection. Hook your own `slog` handler via `WithSlog` to see when this happens.

Multi-guild sessions are automatic: each guild gets its own `VoiceCtx` (and therefore its own queue, cursor, and FFmpeg pipeline), so the bot can play different music in two servers concurrently without any extra wiring.

Retrieve an existing connection from anywhere:

```go
vctx := bot.Voice().Get(guildID)  // nil if not connected
```

### Rate Limiting

Keyword replies accept an optional rate-limit config to silence spammers:

```go
bot.OnKeyword("sikasa").
    ReplyFile("ongo", "media/pp.jpg",
        sikasa.RateLimitInterval(1, 10*time.Second))
```

A given user can trigger this rule at most once per 10 seconds; extra messages are silently dropped.

## Errors

Sikasa exports sentinel errors so you can branch on failure modes without parsing strings. Wrapped errors (with extra context like the offending guild ID) preserve the chain, so `errors.Is` works on both bare and wrapped values.

```go
_, err := bot.Voice().Join("garbage", "ids")
if errors.Is(err, sikasa.ErrInvalidGuildID) {
    // safe to recover or show a friendlier message
}
```

| Sentinel              | Source                                                            |
| --------------------- | ----------------------------------------------------------------- |
| `ErrEmptyToken`       | `sikasa.New("")`                                                  |
| `ErrBotNotStarted`    | Voice operations called before `bot.Start()`                      |
| `ErrInvalidGuildID`   | Guild snowflake fails to parse                                    |
| `ErrInvalidChannelID` | Channel snowflake fails to parse                                  |
| `ErrNotInVoice`       | Voice operation requires an active connection                     |
| `ErrNoAudio`          | `Pause()` called while nothing is playing                         |
| `ErrNotPaused`        | `Resume()` called outside the paused state                        |
| `ErrUnknownCommand`   | Prefix dispatch finds no matching command                         |
| `ErrMissingArg`       | Required builder argument absent from the message                 |
| `ErrInvalidArg`       | Argument value fails type parsing (e.g. `IntArg` got non-numeric) |
| `ErrQueueEmpty`       | Queue navigation called on an empty queue                         |
| `ErrNoPrevious`       | `Prev()` called while the cursor is already at the first track    |
| `ErrNotSameChannel`   | `RequireSameVoice()` gate rejected the invoker                    |

## Escape Hatches

When Sikasa doesn't cover something, drop down to disgo directly:

```go
client := bot.Disgo()                    // *bot.Client (disgo)
event  := ctx.Event()                    // *handler.CommandEvent
data   := ctx.Data()                     // discord.SlashCommandInteractionData
voice  := vctx.Disgo()                   // voice.Conn

// e.g. send a custom embed via the raw REST surface
client.Rest.CreateMessage(channelID, discord.NewMessageCreate().AddEmbeds(myEmbed))
```

## Roadmap / TODO

Voice features deferred from this iteration:

- Real-time **volume** control (currently fixed at 100%; would need PCM-side gain or FFmpeg restart)
- **AudioProvider** chaining for custom audio sources (Spotify, custom TTS, raw PCM)
- **Voice receive** (decoding user audio; disgo exposes `OpusFrameReceiver` but Sikasa hasn't surfaced it yet)
- **Lavalink** mode for production deployments at scale

Now that disgo is the underlying library, the following also become easy follow-ons:

- **Components** (buttons, select menus) via disgo's handler router
- **Modals** with the same path-pattern API
- **Threads, polls, business connections** that disgo supports natively

# Sikasa

Sikasa (see-KAH-sah) is a high-level, fluent wrapper around the modern [disgoorg/disgo](https://github.com/disgoorg/disgo) library. It eliminates boilerplate and simplifies common bot-building tasks while keeping disgo's full power one method call away.

## Why Sikasa?

Writing a bot in raw `disgo` still involves repetitive boilerplate for things that should be simple:
- Wiring slash command definitions, the `handler.Router`, and the option parser separately.
- Building `discord.MessageCreate` chains for every reply variant.
- Manually constructing `MessageReference` for inline replies.
- The 15-line `os.Open` + `defer Close()` + `AddFile` dance just to send an image.

Sikasa solves this by providing a **Builder pattern**, context helpers (`CmdCtx` and `MsgCtx`), and automatic command registration on top of disgo.

> **Migration note:** Sikasa originally wrapped `bwmarrin/discordgo`. It has been rewritten on top of disgo because Discord now enforces the **DAVE (E2EE)** protocol in many voice regions, which discordgo does not support (close code 4017). disgo ships native DAVE support via [thomas-vilte/dave-go](https://github.com/thomas-vilte/dave-go). The escape hatch was renamed from `.DiscordGo()` to `.Disgo()` and now returns a `*bot.Client`. Everything else in the public API is unchanged.

## Features

- **Fluent Command Builder**: Define slash commands, their arguments, and their handler in one place.
- **Auto-Sync**: Atomically bulk-overwrites slash commands on startup via `handler.SyncCommands`.
- **Keyword & Regex Router**: Easily respond to specific words or regex patterns in messages.
- **Context Helpers**: One-liners for replying with text, embeds, local files, or fetching files from URLs.
- **Voice / Music with DAVE**: Join voice channels and stream local files or YouTube URLs with a single call. End-to-end encryption is wired in for you.
- **Rate Limiting**: Optional sliding-window rate limit on keyword replies.
- **Escape Hatches**: Drop down to disgo via `.Disgo()`, `.Event()`, or `.Data()` if Sikasa doesn't cover your specific need.

## Requirements

- Go 1.22+
- `ffmpeg` on `PATH` (only required for `PlayFile` / `PlayYouTube`)
- `yt-dlp` on `PATH` (only required for `PlayYouTube`)

DAVE (E2EE voice) is built in via the pure-Go [dave-go](https://github.com/thomas-vilte/dave-go) backend; no extra setup needed.

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

Control:
```go
vctx.Pause()
vctx.Resume()
vctx.Stop()    // halts current track, voice connection stays
vctx.Leave()   // disconnects entirely
```

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
- **Queue / playlist** management
- **AudioProvider** chaining for custom audio sources (Spotify, custom TTS, raw PCM)
- **Voice receive** (decoding user audio; disgo exposes `OpusFrameReceiver` but Sikasa hasn't surfaced it yet)
- **Lavalink** mode for production deployments at scale

Now that disgo is the underlying library, the following also become easy follow-ons:
- **Components** (buttons, select menus) via disgo's handler router
- **Modals** with the same path-pattern API
- **Threads, polls, business connections** that disgo supports natively

## License
MIT

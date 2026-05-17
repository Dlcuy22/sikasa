# Sikasa

[![Go Reference](https://pkg.go.dev/badge/github.com/dlcuy22/sikasa.svg)](https://pkg.go.dev/github.com/dlcuy22/sikasa)

Sikasa is a high-level, fluent wrapper around the popular [bwmarrin/discordgo](https://github.com/bwmarrin/discordgo) library. It eliminates boilerplate and simplifies common bot-building tasks without hiding the underlying power of `discordgo`.

## Why Sikasa?

Writing a bot in raw `discordgo` often involves repetitive boilerplate for things that should be simple:
- Splitting slash command definitions from their interaction handlers.
- Manually parsing options array into correct types.
- Manually constructing `MessageReference` for inline replies.
- The 15-line `os.Open` + `defer Close()` + `[]*discordgo.File{}` dance just to send an image.

Sikasa solves this by providing a **Router/Builder pattern**, context helpers (`CmdCtx` and `MsgCtx`), and automatic command registration.

## Features

- **Fluent Command Builder**: Define slash commands, their arguments, and their handler in one place.
- **Auto-Sync**: Automatically registers and bulk-overwrites slash commands on startup.
- **Keyword & Regex Router**: Easily respond to specific words or regex patterns in messages.
- **Context Helpers**: One-liners for replying with text, embeds, local files, or fetching files from URLs.
- **Escape Hatches**: You can always call `.Session()`, `.Interaction()`, or `.Message()` to drop down to raw `discordgo` if Sikasa doesn't cover your specific need.

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
			// Easy argument retrieval and replying
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
        if user == nil {
            user = ctx.Author() // Fallback to invoker
        }
        return ctx.ReplyURL("", user.AvatarURL(""), "avatar.png")
    })
```

**Context Helpers:**
- `ctx.Reply("text")`
- `ctx.ReplyEphemeral("secret text")`
- `ctx.ReplyFile("text", "path.png")`
- `ctx.ReplyURL("text", "https://...", "image.png")`
- `ctx.Defer(ephemeral)` and `ctx.Followup("text")` for slow operations.

### Keyword & Regex Routing (`MsgCtx`)

Instead of writing massive `if-else` chains in a single `MessageCreate` handler, register rules:

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

## License
MIT

// example/main.go
// Purpose: Demonstrates how to build a Discord bot with the sikasa wrapper.
// Implements /echo and /joke slash commands plus several keyword matchers.
//
// Key Components:
//   - main(): wires up commands and keywords, then starts the bot
//
// Dependencies:
//   - github.com/dlcuy22/sikasa: the high-level wrapper
//   - github.com/joho/godotenv: loads DISCORD_TOKEN from .env.dev
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/dlcuy22/sikasa"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(".env.dev"); err != nil {
		log.Println("no .env.dev found, falling back to environment")
	}

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN is not set")
	}

	bot, err := sikasa.New(token)
	if err != nil {
		log.Fatalf("create bot: %v", err)
	}
	bot.WithIntents(sikasa.IntentsAll)

	bot.Command("echo", "Echoes back the text you provide").
		StringArg("text", "The text to echo", true).
		Handle(func(ctx *sikasa.CmdCtx) error {
			return ctx.Reply(ctx.String("text"))
		})

	bot.Command("joke", "Tells a programming joke").
		Handle(func(ctx *sikasa.CmdCtx) error {
			return ctx.Reply("Why do programmers prefer dark mode?\n\nBecause light attracts bugs!")
		})

	bot.OnKeyword("hello", "hi").
		Reply(func(ctx *sikasa.MsgCtx) error {
			return ctx.Reply("Hello there, " + ctx.AuthorMention() + "!")
		})

	bot.OnKeyword("help").
		ReplyText("I am a simple bot. Try `/echo` or `/joke`!")

	bot.OnKeyword("sikasa").
		ReplyFile("ongo", "media/pp.jpg")

	if err := bot.Start(); err != nil {
		log.Fatalf("start bot: %v", err)
	}
	defer bot.Stop()

	log.Println("bot is running, ctrl-c to exit")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}

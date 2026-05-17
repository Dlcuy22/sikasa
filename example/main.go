// example/main.go
// Purpose: Demonstrates how to build a Discord bot with the sikasa wrapper.
// Implements slash commands, keyword matchers, and voice/music playback.
//
// Key Components:
//   - main(): wires up commands and keywords, then starts the bot
//
// Dependencies:
//   - github.com/dlcuy22/sikasa: the high-level wrapper
//   - github.com/joho/godotenv:  loads DISCORD_TOKEN from .env.dev
//
// Note: /kplay and /kyt require ffmpeg in PATH; /kyt also requires yt-dlp.
package main

import (
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	bot.WithIntents(sikasa.IntentsAll).
		WithGuild("1434105028863590565").
		WithVerbose() // turns on slog debug for gateway, voice, REST

	bot.Command("echo", "Echoes back the text you provide").
		StringArg("text", "The text to echo", true).
		Handle(func(ctx *sikasa.CmdCtx) error {
			return ctx.Reply(ctx.String("text"))
		})

	bot.Command("joke", "Tells a programming joke").
		Handle(func(ctx *sikasa.CmdCtx) error {
			return ctx.Reply("Why do programmers prefer dark mode?\n\nBecause light attracts bugs!")
		})

	// Music commands. The bot joins whichever voice channel the invoker is in.
	// Voice handlers Defer immediately because Voice().Join() blocks waiting
	// for VoiceServerUpdate from the gateway, which can exceed Discord's 3s
	// interaction ack window.
	bot.Command("kplay", "Plays a local audio file from the example/ directory").
		StringArg("file", "Path to the audio file (e.g. media/song.mp3)", true).
		Handle(func(ctx *sikasa.CmdCtx) error {
			if err := ctx.Defer(false); err != nil {
				return err
			}
			vctx, err := joinAuthorVoice(ctx)
			if err != nil {
				return ctx.Followup(err.Error())
			}
			path := ctx.String("file")
			if err := vctx.PlayFile(path); err != nil {
				return ctx.Followup("play error: " + err.Error())
			}
			return ctx.Followup("now playing: `" + path + "`")
		})

	bot.Command("kyt", "Plays audio from a YouTube URL via yt-dlp").
		StringArg("url", "YouTube URL", true).
		Handle(func(ctx *sikasa.CmdCtx) error {
			if err := ctx.Defer(false); err != nil {
				return err
			}
			vctx, err := joinAuthorVoice(ctx)
			if err != nil {
				return ctx.Followup(err.Error())
			}
			url := ctx.String("url")
			if err := vctx.PlayYouTube(url); err != nil {
				return ctx.Followup("play error: " + err.Error())
			}
			return ctx.Followup("now streaming: " + url)
		})

	bot.Command("kpause", "Pauses the current audio").
		Handle(func(ctx *sikasa.CmdCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.ReplyEphemeral("not in a voice channel")
			}
			if err := vctx.Pause(); err != nil {
				return ctx.ReplyEphemeral(err.Error())
			}
			return ctx.Reply("paused")
		})

	bot.Command("kresume", "Resumes the paused audio").
		Handle(func(ctx *sikasa.CmdCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.ReplyEphemeral("not in a voice channel")
			}
			if err := vctx.Resume(); err != nil {
				return ctx.ReplyEphemeral(err.Error())
			}
			return ctx.Reply("resumed")
		})

	bot.Command("kstop", "Stops playback and leaves the voice channel").
		Handle(func(ctx *sikasa.CmdCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.ReplyEphemeral("not in a voice channel")
			}
			_ = vctx.Leave()
			return ctx.Reply("disconnected")
		})

	// Keyword examples
	bot.OnKeyword("hello", "hi").
		Reply(func(ctx *sikasa.MsgCtx) error {
			return ctx.Reply("Hello there, " + ctx.AuthorMention() + "!")
		})

	bot.OnKeyword("help").
		ReplyText("Try `/echo`, `/joke`, `/kplay`, `/kyt`, `/kpause`, `/kresume`, or `/kstop`")

	bot.OnKeyword("sikasa").
		ReplyFile("ongo", "media/pp.jpg", sikasa.RateLimitInterval(1, 10*time.Second))

	if err := bot.Start(); err != nil {
		log.Fatalf("start bot: %v", err)
	}
	defer bot.Stop()

	log.Println("bot is running, ctrl-c to exit")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}

/*
joinAuthorVoice locates the voice channel the command invoker is currently in
and joins it (or moves to it if already connected elsewhere in the guild).

	params:
	      ctx: the slash command context
	returns:
	      *sikasa.VoiceCtx: live voice handle ready for playback
	      error:            if the user is not in a voice channel
*/
func joinAuthorVoice(ctx *sikasa.CmdCtx) (*sikasa.VoiceCtx, error) {
	guildID := ctx.GuildID()
	if guildID == "" {
		return nil, errors.New("voice commands only work in a server")
	}
	gid := ctx.Event().GuildID()
	if gid == nil {
		return nil, errors.New("voice commands only work in a server")
	}
	state, ok := ctx.Bot().Disgo().Caches.VoiceState(*gid, ctx.Author().ID)
	if !ok || state.ChannelID == nil {
		return nil, errors.New("you must be in a voice channel first")
	}
	return ctx.Bot().Voice().Join(guildID, state.ChannelID.String())
}

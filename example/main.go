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
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
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
		SetPrefix("k!")
		// WithVerbose() // turns on slog debug for gateway, voice, REST

	bot.Command("echo", "Echoes back the text you provide").
		StringArg("text", "The text to echo", true).
		Handle(func(ctx *sikasa.CmdCtx) error {
			return ctx.Reply(ctx.String("text"))
		})

	bot.Command("joke", "Tells a programming joke").
		Handle(func(ctx *sikasa.CmdCtx) error {
			return ctx.Reply("Why do programmers prefer dark mode?\n\nBecause light attracts bugs!")
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

	bot.OnKeyword("karbit").
		ReplyFile(" ", "media/karbit.jpg")

	// Prefix command examples. Triggered by the prefix set via SetPrefix("k!").
	// !ping        -> "pong"
	// !echo X Y Z  -> "X Y Z" (last StringArg consumes the tail)
	// !add 2 3     -> "5"
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

	bot.OnPrefix("add", "Adds two integers or decimals").
		StringArg("a", "first number", true).
		StringArg("b", "second number", true).
		Handle(func(ctx *sikasa.PrefixCtx) error {
			a, errA := parseNumber(ctx.String("a"))
			b, errB := parseNumber(ctx.String("b"))
			if errA != nil || errB != nil {
				return ctx.Reply("invalid number format")
			}

			result := a + b
			formatted := strconv.FormatFloat(result, 'f', 10, 64)
			formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
			return ctx.Reply(formatted)
		})

	// Voice commands via prefix
	bot.OnPrefix("play", "Plays a local audio file").
		Aliases("p").
		StringArg("file", "path to audio file", true).
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx, err := joinAuthorVoicePrefix(ctx)
			if err != nil {
				return ctx.Reply(err.Error())
			}
			path := ctx.String("file")
			if err := vctx.PlayFile(path); err != nil {
				return ctx.Reply("play error: " + err.Error())
			}
			return ctx.Reply("now playing: `" + path + "`")
		})

	bot.OnPrefix("yt", "Plays audio from a YouTube URL").
		Aliases("youtube").
		StringArg("url", "YouTube URL", true).
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx, err := joinAuthorVoicePrefix(ctx)
			if err != nil {
				return ctx.Reply(err.Error())
			}
			url := ctx.String("url")
			if err := vctx.PlayYouTube(url); err != nil {
				return ctx.Reply("play error: " + err.Error())
			}
			return ctx.Reply("now streaming: " + url)
		})

	bot.OnPrefix("pause", "Pauses the current audio").
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.Reply("not in a voice channel")
			}
			if err := vctx.Pause(); err != nil {
				return ctx.Reply(err.Error())
			}
			return ctx.Reply("paused")
		})

	bot.OnPrefix("resume", "Resumes the paused audio").
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.Reply("not in a voice channel")
			}
			if err := vctx.Resume(); err != nil {
				return ctx.Reply(err.Error())
			}
			return ctx.Reply("resumed")
		})

	bot.OnPrefix("stop", "Stops playback and leaves the voice channel").
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.Reply("not in a voice channel")
			}
			_ = vctx.Leave()
			return ctx.Reply("disconnected")
		})

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

/*
joinAuthorVoicePrefix locates the voice channel the prefix command invoker is
currently in and joins it (or moves to it if already connected elsewhere).

	params:
	      ctx: the prefix command context
	returns:
	      *sikasa.VoiceCtx: live voice handle ready for playback
	      error:            if the user is not in a voice channel
*/
func joinAuthorVoicePrefix(ctx *sikasa.PrefixCtx) (*sikasa.VoiceCtx, error) {
	guildID := ctx.GuildID()
	if guildID == "" {
		return nil, errors.New("voice commands only work in a server")
	}
	event := ctx.Event()
	if event.GuildID == nil {
		return nil, errors.New("voice commands only work in a server")
	}
	state, ok := ctx.Bot().Disgo().Caches.VoiceState(*event.GuildID, ctx.Author().ID)
	if !ok || state.ChannelID == nil {
		return nil, errors.New("you must be in a voice channel first")
	}
	return ctx.Bot().Voice().Join(guildID, state.ChannelID.String())
}

var constants = map[string]float64{
	"pi":    math.Pi,
	"π":     math.Pi,
	"e":     math.E,
	"ℯ":     math.E,
	"phi":   1.6180339887,
	"φ":     1.6180339887,
	"√2":    math.Sqrt2,
	"sqrt2": math.Sqrt2,
	"ln2":   math.Ln2,
	"㏑2":    math.Ln2,
	"ln10":  math.Log(10),
	"∞":     math.Inf(1),
	"inf":   math.Inf(1),
}

func parseNumber(s string) (float64, error) {
	s = strings.ReplaceAll(s, ",", ".")
	lower := strings.ToLower(s)

	if val, ok := constants[lower]; ok {
		return val, nil
	}

	if parts := strings.SplitN(s, "^", 2); len(parts) == 2 {
		base, errA := parseNumber(parts[0]) // rekursif, support pi^2
		exp, errB := parseNumber(parts[1])
		if errA != nil || errB != nil {
			return 0, fmt.Errorf("invalid power format")
		}
		return math.Pow(base, exp), nil
	}

	return strconv.ParseFloat(s, 64)
}

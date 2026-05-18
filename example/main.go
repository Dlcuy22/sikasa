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

	"github.com/disgoorg/snowflake/v2"
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

	bot.OnKeyword("dongo").ReplyText("Apasih 😡")

	// Prefix command examples. Triggered by the prefix set via SetPrefix("k!").
	// k!ping        -> "pong"
	// k!echo X Y Z  -> "X Y Z" (last StringArg consumes the tail)
	// k!add 2 3     -> "5"
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
	bot.OnPrefix("play", "Plays or queues a local audio file").
		Aliases("p").
		StringArg("file", "path to audio file", true).
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx, err := joinAuthorVoicePrefix(ctx)
			if err != nil {
				return ctx.Reply(err.Error())
			}
			path := ctx.String("file")
			pos, started, err := vctx.PlayFile(path)
			if err != nil {
				return ctx.Reply("play error: " + err.Error())
			}
			if started {
				return ctx.Reply("now playing: `" + path + "`")
			}
			return ctx.Reply(fmt.Sprintf("queued at #%d: `%s`", pos+1, path))
		})

	bot.OnPrefix("yt", "Plays or queues a YouTube URL, playlist, or search query").
		Aliases("youtube").
		IntArg("results", "number of search results to show (default 3, max 5)", false).
		StringArg("query", "URL or search query", true).
		Handle(func(ctx *sikasa.PrefixCtx) error {
			return runYouTubePrefix(ctx, sikasa.YTSearchModeEnqueue)
		})

	bot.OnPrefix("insert", "Insert a YouTube URL or search result right after the current track").
		Aliases("ins", "playnext").
		IntArg("results", "number of search results to show (default 3, max 5)", false).
		StringArg("query", "URL or search query", true).
		Handle(func(ctx *sikasa.PrefixCtx) error {
			return runYouTubePrefix(ctx, sikasa.YTSearchModeInsertNext)
		})

	bot.OnButton("/sikasa/ytsearch/{session}/{idx}", func(ctx *sikasa.ButtonCtx) error {
		return handleYTSearchClick(ctx)
	})

	bot.OnPrefix("skip", "Skip to the next track or jump to a queue position").
		Aliases("next", "n").
		StringArg("position", "1-based queue position to jump to (optional)", false).
		RequireSameVoice().
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.Reply("not in a voice channel")
			}
			if pos := strings.TrimSpace(ctx.String("position")); pos != "" {
				n, err := strconv.Atoi(pos)
				if err != nil || n < 1 {
					return ctx.Reply("position must be a positive number")
				}
				t, err := vctx.JumpTo(n - 1)
				if err != nil {
					return ctx.Reply(err.Error())
				}
				return ctx.Reply(fmt.Sprintf("jumped to #%d: %s", n, t.Label()))
			}
			t, ok, err := vctx.Skip()
			if err != nil {
				return ctx.Reply("skip error: " + err.Error())
			}
			if !ok {
				return ctx.Reply("queue exhausted")
			}
			return ctx.Reply("skipped to: " + t.Label())
		})

	bot.OnPrefix("prev", "Replay the previous track or jump back to a position").
		Aliases("previous", "back").
		StringArg("position", "1-based queue position to jump to (optional)", false).
		RequireSameVoice().
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.Reply("not in a voice channel")
			}
			if pos := strings.TrimSpace(ctx.String("position")); pos != "" {
				n, err := strconv.Atoi(pos)
				if err != nil || n < 1 {
					return ctx.Reply("position must be a positive number")
				}
				t, err := vctx.JumpTo(n - 1)
				if err != nil {
					return ctx.Reply(err.Error())
				}
				return ctx.Reply(fmt.Sprintf("jumped to #%d: %s", n, t.Label()))
			}
			t, err := vctx.Prev()
			if err != nil {
				return ctx.Reply(err.Error())
			}
			return ctx.Reply("rewound to: " + t.Label())
		})

	bot.OnPrefix("queue", "Show the current queue").
		Aliases("q", "list").
		StringArg("page", "1-based page number (10 tracks per page)", false).
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.Reply("not in a voice channel")
			}
			tracks := vctx.Queue()
			if len(tracks) == 0 {
				return ctx.Reply("queue is empty")
			}
			cursor := vctx.Cursor()

			const perPage = 10
			pages := (len(tracks) + perPage - 1) / perPage

			page := 1
			if cursor >= 0 {
				page = cursor/perPage + 1
			}
			if raw := strings.TrimSpace(ctx.String("page")); raw != "" {
				n, err := strconv.Atoi(raw)
				if err != nil || n < 1 || n > pages {
					return ctx.Reply(fmt.Sprintf("page must be between 1 and %d", pages))
				}
				page = n
			}

			start := (page - 1) * perPage
			end := min(start+perPage, len(tracks))

			var body strings.Builder
			for i := start; i < end; i++ {
				marker := "  "
				if i == cursor {
					marker = "▶ "
				}
				fmt.Fprintf(&body, "%s`%d.` %s\n", marker, i+1, sikasa.Truncate(tracks[i].Label(), 100))
			}

			embed := ctx.NewEmbed().
				Title("Queue").
				Color(0x5865F2).
				Description(sikasa.Truncate(body.String(), sikasa.EmbedDescriptionMaxLen)).
				Footer(fmt.Sprintf("Page %d of %d • %d tracks total", page, pages, len(tracks)), "")
			if now, ok := vctx.Now(); ok {
				embed.Field("Now Playing", sikasa.Truncate(now.Label(), sikasa.EmbedFieldValueMaxLen), false)
			}
			return ctx.SendEmbed(embed)
		})

	bot.OnPrefix("nowplaying", "Show the current track").
		Aliases("np", "now").
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.Reply("not in a voice channel")
			}
			t, ok := vctx.Now()
			if !ok {
				return ctx.Reply("nothing playing")
			}
			embed := ctx.NewEmbed().
				Title("Now Playing").
				Color(0x57F287).
				Description(sikasa.Truncate(t.Label(), sikasa.EmbedDescriptionMaxLen))
			if t.Author != "" {
				embed.Field("Artist", t.Author, true)
			}
			if pos := vctx.Cursor(); pos >= 0 {
				embed.Field("Position", fmt.Sprintf("#%d of %d", pos+1, len(vctx.Queue())), true)
			}
			return ctx.SendEmbed(embed)
		})

	bot.OnPrefix("clearqueue", "Clear the queue (keeps the current track)").
		Aliases("cq").
		RequireSameVoice().
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.Reply("not in a voice channel")
			}
			vctx.ClearQueue()
			return ctx.Reply("queue cleared")
		})

	bot.OnPrefix("stop", "Stop playback (queue is preserved)").
		RequireSameVoice().
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.Reply("not in a voice channel")
			}
			_ = vctx.Stop()
			return ctx.Reply("stopped")
		})

	bot.OnPrefix("pause", "Pauses the current audio").
		RequireSameVoice().
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
		RequireSameVoice().
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

	bot.OnPrefix("leave", "Stops playback and leaves the voice channel").
		RequireSameVoice().
		Handle(func(ctx *sikasa.PrefixCtx) error {
			vctx := ctx.Bot().Voice().Get(ctx.GuildID())
			if vctx == nil {
				return ctx.Reply("not in a voice channel")
			}
			err = vctx.Leave()
			if err != nil {
				return ctx.Reply("leave error: " + err.Error())
			}
			return ctx.ReplyFile(" ", "media/akupergi.jpg")
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
	vctx, err := ctx.Bot().Voice().Join(guildID, state.ChannelID.String())
	if err != nil {
		return nil, err
	}
	// Route auto-advance announcements ("next track: ...", "no more tracks")
	// back to the text channel that requested playback.
	vctx.SetAnnounceChannel(ctx.ChannelID())
	return vctx, nil
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

/*
runYouTubePrefix is the shared body of k!yt and k!insert. It branches on
whether the user passed a URL or a free-text query and on the requested
mode (enqueue vs insert-next), so both prefix commands share one
implementation.

	params:
	      ctx:  prefix command context
	      mode: enqueue (k!yt) or insert-next (k!insert)
	returns:
	      error: from voice ops or yt-dlp
*/
func runYouTubePrefix(ctx *sikasa.PrefixCtx, mode sikasa.YTSearchMode) error {
	vctx, err := joinAuthorVoicePrefix(ctx)
	if err != nil {
		return ctx.Reply(err.Error())
	}
	query := ctx.String("query")
	if sikasa.IsHTTPURL(query) {
		return playYouTubeURL(ctx, vctx, query, mode)
	}
	n := int(ctx.Int("results"))
	if n <= 0 {
		n = 3
	}
	if n > 5 {
		n = 5
	}
	return showYouTubeSearch(ctx, query, n, mode)
}

/*
playYouTubeURL routes a direct URL through PlayYouTube or InsertNextYouTube
depending on mode and replies with the appropriate "queued"/"now streaming"/
"inserted" line.

	params:
	      ctx:  prefix command context
	      vctx: live VoiceCtx for the guild
	      url:  YouTube URL to play
	      mode: enqueue (append) or insert-next (after current)
	returns:
	      error: from the underlying voice op or Reply
*/
func playYouTubeURL(ctx *sikasa.PrefixCtx, vctx *sikasa.VoiceCtx, url string, mode sikasa.YTSearchMode) error {
	var (
		firstPos, added int
		started         bool
		err             error
	)
	switch mode {
	case sikasa.YTSearchModeInsertNext:
		firstPos, added, started, err = vctx.InsertNextYouTube(url)
	default:
		firstPos, added, started, err = vctx.PlayYouTube(url)
	}
	if err != nil {
		return ctx.Reply("play error: " + err.Error())
	}
	tracks := vctx.Queue()
	firstLabel := url
	if firstPos < len(tracks) {
		firstLabel = tracks[firstPos].Label()
	}
	switch {
	case started:
		return ctx.Reply("now streaming: " + firstLabel)
	case mode == sikasa.YTSearchModeInsertNext && added > 1:
		return ctx.Reply(fmt.Sprintf("inserted %d tracks starting at #%d: %s", added, firstPos+1, firstLabel))
	case mode == sikasa.YTSearchModeInsertNext:
		return ctx.Reply(fmt.Sprintf("inserted at #%d: %s", firstPos+1, firstLabel))
	case added > 1:
		return ctx.Reply(fmt.Sprintf("queued %d tracks starting at #%d: %s", added, firstPos+1, firstLabel))
	default:
		return ctx.Reply(fmt.Sprintf("queued at #%d: %s", firstPos+1, firstLabel))
	}
}

/*
showYouTubeSearch runs SearchYouTube, registers a session in the requested
mode, and posts the picker embed plus a button row. The session ties this
picker to the invoker so handleYTSearchClick can refuse strangers.

	params:
	      ctx:   prefix command context
	      query: free-text search string
	      n:     number of results to show
	      mode:  enqueue or insert-next, propagated to the click handler
	returns:
	      error: from yt-dlp or from posting the embed
*/
func showYouTubeSearch(ctx *sikasa.PrefixCtx, query string, n int, mode sikasa.YTSearchMode) error {
	results, err := sikasa.SearchYouTube(query, n)
	if err != nil {
		return ctx.Reply("search error: " + err.Error())
	}
	if len(results) == 0 {
		return ctx.Reply("no results for: " + query)
	}

	guildID, err := snowflake.Parse(ctx.GuildID())
	if err != nil {
		return ctx.Reply("voice commands only work in a server")
	}
	sessionID := ctx.Bot().NewYTSearchSessionMode(ctx.Author().ID, guildID, results, mode)

	embed, buttons := sikasa.BuildYTSearchEmbed(query, sessionID, results)
	return ctx.SendEmbedWithButtons(embed, buttons...)
}

/*
handleYTSearchClick processes a click on a search-result button. Validates
that the clicker is the invoker, joins the user's voice channel if needed,
and either enqueues or insert-nexts the chosen track based on the
session's stored mode. Cancel buttons just clear the picker.

	params:
	      ctx: button interaction context
	returns:
	      error: from the underlying voice operations
*/
func handleYTSearchClick(ctx *sikasa.ButtonCtx) error {
	sessionID := ctx.Var("session")
	choice := ctx.Var("idx")

	session := ctx.Bot().YTSearchSession(sessionID)
	if session == nil {
		return ctx.Reply("this picker has expired")
	}
	if ctx.Author().ID != session.InvokerID() {
		return ctx.Reply("only the requester can pick a result")
	}

	if choice == "cancel" {
		ctx.Bot().ConsumeYTSearch(sessionID)
		return ctx.UpdateEmbed(sikasa.NewEmbed().
			Title("Search cancelled").
			Color(0x99AAB5))
	}

	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 0 || idx >= len(session.Tracks()) {
		return ctx.Reply("invalid choice")
	}
	track := session.Tracks()[idx]

	state, ok := ctx.Bot().Disgo().Caches.VoiceState(session.GuildID(), session.InvokerID())
	if !ok || state.ChannelID == nil {
		return ctx.Reply("you must be in a voice channel first")
	}
	vctx, err := ctx.Bot().Voice().Join(session.GuildID().String(), state.ChannelID.String())
	if err != nil {
		return ctx.Reply("join error: " + err.Error())
	}
	vctx.SetAnnounceChannel(ctx.ChannelID())

	mode := session.Mode()
	ctx.Bot().ConsumeYTSearch(sessionID)

	var (
		pos     int
		started bool
	)
	switch mode {
	case sikasa.YTSearchModeInsertNext:
		pos, started, err = vctx.InsertNext(track)
	default:
		pos, _, started, err = vctx.PlayYouTube(track.Source)
	}
	if err != nil {
		return ctx.Reply("play error: " + err.Error())
	}

	embed := sikasa.NewEmbed().
		Color(0x57F287)
	switch {
	case started:
		embed.Title("Now Playing").Description(track.Label())
	case mode == sikasa.YTSearchModeInsertNext:
		embed.Title(fmt.Sprintf("Inserted at #%d", pos+1)).Description(track.Label())
	default:
		embed.Title(fmt.Sprintf("Queued at #%d", pos+1)).Description(track.Label())
	}
	return ctx.UpdateEmbed(embed)
}

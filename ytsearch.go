// Package sikasa: ytsearch.go
// Purpose: Holds the per-bot YouTube search session map. Each session
// captures the candidate tracks shown to one user via an interactive picker
// and the metadata (invoker, expiry) needed to authorize a button click.
// Bound to the bot lifetime; sessions auto-expire after ytSearchTTL.
//
// Key Components:
//   - ytSearchSession:        per-picker state: candidates, invoker, expiry
//   - Bot.NewYTSearchSession: registers a fresh session and returns its id
//   - Bot.YTSearchSession:    looks up an active session by id
//   - Bot.consumeYTSearch:    atomic lookup-and-delete for click handlers
//   - BuildYTSearchEmbed:     renders the picker embed and button row
//
// Note: customID layout: "sikasa:ytsearch:<sessionID>:<idx>". The session
// id is a 12-char base32 random string; collisions are vanishingly small
// for the active window. Sessions live in memory only; restarting the bot
// invalidates outstanding pickers (acceptable trade since the parent
// message visually loses its interactive state on restart anyway).
package sikasa

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// ytSearchTTL bounds how long a picker stays clickable after creation.
// Five minutes lines up with Discord's component interaction window for
// non-deferred replies and keeps memory use bounded if a user walks away.
const ytSearchTTL = 5 * time.Minute

// ytSearchCustomIDPrefix is the namespaced prefix used by the picker buttons.
// Disgo's handler.Mux requires patterns to start with "/" and uses "/" as
// the path separator, so we mirror that in the customID.
const ytSearchCustomIDPrefix = "/sikasa/ytsearch"

// YTSearchMode tags how the picker should hand off to the voice queue when a
// candidate is selected. Embedded in the session so a single OnButton route
// can drive both "enqueue" (k!yt) and "insert next" (k!insert) flows
// without separate customID namespaces.
type YTSearchMode int

const (
	// YTSearchModeEnqueue appends the chosen track to the queue tail.
	YTSearchModeEnqueue YTSearchMode = iota
	// YTSearchModeInsertNext inserts the chosen track right after the
	// currently playing one, shifting later tracks down.
	YTSearchModeInsertNext
)

// ytSearchSession captures one picker's state.
//
// Key Fields:
//   - id:        random 12-char base32 string used as the session lookup key
//   - invoker:   user who issued the search; only this user may click results
//   - guildID:   guild where the search lives, used by handlers to route
//                back to the correct VoiceCtx
//   - tracks:    candidate tracks shown in the embed, in display order
//   - mode:      hand-off mode (enqueue vs insert-next)
//   - createdAt: wall clock at construction; expiry derived from createdAt+TTL
type ytSearchSession struct {
	id        string
	invoker   snowflake.ID
	guildID   snowflake.ID
	tracks    []Track
	mode      YTSearchMode
	createdAt time.Time
}

/*
NewYTSearchSession registers a picker session and returns its id. Use
the returned id to build button customIDs in the form
"sikasa:ytsearch:<id>:<idx>".

	params:
	      invoker: user who triggered the search; only this user may click
	      guildID: guild where the search was issued (must be non-zero)
	      tracks:  candidate tracks to display
	returns:
	      string: session id
*/
/*
NewYTSearchSession registers a picker session and returns its id. Use
the returned id to build button customIDs in the form
"sikasa:ytsearch:<id>:<idx>". Mode defaults to YTSearchModeEnqueue.

	params:
	      invoker: user who triggered the search; only this user may click
	      guildID: guild where the search was issued (must be non-zero)
	      tracks:  candidate tracks to display
	returns:
	      string: session id
*/
func (b *Bot) NewYTSearchSession(invoker, guildID snowflake.ID, tracks []Track) string {
	return b.NewYTSearchSessionMode(invoker, guildID, tracks, YTSearchModeEnqueue)
}

/*
NewYTSearchSessionMode is NewYTSearchSession with an explicit hand-off mode.
Use YTSearchModeInsertNext to wire a search picker into a "play next"
prefix command without rebuilding the dispatcher.

	params:
	      invoker, guildID, tracks: see NewYTSearchSession
	      mode: how the click handler should hand the choice off to voice
	returns:
	      string: session id
*/
func (b *Bot) NewYTSearchSessionMode(invoker, guildID snowflake.ID, tracks []Track, mode YTSearchMode) string {
	b.sweepYTSearches()
	id := newSessionID()
	s := &ytSearchSession{
		id:        id,
		invoker:   invoker,
		guildID:   guildID,
		tracks:    tracks,
		mode:      mode,
		createdAt: time.Now(),
	}
	b.ytSearchesMu.Lock()
	if b.ytSearches == nil {
		b.ytSearches = make(map[string]*ytSearchSession)
	}
	b.ytSearches[id] = s
	b.ytSearchesMu.Unlock()
	return id
}

/*
YTSearchSession looks up an active session by id without removing it.
Returns nil when the id is unknown or the session has expired.

	params:
	      id: session id from NewYTSearchSession
	returns:
	      *ytSearchSession: live session, or nil
*/
func (b *Bot) YTSearchSession(id string) *ytSearchSession {
	b.ytSearchesMu.Lock()
	defer b.ytSearchesMu.Unlock()
	s, ok := b.ytSearches[id]
	if !ok {
		return nil
	}
	if time.Since(s.createdAt) > ytSearchTTL {
		delete(b.ytSearches, id)
		return nil
	}
	return s
}

/*
ConsumeYTSearch atomically looks up a session and removes it from the
registry. Picker handlers should consume on success or cancel so the same
button cannot fire twice.

	params:
	      id: session id
	returns:
	      *ytSearchSession: the session, or nil if missing/expired
*/
func (b *Bot) ConsumeYTSearch(id string) *ytSearchSession {
	b.ytSearchesMu.Lock()
	defer b.ytSearchesMu.Unlock()
	s, ok := b.ytSearches[id]
	if !ok {
		return nil
	}
	delete(b.ytSearches, id)
	if time.Since(s.createdAt) > ytSearchTTL {
		return nil
	}
	return s
}

// InvokerID returns the user who created this search session. Click handlers
// use it to refuse strangers attempting to interact with someone else's
// picker.
func (s *ytSearchSession) InvokerID() snowflake.ID { return s.invoker }

// GuildID returns the guild snowflake the session was created in.
func (s *ytSearchSession) GuildID() snowflake.ID { return s.guildID }

// Tracks returns the candidate Tracks shown in the picker, in display order.
// Safe for read-only use; the slice is the same one stored in the session.
func (s *ytSearchSession) Tracks() []Track { return s.tracks }

// Mode returns the hand-off mode picked at session creation. Click handlers
// branch on this to decide between Enqueue and InsertNext.
func (s *ytSearchSession) Mode() YTSearchMode { return s.mode }

// sweepYTSearches drops every expired session. Cheap to call; only called
// from NewYTSearchSession to amortize collection without a goroutine.
func (b *Bot) sweepYTSearches() {
	b.ytSearchesMu.Lock()
	defer b.ytSearchesMu.Unlock()
	for id, s := range b.ytSearches {
		if time.Since(s.createdAt) > ytSearchTTL {
			delete(b.ytSearches, id)
		}
	}
}

/*
BuildYTSearchEmbed renders a picker embed plus a button ActionRow for the
given candidate tracks. The customIDs encode the session id and a 0-based
index, matching the route Bot.OnButton handlers should listen on.

	params:
	      query:     the original search string, shown in the embed title
	      sessionID: id returned by NewYTSearchSession
	      tracks:    same slice passed into the session
	returns:
	      *EmbedBuilder:           ready to pass to ReplyEmbed/SendEmbed
	      []discord.ButtonComponent: one button per track, plus a Cancel button
*/
func BuildYTSearchEmbed(query, sessionID string, tracks []Track) (*EmbedBuilder, []discord.InteractiveComponent) {
	embed := NewEmbed().
		Title(Truncate(fmt.Sprintf("Search: %s", query), EmbedTitleMaxLen)).
		Color(0xED4245)

	var body strings.Builder
	for i, t := range tracks {
		fmt.Fprintf(&body, "**%d.** %s\n", i+1, Truncate(t.Label(), 200))
	}
	if body.Len() == 0 {
		body.WriteString("_no results_")
	}
	embed.Description(Truncate(body.String(), EmbedDescriptionMaxLen))
	embed.Footer("Pick a track within 5 minutes; only the requester can choose.", "")

	buttons := make([]discord.InteractiveComponent, 0, len(tracks)+1)
	for i := range tracks {
		customID := fmt.Sprintf("%s/%s/%d", ytSearchCustomIDPrefix, sessionID, i)
		buttons = append(buttons, discord.NewPrimaryButton(fmt.Sprintf("%d", i+1), customID))
	}
	cancelID := fmt.Sprintf("%s/%s/cancel", ytSearchCustomIDPrefix, sessionID)
	buttons = append(buttons, discord.NewDangerButton("Cancel", cancelID))

	return embed, buttons
}

// newSessionID returns a 12-character lowercased base32 string suitable for
// embedding in a customID. base32 keeps the value URL-safe and short
// enough to stay within Discord's 100-byte customID budget.
func newSessionID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Random failure is fatal at this layer; nanosecond timestamp is a
		// last-ditch fallback so we never panic in a hot path.
		return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(
			[]byte(fmt.Sprintf("%d", time.Now().UnixNano())),
		))[:12]
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))[:12]
}

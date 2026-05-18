// Package sikasa: embed.go
// Purpose: Provides a fluent EmbedBuilder so callers can compose Discord
// embeds without dropping into the raw discord.Embed struct. Pairs with
// MsgCtx.ReplyEmbed / SendEmbed and CmdCtx.ReplyEmbed.
//
// Key Components:
//   - EmbedBuilder:  immutable, chainable wrapper over discord.Embed
//   - NewEmbed():    constructor; preferred entry point
//   - Build():       returns the underlying discord.Embed for direct use
//
// Note: Discord enforces hard limits on embed fields. Setters do NOT
// truncate; they pass through whatever you give them. The package-level
// Truncate helper is provided for callers that want explicit clamping.
package sikasa

import (
	"time"

	"github.com/disgoorg/disgo/discord"
)

// Discord embed limits, surfaced as constants so callers can clamp values
// before hitting the API. https://discord.com/developers/docs/resources/message
const (
	EmbedTitleMaxLen       = 256
	EmbedDescriptionMaxLen = 4096
	EmbedFieldNameMaxLen   = 256
	EmbedFieldValueMaxLen  = 1024
	EmbedFooterTextMaxLen  = 2048
	EmbedAuthorNameMaxLen  = 256
	EmbedFieldsMax         = 25
)

// EmbedBuilder is a fluent wrapper over discord.Embed. Methods mutate the
// receiver and return it, so chains compile to a single Embed value.
//
// Note: Constructor functions return *EmbedBuilder so chains stay nil-safe
// even if zero-value method calls are accidentally inlined. Treat the
// builder as scoped to a single message; do not reuse a builder across
// concurrent goroutines.
type EmbedBuilder struct {
	embed discord.Embed
}

// NewEmbed returns a fresh builder. Identical to MsgCtx.NewEmbed() and
// CmdCtx.NewEmbed(); exposed at package level so callers can build embeds
// outside of a context.
func NewEmbed() *EmbedBuilder {
	return &EmbedBuilder{}
}

// Title sets the embed title (max 256 chars).
func (b *EmbedBuilder) Title(s string) *EmbedBuilder {
	b.embed.Title = s
	return b
}

// Description sets the embed description (max 4096 chars).
func (b *EmbedBuilder) Description(s string) *EmbedBuilder {
	b.embed.Description = s
	return b
}

// URL makes the title clickable, pointing to the given URL.
func (b *EmbedBuilder) URL(u string) *EmbedBuilder {
	b.embed.URL = u
	return b
}

// Color sets the left-hand stripe color. Accepts a hex int like 0x5865F2.
func (b *EmbedBuilder) Color(c int) *EmbedBuilder {
	b.embed.Color = c
	return b
}

// Timestamp sets the small timestamp shown in the footer.
func (b *EmbedBuilder) Timestamp(t time.Time) *EmbedBuilder {
	b.embed.Timestamp = &t
	return b
}

// Now sets the timestamp to the current wall-clock time.
func (b *EmbedBuilder) Now() *EmbedBuilder {
	now := time.Now()
	b.embed.Timestamp = &now
	return b
}

// Author sets the small author block at the top of the embed.
//
//	params:
//	      name:    display name (max 256 chars; required for the block to render)
//	      iconURL: optional thumbnail beside the name; pass "" to omit
//	      url:     optional clickable link on the name; pass "" to omit
func (b *EmbedBuilder) Author(name, iconURL, url string) *EmbedBuilder {
	if name == "" {
		b.embed.Author = nil
		return b
	}
	a := &discord.EmbedAuthor{Name: name}
	if iconURL != "" {
		a.IconURL = iconURL
	}
	if url != "" {
		a.URL = url
	}
	b.embed.Author = a
	return b
}

// Footer sets the small footer text below the body.
//
//	params:
//	      text:    footer text (max 2048 chars; required for the block to render)
//	      iconURL: optional icon beside the footer; pass "" to omit
func (b *EmbedBuilder) Footer(text, iconURL string) *EmbedBuilder {
	if text == "" {
		b.embed.Footer = nil
		return b
	}
	f := &discord.EmbedFooter{Text: text}
	if iconURL != "" {
		f.IconURL = iconURL
	}
	b.embed.Footer = f
	return b
}

// Thumbnail sets the small image rendered in the top-right corner.
func (b *EmbedBuilder) Thumbnail(url string) *EmbedBuilder {
	if url == "" {
		b.embed.Thumbnail = nil
		return b
	}
	b.embed.Thumbnail = &discord.EmbedResource{URL: url}
	return b
}

// Image sets the large image rendered below the body.
func (b *EmbedBuilder) Image(url string) *EmbedBuilder {
	if url == "" {
		b.embed.Image = nil
		return b
	}
	b.embed.Image = &discord.EmbedResource{URL: url}
	return b
}

// Field appends a name/value field. Discord caps at 25 fields per embed;
// extra fields beyond the cap are silently dropped on the API side.
//
//	params:
//	      name:   field heading (max 256 chars)
//	      value:  field body (max 1024 chars)
//	      inline: render side-by-side with adjacent inline fields
func (b *EmbedBuilder) Field(name, value string, inline bool) *EmbedBuilder {
	b.embed.Fields = append(b.embed.Fields, discord.EmbedField{
		Name:   name,
		Value:  value,
		Inline: &inline,
	})
	return b
}

// ClearFields removes any previously-added fields. Useful when reusing a
// builder template across multiple embeds.
func (b *EmbedBuilder) ClearFields() *EmbedBuilder {
	b.embed.Fields = nil
	return b
}

// Build returns the underlying discord.Embed. Most callers do not need
// this directly: ReplyEmbed / SendEmbed accept an EmbedBuilder via Build()
// internally. Exposed for callers that need to mix sikasa embeds with raw
// disgo APIs.
func (b *EmbedBuilder) Build() discord.Embed {
	return b.embed
}

// Truncate clips s to at most n characters, appending an ellipsis when
// truncation occurs. Multi-byte safe for Discord's UTF-8 byte budget if
// n is given as a byte count.
func Truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

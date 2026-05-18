// Package sikasa: errors.go
// Purpose: Exports sentinel errors used across the package so callers can
// distinguish failure modes via errors.Is() instead of string matching.
//
// Key Components:
//   - Voice errors: ErrBotNotStarted, ErrInvalidGuildID, ErrInvalidChannelID,
//     ErrNotInVoice, ErrNoAudio, ErrNotPaused
//   - Bot lifecycle: ErrEmptyToken
//   - Prefix command errors: ErrUnknownCommand, ErrMissingArg, ErrInvalidArg
//
// Note: Wrapped errors (fmt.Errorf("%w: ...", ErrXxx, ...)) preserve the
// sentinel chain, so errors.Is(err, sikasa.ErrXxx) works on both bare and
// wrapped values.
package sikasa

import "errors"

var (
	// ErrBotNotStarted is returned by APIs that require a live gateway
	// connection (most voice operations) when invoked before Bot.Start().
	ErrBotNotStarted = errors.New("sikasa: bot not started yet")

	// ErrInvalidGuildID is returned when a guild snowflake string fails
	// to parse. Wrapped with the offending value for context.
	ErrInvalidGuildID = errors.New("sikasa: invalid guild id")

	// ErrInvalidChannelID is returned when a channel snowflake string
	// fails to parse. Wrapped with the offending value for context.
	ErrInvalidChannelID = errors.New("sikasa: invalid channel id")

	// ErrNotInVoice is returned by voice operations that require an
	// active voice connection for the guild but find none.
	ErrNotInVoice = errors.New("sikasa: not connected to a voice channel")

	// ErrNoAudio is returned when a playback control method (Pause)
	// runs while nothing is playing.
	ErrNoAudio = errors.New("sikasa: no audio playing")

	// ErrNotPaused is returned by Resume() when the stream is not in
	// the paused state.
	ErrNotPaused = errors.New("sikasa: not paused")

	// ErrEmptyToken is returned by New() when the supplied token is "".
	ErrEmptyToken = errors.New("sikasa: empty token")

	// ErrUnknownCommand is the sentinel for prefix-dispatch lookups that
	// fail. The dispatcher renders it to the user as a reply.
	ErrUnknownCommand = errors.New("sikasa: unknown prefix command")

	// ErrMissingArg signals that a required builder argument was not
	// supplied in the user's message.
	ErrMissingArg = errors.New("sikasa: missing required argument")

	// ErrInvalidArg signals that a supplied argument failed type parsing
	// (e.g. IntArg given non-numeric text).
	ErrInvalidArg = errors.New("sikasa: invalid argument value")
)

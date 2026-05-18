// Package sikasa: prefix.go
// Purpose: Provides a fluent PrefixBuilder for declaring text-based prefix
// commands (e.g. "!play song.mp3"). Mirrors the bot.Command builder so users
// get a consistent feel across slash and prefix invocation paths.
//
// Key Components:
//   - PrefixBuilder:   fluent type for one prefix command
//   - Bot.OnPrefix():  entry point that returns a fresh builder
//   - Bot.SetPrefix(): sets the global trigger prefix (default: "")
//   - StringArg/IntArg/BoolArg: option helpers, hybrid with ctx.Rest()
//   - PrefixHandler:   function signature for prefix command handlers
//   - dispatchPrefix:  internal MessageCreate router
//
// Note: Last StringArg consumes the entire remaining message tail. Quote-aware
// tokenization (e.g. "!echo \"hello world\"" as a single arg) is intentionally
// out of scope for the MVP; callers can fall back to ctx.Rest() if they need
// the raw string.
package sikasa

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/events"
)

// PrefixHandler is the signature every prefix command handler must satisfy.
type PrefixHandler func(ctx *PrefixCtx) error

// argKind tags a builder option's parsing strategy.
type argKind int

const (
	argString argKind = iota
	argInt
	argBool
)

// prefixArg is one declared option on a PrefixBuilder.
//
// Key Fields:
//   - name:     used as the lookup key in PrefixCtx.String/Int/Bool
//   - kind:     determines the parser used at dispatch time
//   - required: dispatcher replies with ErrMissingArg if absent
type prefixArg struct {
	name     string
	desc     string
	kind     argKind
	required bool
}

// PrefixBuilder accumulates a prefix command's definition and handler.
//
// Key Fields:
//   - name, desc: user-visible name and description (description currently
//                 unused but reserved for a future help generator)
//   - aliases:    extra names that resolve to this builder
//   - args:       option list, parsed in registration order
//   - handler:    invoked when a message matches the command
//
// Note: Builders are not thread-safe; finish configuration before Start().
type PrefixBuilder struct {
	name    string
	desc    string
	aliases []string
	args    []prefixArg
	handler PrefixHandler
}

/*
SetPrefix sets the global text prefix that triggers PrefixBuilder dispatch.
An empty string disables prefix dispatch entirely (the default).

	params:
	      p: the prefix string (e.g. "!", "?", "k!")
	returns:
	      *Bot: receiver, for chaining
*/
func (b *Bot) SetPrefix(p string) *Bot {
	b.prefix = p
	return b
}

/*
OnPrefix registers a new prefix command and returns its builder. Chain
arg helpers and Handle() to finish the definition.

	params:
	      name:        the command name (without the prefix)
	      description: a short user-visible description
	returns:
	      *PrefixBuilder: builder for further configuration
*/
func (b *Bot) OnPrefix(name, description string) *PrefixBuilder {
	pb := &PrefixBuilder{name: name, desc: description}
	b.prefixes = append(b.prefixes, pb)
	return pb
}

/*
Aliases attaches alternative names that dispatch to the same handler.
For example, .Aliases("p", "pl") on a "play" command lets users type
"!p" or "!pl" interchangeably.

	params:
	      names: one or more alias strings
	returns:
	      *PrefixBuilder: receiver, for chaining
*/
func (p *PrefixBuilder) Aliases(names ...string) *PrefixBuilder {
	p.aliases = append(p.aliases, names...)
	return p
}

/*
StringArg adds a string option to the command. The last StringArg in the
declaration order consumes the entire remaining message tail, so a
single trailing StringArg captures free-form text like "echo hello world".

	params:
	      name, description, required: see CommandBuilder.StringArg
	returns:
	      *PrefixBuilder: receiver, for chaining
*/
func (p *PrefixBuilder) StringArg(name, description string, required bool) *PrefixBuilder {
	p.args = append(p.args, prefixArg{name: name, desc: description, kind: argString, required: required})
	return p
}

/*
IntArg adds an integer option to the command. Values are parsed via
strconv.ParseInt; invalid values trigger ErrInvalidArg.

	params:
	      name, description, required: see CommandBuilder.IntArg
	returns:
	      *PrefixBuilder: receiver, for chaining
*/
func (p *PrefixBuilder) IntArg(name, description string, required bool) *PrefixBuilder {
	p.args = append(p.args, prefixArg{name: name, desc: description, kind: argInt, required: required})
	return p
}

/*
BoolArg adds a boolean option. Accepted truthy values: "true", "1", "yes",
"y", "on". Falsy: "false", "0", "no", "n", "off". Anything else triggers
ErrInvalidArg.

	params:
	      name, description, required: see CommandBuilder.BoolArg
	returns:
	      *PrefixBuilder: receiver, for chaining
*/
func (p *PrefixBuilder) BoolArg(name, description string, required bool) *PrefixBuilder {
	p.args = append(p.args, prefixArg{name: name, desc: description, kind: argBool, required: required})
	return p
}

/*
Handle attaches the handler that runs when this command is invoked.

	params:
	      h: the handler invoked with a *PrefixCtx
	returns:
	      *PrefixBuilder: receiver, for chaining
*/
func (p *PrefixBuilder) Handle(h PrefixHandler) *PrefixBuilder {
	p.handler = h
	return p
}

// indexPrefixes builds the lookup table from b.prefixes. Called once during
// Start() after all OnPrefix() calls have completed. Panics on duplicate
// names or aliases since that is developer error and should fail fast.
func (b *Bot) indexPrefixes() {
	b.prefixIndex = make(map[string]*PrefixBuilder, len(b.prefixes))
	for _, pb := range b.prefixes {
		if pb.handler == nil {
			continue
		}
		register := func(key string) {
			low := strings.ToLower(key)
			if _, exists := b.prefixIndex[low]; exists {
				panic(fmt.Sprintf("sikasa: duplicate prefix command or alias %q", key))
			}
			b.prefixIndex[low] = pb
		}
		register(pb.name)
		for _, a := range pb.aliases {
			register(a)
		}
	}
}

// dispatchPrefix routes a MessageCreate through the prefix command lookup.
// Returns true if the message was claimed by prefix dispatch (matched the
// prefix, even if the command was unknown), so the caller can skip keyword
// matching to prevent double-responses.
func (b *Bot) dispatchPrefix(e *events.MessageCreate) bool {
	if b.prefix == "" {
		return false
	}
	content := e.Message.Content
	if !strings.HasPrefix(content, b.prefix) {
		return false
	}
	body := strings.TrimPrefix(content, b.prefix)
	tokens := strings.Fields(body)
	if len(tokens) == 0 {
		// Bare prefix with no command name; treat as not-a-command so other
		// listeners (keywords) still get a chance.
		return false
	}

	name := strings.ToLower(tokens[0])
	pb, ok := b.prefixIndex[name]
	mctx := newMsgCtx(b, e)
	if !ok {
		// Unknown command: reply but still claim the message so keyword
		// matchers do not double-fire.
		_ = mctx.Reply(fmt.Sprintf("%s: %s%s", ErrUnknownCommand.Error(), b.prefix, tokens[0]))
		return true
	}

	// rawArgs preserves whitespace; rest is "everything after the command name".
	rest := strings.TrimLeft(strings.TrimPrefix(body, tokens[0]), " \t")
	rawArgs := tokens[1:]

	parsed, perr := parsePrefixArgs(pb.args, rawArgs, rest)
	if perr != nil {
		_ = mctx.Reply(perr.Error())
		return true
	}

	pctx := &PrefixCtx{
		MsgCtx: mctx,
		name:   pb.name,
		args:   parsed,
		raw:    rawArgs,
		rest:   rest,
	}
	if err := pb.handler(pctx); err != nil {
		b.logger.Printf("sikasa: prefix %q error: %v", pb.name, err)
	}
	return true
}

// parsePrefixArgs walks the builder's declared args and pulls values from
// the tokenized message body. The last StringArg in the declaration absorbs
// all remaining tokens (including embedded whitespace) so handlers can take
// free-form trailing text.
func parsePrefixArgs(decls []prefixArg, tokens []string, rest string) (map[string]any, error) {
	out := make(map[string]any, len(decls))
	consumed := 0

	// Locate the last StringArg index; only that one greedily absorbs the tail.
	lastString := -1
	for i, d := range decls {
		if d.kind == argString {
			lastString = i
		}
	}

	// We rebuild the tail at each step: rest minus the tokens we already took.
	// Computing this exactly requires tracking byte offsets, so we just track
	// how many tokens we've consumed and slice tokens for non-greedy args; the
	// greedy slot uses the original `rest` minus the prefix-stripped tokens.
	tail := rest

	for i, d := range decls {
		// Strip leading tokens we've already consumed off `tail` so the greedy
		// StringArg sees only its share of the original whitespace-preserving rest.
		if consumed > 0 && i == lastString && d.kind == argString {
			tail = stripLeadingTokens(rest, consumed)
		}

		hasMore := consumed < len(tokens)
		if !hasMore {
			if d.required {
				return nil, fmt.Errorf("%w: %s", ErrMissingArg, d.name)
			}
			continue
		}

		switch d.kind {
		case argString:
			if i == lastString {
				out[d.name] = tail
				consumed = len(tokens)
			} else {
				out[d.name] = tokens[consumed]
				consumed++
			}
		case argInt:
			v, err := strconv.ParseInt(tokens[consumed], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: %s=%q", ErrInvalidArg, d.name, tokens[consumed])
			}
			out[d.name] = v
			consumed++
		case argBool:
			v, err := parseBoolLoose(tokens[consumed])
			if err != nil {
				return nil, fmt.Errorf("%w: %s=%q", ErrInvalidArg, d.name, tokens[consumed])
			}
			out[d.name] = v
			consumed++
		}
	}
	return out, nil
}

// stripLeadingTokens removes the first n whitespace-separated tokens from s
// and returns the remainder with original whitespace between later tokens
// preserved.
func stripLeadingTokens(s string, n int) string {
	r := s
	for i := 0; i < n; i++ {
		r = strings.TrimLeft(r, " \t")
		// find the next whitespace boundary
		idx := strings.IndexAny(r, " \t")
		if idx < 0 {
			return ""
		}
		r = r[idx:]
	}
	return strings.TrimLeft(r, " \t")
}

// parseBoolLoose accepts the common truthy/falsy strings users actually type,
// rather than only Go's strict "true"/"false".
func parseBoolLoose(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1", "yes", "y", "on":
		return true, nil
	case "false", "0", "no", "n", "off":
		return false, nil
	}
	return false, errors.New("not a boolean")
}

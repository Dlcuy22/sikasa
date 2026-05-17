// Package sikasa: keyword.go
// Purpose: Provides a fluent KeywordBuilder for matching plain-message
// content against a set of substrings or a regular expression and firing
// a handler when a match occurs.
//
// Key Components:
//   - KeywordBuilder: fluent type for one keyword/regex rule
//   - Bot.OnKeyword(): substring-based matcher (case-insensitive)
//   - Bot.OnRegex():   regex-based matcher (compiled once at registration)
//   - MsgHandler:      function signature for keyword handlers
//
// Dependencies:
//   - regexp: standard library regex engine
//
// Note: Multiple keyword rules may match the same message; all matching
// rules fire in registration order.
package sikasa

import (
	"regexp"
	"strings"
)

// MsgHandler is the signature every message handler must satisfy.
type MsgHandler func(ctx *MsgCtx) error

// KeywordBuilder accumulates a keyword rule and its handler.
//
// Key Fields:
//   - terms:   substrings to match against (lower-cased on the message side)
//   - regex:   compiled regex; mutually exclusive with terms in practice
//   - handler: invoked when a match is found
type KeywordBuilder struct {
	terms   []string
	regex   *regexp.Regexp
	handler MsgHandler
}

/*
OnKeyword registers a substring-based keyword rule. Matching is
case-insensitive and uses Contains semantics, so "hello" matches
"hello world" and "Well, Hello!" alike.

	params:
	      terms: one or more substrings; the rule fires if ANY are present
	returns:
	      *KeywordBuilder: builder for attaching a handler via Reply()
*/
func (b *Bot) OnKeyword(terms ...string) *KeywordBuilder {
	lower := make([]string, len(terms))
	for i, t := range terms {
		lower[i] = strings.ToLower(t)
	}
	kb := &KeywordBuilder{terms: lower}
	b.kws = append(b.kws, kb)
	return kb
}

/*
OnRegex registers a regex-based message rule. The pattern is compiled
once at registration; an invalid pattern panics here rather than later
inside the message dispatcher.

	params:
	      pattern: a Go regexp/RE2 pattern
	returns:
	      *KeywordBuilder: builder for attaching a handler via Reply()
*/
func (b *Bot) OnRegex(pattern string) *KeywordBuilder {
	// Panic on invalid pattern at registration; this is developer error,
	// not runtime data, so failing fast is correct.
	re := regexp.MustCompile(pattern)
	kb := &KeywordBuilder{regex: re}
	b.kws = append(b.kws, kb)
	return kb
}

/*
Reply attaches the handler that runs when this rule matches.

	params:
	      h:      the handler invoked with a *MsgCtx
	      limits: optional rate limit config (e.g. sikasa.RateLimitInterval(3, time.Minute))
	returns:
	      *KeywordBuilder: receiver, for chaining
*/
func (k *KeywordBuilder) Reply(h MsgHandler, limits ...RateLimitConfig) *KeywordBuilder {
	k.handler = wrapMsgHandler(h, limits)
	return k
}

/*
ReplyText is a shortcut for fixed text replies. The reply uses Discord's
inline reply (message reference) so it links back to the user's message.

	params:
	      text:   the message body to send
	      limits: optional rate limit config
	returns:
	      *KeywordBuilder: receiver, for chaining
*/
func (k *KeywordBuilder) ReplyText(text string, limits ...RateLimitConfig) *KeywordBuilder {
	h := func(ctx *MsgCtx) error { return ctx.Reply(text) }
	k.handler = wrapMsgHandler(h, limits)
	return k
}

/*
ReplyFile is a shortcut for replying with a local file attachment.

	params:
	      content:  optional message body sent alongside the file
	      filePath: path to a file on disk
	      limits:   optional rate limit config
	returns:
	      *KeywordBuilder: receiver, for chaining
*/
func (k *KeywordBuilder) ReplyFile(content, filePath string, limits ...RateLimitConfig) *KeywordBuilder {
	h := func(ctx *MsgCtx) error { return ctx.ReplyFile(content, filePath) }
	k.handler = wrapMsgHandler(h, limits)
	return k
}

// matches reports whether the rule matches the given message content.
// For substring rules, the comparison is case-insensitive; for regex
// rules the pattern is applied as-is.
func (k *KeywordBuilder) matches(content string) bool {
	if k.regex != nil {
		return k.regex.MatchString(content)
	}
	if len(k.terms) == 0 {
		return false
	}
	low := strings.ToLower(content)
	for _, t := range k.terms {
		if strings.Contains(low, t) {
			return true
		}
	}
	return false
}

// fire invokes the handler if one was attached. Rules without a handler
// are silently skipped; this lets users build rules incrementally.
func (k *KeywordBuilder) fire(ctx *MsgCtx) error {
	if k.handler == nil {
		return nil
	}
	return k.handler(ctx)
}

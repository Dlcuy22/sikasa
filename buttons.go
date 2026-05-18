// Package sikasa: buttons.go
// Purpose: Provides Bot.OnButton, a thin sugar over disgo's
// handler.Mux.ButtonComponent that hands the user a ButtonCtx instead of
// raw disgo types. Pattern strings are passed straight through to disgo,
// so customID lookup uses the same chi-style {var} syntax.
//
// Key Components:
//   - ButtonHandler:  signature for sikasa-style button handlers
//   - Bot.OnButton(): registers a button route on the underlying router
//
// Note: OnButton must be called BEFORE Bot.Start(), because the disgo
// handler.Mux is constructed there and routes are registered on it during
// Start. Calling OnButton after Start would panic.
package sikasa

import (
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/handler"
)

// ButtonHandler is the signature every button handler must satisfy.
type ButtonHandler func(ctx *ButtonCtx) error

// buttonRoute holds a registered pattern and its sikasa-level handler. The
// router is built in Start(), so registrations made via OnButton are queued
// in this slice and flushed there.
type buttonRoute struct {
	pattern string
	handler ButtonHandler
}

/*
OnButton registers a handler for button clicks whose customID matches the
given chi-style pattern (e.g. "/sikasa/ytsearch/{session}/{idx}"). Path
variables are accessible from the ButtonCtx via Var(name) or Vars().

	params:
	      pattern: customID pattern, supports {var} placeholders
	      h:       handler invoked with a *ButtonCtx
	returns:
	      *Bot:    receiver, for chaining

Note: Must be called before Bot.Start(). Handlers registered after Start()
will not be wired into the live router.
*/
func (b *Bot) OnButton(pattern string, h ButtonHandler) *Bot {
	b.buttonRoutes = append(b.buttonRoutes, buttonRoute{pattern: pattern, handler: h})
	return b
}

// registerButtons flushes queued button routes onto the disgo router. Called
// from Start() after b.router is constructed.
func (b *Bot) registerButtons() {
	for _, r := range b.buttonRoutes {
		route := r
		b.router.ButtonComponent(route.pattern, func(data discord.ButtonInteractionData, e *handler.ComponentEvent) error {
			ctx := &ButtonCtx{
				bot:   b,
				event: e,
				data:  data,
				vars:  e.Vars,
			}
			return route.handler(ctx)
		})
	}
}

// Package sikasa: prefixctx.go
// Purpose: Defines PrefixCtx, the per-invocation context passed to prefix
// command handlers. Embeds *MsgCtx to inherit Reply/Send/React helpers, and
// adds typed accessors (String/Int/Bool) plus raw-token escape hatches
// (Args/Arg/Rest) so handlers can mix builder validation with manual parsing.
//
// Key Components:
//   - PrefixCtx:  per-invocation handler context, wraps MsgCtx
//   - String/Int/Bool: typed access to declared builder args
//   - Args/Arg/Rest:    raw-token access for free-form parsing
//   - Name():           the resolved command name (not the alias)
//
// Note: Reply* methods come from the embedded *MsgCtx; the prefix dispatcher
// is the only construction site, so these accessors are guaranteed non-nil
// for the user.
package sikasa

// PrefixCtx is the context passed to a prefix command handler.
//
// Key Fields:
//   - MsgCtx: embedded; provides Bot(), Event(), Reply(), Send(), etc.
//   - name:   resolved canonical command name (Aliases are normalized away)
//   - args:   parsed builder args, keyed by the names declared in StringArg
//             / IntArg / BoolArg
//   - raw:    every whitespace-separated token after the command name
//   - rest:   the entire message tail after the command name, with original
//             whitespace preserved
type PrefixCtx struct {
	*MsgCtx
	name string
	args map[string]any
	raw  []string
	rest string
}

// Name returns the canonical command name that matched, even if the user
// invoked the command via an alias.
func (c *PrefixCtx) Name() string { return c.name }

/*
String returns the value of a named StringArg. If the arg was optional and
omitted, the zero value "" is returned.

	params:
	      name: the arg name as declared via StringArg
	returns:
	      string: the parsed value, or "" if absent
*/
func (c *PrefixCtx) String(name string) string {
	if v, ok := c.args[name]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

/*
Int returns the value of a named IntArg.

	params:
	      name: the arg name as declared via IntArg
	returns:
	      int64: the parsed value, or 0 if absent
*/
func (c *PrefixCtx) Int(name string) int64 {
	if v, ok := c.args[name]; ok {
		if n, ok := v.(int64); ok {
			return n
		}
	}
	return 0
}

/*
Bool returns the value of a named BoolArg.

	params:
	      name: the arg name as declared via BoolArg
	returns:
	      bool: the parsed value, or false if absent
*/
func (c *PrefixCtx) Bool(name string) bool {
	if v, ok := c.args[name]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// Args returns every whitespace-separated token after the command name. Use
// this when builder args do not fit the parsing shape you need (e.g. a
// variadic list of user IDs).
func (c *PrefixCtx) Args() []string { return c.raw }

// Arg returns the i-th token after the command name, or "" if i is out of
// range. Convenient when you only care about positions.
func (c *PrefixCtx) Arg(i int) string {
	if i < 0 || i >= len(c.raw) {
		return ""
	}
	return c.raw[i]
}

// Rest returns the message tail after the command name with original
// whitespace preserved. Use this for raw text capture (e.g. quoting,
// multi-line input) that strings.Fields would have collapsed.
func (c *PrefixCtx) Rest() string { return c.rest }

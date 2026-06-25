// Package sikasa: group_test.go
// Purpose: Implements unit tests for the fluent Group API.
//
// Key Components:
//   - TestGroup_FluentAPI(): Verifies that Group registration correctly routes to the parent Bot
//
// Dependencies:
//   - testing: standard Go testing framework
package sikasa

import (
	"testing"
)

/*
TestGroup_FluentAPI tests the Registration of a group and command routing.

	params:
	      t: test runner context
*/
func TestGroup_FluentAPI(t *testing.T) {
	bot, err := New("dummy_token")
	if err != nil {
		t.Fatalf("failed to create bot: %v", err)
	}

	registered := false
	bot.Group("test_group", func(g *Group) {
		if g.Name() != "test_group" {
			t.Errorf("expected group name test_group, got %q", g.Name())
		}
		if g.Bot() != bot {
			t.Error("expected group bot reference to match parent bot")
		}

		g.Command("test_cmd", "test description")
		registered = true
	})

	if !registered {
		t.Error("group setup callback was not called")
	}

	if len(bot.cmds) != 1 {
		t.Errorf("expected 1 command to be registered, got %d", len(bot.cmds))
	}
	if bot.cmds[0].name != "test_cmd" {
		t.Errorf("expected command name 'test_cmd', got %q", bot.cmds[0].name)
	}
}

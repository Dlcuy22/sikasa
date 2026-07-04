//go:build windows

// Package sikasa: restart_windows.go
// Purpose: Implements Windows-specific process re-execution fallback for hot reloading and
// hard restarting of the bot.
//
// Key Components:
//   - restartProcess(): Windows implementation spawning a child process and exiting the parent.
//
// Dependencies:
//   - os: Accessing command-line arguments.
//   - os/exec: Spawning child process.
//
package sikasa

import (
	"os"
	"os/exec"
)

/*
restartProcess spawns a new instance of the current binary and exits the
current process.

	note:
	      This is a fallback for Windows where replacing process image via Exec is not supported.
*/
func (b *Bot) restartProcess() {
	argv0 := os.Args[0]
	if absolutePath, err := exec.LookPath(argv0); err == nil {
		argv0 = absolutePath
	}
	cmd := exec.Command(argv0, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Start()
	os.Exit(0)
}

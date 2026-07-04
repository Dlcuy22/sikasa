//go:build !windows

// Package sikasa: restart_unix.go
// Purpose: Implements Unix-specific process re-execution for hot reloading and
// hard restarting of the bot.
//
// Key Components:
//   - restartProcess(): Unix implementation using syscall.Exec to replace the process image.
//
// Dependencies:
//   - os: Accessing command-line arguments.
//   - os/exec: Resolving absolute binary path.
//   - syscall: Replacing process image via Exec.
//
package sikasa

import (
	"os"
	"os/exec"
	"syscall"
)

/*
restartProcess replaces the current process image with a fresh execution of
the current binary, preserving the process identifier (PID).

	note:
	      This is only supported on Unix-like operating systems.
*/
func (b *Bot) restartProcess() {
	argv0 := os.Args[0]
	if absolutePath, err := exec.LookPath(argv0); err == nil {
		argv0 = absolutePath
	}
	_ = syscall.Exec(argv0, os.Args, os.Environ())
	// Fallback to exit if Exec fails
	os.Exit(1)
}

//go:build !windows

// Package sikasa: restart_unix_test.go
// Purpose: Verifies Unix-specific OS-level process re-execution (syscall.Exec)
// preserving the process ID (PID) across execution.
//
// Key Components:
//   - TestProcessReexec(): Spawns a child process, triggers restart, and verifies PID.
//
// Dependencies:
//   - os: Checking environment and process details
//   - os/exec: Spawning the helper process
//
package sikasa

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

/*
TestProcessReexec verifies that restartProcess replaces the process image
using syscall.Exec, maintaining the exact same PID.
*/
func TestProcessReexec(t *testing.T) {
	role := os.Getenv("TEST_REEXEC_ROLE")

	if role == "" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatalf("failed to find current executable: %v", err)
		}

		cmd := exec.Command(exe, "-test.run=TestProcessReexec")
		cmd.Env = append(os.Environ(), "TEST_REEXEC_ROLE=child_initial")

		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			t.Fatalf("child process failed: %v", err)
		}

		scanner := bufio.NewScanner(&stdout)
		var pid1, pid2 string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "START_PID:") {
				pid1 = strings.TrimPrefix(line, "START_PID:")
			} else if strings.HasPrefix(line, "RESTART_PID:") {
				pid2 = strings.TrimPrefix(line, "RESTART_PID:")
			}
		}

		if pid1 == "" || pid2 == "" {
			t.Fatalf("expected both PIDs, got pid1=%q, pid2=%q. Output:\n%s", pid1, pid2, stdout.String())
		}
		if pid1 != pid2 {
			t.Fatalf("PID was not preserved: %s vs %s", pid1, pid2)
		}
		return
	}

	if role == "child_initial" {
		fmt.Printf("START_PID:%d\n", os.Getpid())

		bot, err := New("dummy_token")
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to create bot: %v\n", err)
			os.Exit(1)
		}

		os.Setenv("TEST_REEXEC_ROLE", "child_restarted")

		bot.restartProcess()

		fmt.Fprintln(os.Stderr, "error: restartProcess returned instead of replacing image")
		os.Exit(1)
	}

	if role == "child_restarted" {
		fmt.Printf("RESTART_PID:%d\n", os.Getpid())
		os.Exit(0)
	}
}

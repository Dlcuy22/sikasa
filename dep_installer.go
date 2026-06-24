// Package sikasa: dep_installer.go
// Purpose: Handles automatic detection and installation of system-level
// shared library dependencies (FFmpeg/libavformat/libavutil).
//
// Key Components:
//   - ensureDependencies(): Automatically detects OS and package manager to install FFmpeg shared libraries.
//
// Dependencies:
//   - os: to read files and check states
//   - os/exec: to execute installation commands
//   - regexp: to parse OS release information
//   - runtime: to detect host OS
//
package sikasa

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

/*
ensureDependencies detects the running operating system and attempts to
install the required FFmpeg shared libraries (libavformat/libavutil) using
the system package manager if they are not already installed.

    returns:
          error: if installation command fails
*/
func ensureDependencies() error {
	log.Printf("sikasa: required FFmpeg shared libraries not found; attempting automated installation...")

	switch runtime.GOOS {
	case "linux":
		return installLinuxDeps()
	case "windows":
		return installWindowsDeps()
	case "darwin":
		return installMacDeps()
	default:
		return fmt.Errorf("unsupported operating system for automated dependency installation: %s", runtime.GOOS)
	}
}

/*
installLinuxDeps parses /etc/os-release and installs development packages.

    returns:
          error: if command execution fails
*/
func installLinuxDeps() error {
	f, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return fmt.Errorf("failed to read /etc/os-release: %w", err)
	}

	idReg := regexp.MustCompile(`(?m)^ID=["']?([a-zA-Z0-9_-]+)["']?`)
	matches := idReg.FindSubmatch(f)
	if len(matches) < 2 {
		return fmt.Errorf("failed to parse system ID from /etc/os-release")
	}
	distroID := strings.ToLower(string(matches[1]))

	var cmd *exec.Cmd
	switch distroID {
	case "ubuntu", "debian", "pop", "mint":
		log.Printf("sikasa: detected %s; running apt-get to install libavformat-dev and libavutil-dev", distroID)
		cmd = exec.Command("sudo", "apt-get", "update")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("apt-get update failed: %w", err)
		}
		cmd = exec.Command("sudo", "apt-get", "install", "-y", "libavformat-dev", "libavutil-dev")
	case "arch", "manjaro":
		log.Printf("sikasa: detected %s; running pacman to install ffmpeg", distroID)
		cmd = exec.Command("sudo", "pacman", "-Syu", "--noconfirm", "ffmpeg")
	case "fedora", "centos", "rhel":
		log.Printf("sikasa: detected %s; running dnf to install ffmpeg-devel", distroID)
		cmd = exec.Command("sudo", "dnf", "install", "-y", "ffmpeg-devel")
	case "alpine":
		log.Printf("sikasa: detected %s; running apk to install ffmpeg-dev", distroID)
		cmd = exec.Command("apk", "add", "ffmpeg-dev")
	default:
		return fmt.Errorf("unsupported linux distribution for auto-install: %s; please install libavformat/libavutil shared libraries manually", distroID)
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("package installation command failed: %w", err)
	}

	log.Printf("sikasa: shared libraries installed successfully")
	return nil
}

/*
installWindowsDeps uses winget to install FFmpeg shared libraries.

    returns:
          error: if winget execution fails
*/
func installWindowsDeps() error {
	log.Printf("sikasa: running winget to install Gyan.FFmpeg.Shared...")
	cmd := exec.Command("winget", "install", "-e", "--id", "Gyan.FFmpeg.Shared")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("winget installation failed: %w", err)
	}

	log.Println("********************************************************************************")
	log.Println("WARNING: FFmpeg shared libraries installed successfully via winget.")
	log.Println("Please RESTART your shell (close and re-open Windows Terminal/Command Prompt)")
	log.Println("so that the updated PATH environment variables take effect.")
	log.Println("********************************************************************************")
	return nil
}

/*
installMacDeps uses Homebrew to install FFmpeg shared libraries.

    returns:
          error: if brew execution fails
*/
func installMacDeps() error {
	log.Printf("sikasa: running brew to install ffmpeg...")
	cmd := exec.Command("brew", "install", "ffmpeg")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("homebrew installation failed: %w", err)
	}
	log.Printf("sikasa: shared libraries installed successfully")
	return nil
}

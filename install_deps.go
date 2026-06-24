// Package sikasa: install_deps.go
// Purpose: Handles automatic detection and installation of system-level
// shared library dependencies (FFmpeg/libavformat/libavutil) and CLI binaries
// (ffmpeg, yt-dlp, bun) required for the music/remux pipelines.
//
// Key Components:
//   - ensureDependencies(): Performs pre-flight check and downloads missing dependencies
//   - ResolveBinaryPath(): Finds absolute paths of required CLI executables
//
// Dependencies:
//   - archive/zip: for extracting bun zip archives
//   - net/http: for downloading binaries
//   - os: to read files and check states
//   - os/exec: to execute installation commands
//   - regexp: to parse OS release information
//   - runtime: to detect host OS
//
package sikasa

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

/*
getSikasaBinDir returns the target path to store downloaded binary tools.

    returns:
          string: path to local bin folder
*/
func getSikasaBinDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "sikasa-data/bin"
	}
	return filepath.Join(home, ".sikasa", "bin")
}

/*
ResolveBinaryPath resolves the absolute path of a binary. It first checks the
system PATH, and if not found, checks the local Sikasa bin directory.

    params:
          name: binary name (e.g. "yt-dlp", "ffmpeg", "bun")
    returns:
          string: resolved path or name if not found
*/
func ResolveBinaryPath(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		name += ".exe"
	}
	if p, err := exec.LookPath(name); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	localPath := filepath.Join(getSikasaBinDir(), name)
	if _, err := os.Stat(localPath); err == nil {
		if abs, err := filepath.Abs(localPath); err == nil {
			return abs
		}
		return localPath
	}
	return name
}

/*
ensureDependencies detects the running operating system and attempts to
install the required FFmpeg shared libraries (libavformat/libavutil) using
the system package manager if they are not already installed, as well as
downloading yt-dlp and bun binaries if they are missing.

    returns:
          error: if installation or download fails
*/
func ensureDependencies() error {
	log.Printf("sikasa: checking required dependencies (FFmpeg shared libraries, yt-dlp, bun)...")

	// 1. Ensure local bin directory exists
	binDir := getSikasaBinDir()
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("failed to create local bin directory: %w", err)
	}

	// 2. Check if we need to install FFmpeg shared libraries (for native remuxer)
	if err := loadFFmpegLibraries(); err != nil {
		log.Printf("sikasa: FFmpeg shared libraries not found (%v); attempting system-level installation...", err)
		if errInstall := installSystemFFmpeg(); errInstall != nil {
			return fmt.Errorf("failed to install FFmpeg shared libraries: %w", errInstall)
		}
	}

	// 3. Check for yt-dlp
	ytDlpPath := ResolveBinaryPath("yt-dlp")
	if ytDlpPath == "yt-dlp" || ytDlpPath == "yt-dlp.exe" {
		log.Printf("sikasa: yt-dlp not found in PATH or local directory; downloading...")
		if err := downloadYtDlp(); err != nil {
			return fmt.Errorf("failed to download yt-dlp: %w", err)
		}
	}

	// 4. Check for bun
	bunPath := ResolveBinaryPath("bun")
	if bunPath == "bun" || bunPath == "bun.exe" {
		log.Printf("sikasa: bun not found in PATH or local directory; downloading...")
		if err := downloadBun(); err != nil {
			return fmt.Errorf("failed to download bun: %w", err)
		}
	}

	log.Printf("sikasa: all dependencies verified and ready.")
	return nil
}

/*
downloadFile helper downloads a URL directly to the target file path.

    params:
          url:      download endpoint
          destPath: local destination path
    returns:
          error:    on download or file write failure
*/
func downloadFile(url, destPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

/*
extractFileFromZip extracts a single target file from a zip archive.

    params:
          zipPath:    source zip archive path
          targetName: name of the file inside zip to extract
          destPath:   local path to save extracted file
    returns:
          error:      on extraction failure
*/
func extractFileFromZip(zipPath, targetName, destPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		if filepath.Base(f.Name) == targetName {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()

			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return err
			}
			defer out.Close()

			_, err = io.Copy(out, rc)
			return err
		}
	}
	return fmt.Errorf("target file %s not found in zip archive", targetName)
}

/*
downloadYtDlp downloads the correct yt-dlp binary for the current OS/architecture.

    returns:
          error: on download failure
*/
func downloadYtDlp() error {
	var url string
	ext := ""
	if runtime.GOOS == "windows" {
		url = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe"
		ext = ".exe"
	} else if runtime.GOOS == "darwin" {
		url = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_macos"
	} else {
		if runtime.GOARCH == "arm64" {
			url = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux_aarch64"
		} else {
			url = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp"
		}
	}

	dest := filepath.Join(getSikasaBinDir(), "yt-dlp"+ext)
	log.Printf("sikasa: downloading yt-dlp from %s to %s...", url, dest)
	if err := downloadFile(url, dest); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dest, 0755); err != nil {
			return fmt.Errorf("failed to make yt-dlp executable: %w", err)
		}
	}
	return nil
}

/*
downloadBun downloads and extracts the bun CLI for the current OS/architecture.

    returns:
          error: on download or extraction failure
*/
func downloadBun() error {
	var url string
	var archiveName string
	binaryName := "bun"
	if runtime.GOOS == "windows" {
		url = "https://github.com/oven-sh/bun/releases/latest/download/bun-windows-x64.zip"
		archiveName = "bun-windows-x64.zip"
		binaryName = "bun.exe"
	} else if runtime.GOOS == "darwin" {
		archiveName = "bun-darwin.zip"
		if runtime.GOARCH == "arm64" {
			url = "https://github.com/oven-sh/bun/releases/latest/download/bun-darwin-aarch64.zip"
		} else {
			url = "https://github.com/oven-sh/bun/releases/latest/download/bun-darwin-x64.zip"
		}
	} else {
		archiveName = "bun-linux.zip"
		if runtime.GOARCH == "arm64" {
			url = "https://github.com/oven-sh/bun/releases/latest/download/bun-linux-aarch64.zip"
		} else {
			url = "https://github.com/oven-sh/bun/releases/latest/download/bun-linux-x64.zip"
		}
	}

	zipDest := filepath.Join(getSikasaBinDir(), archiveName)
	log.Printf("sikasa: downloading bun from %s to %s...", url, zipDest)
	if err := downloadFile(url, zipDest); err != nil {
		return err
	}
	defer os.Remove(zipDest)

	dest := filepath.Join(getSikasaBinDir(), binaryName)
	log.Printf("sikasa: extracting %s from zip archive...", binaryName)
	if err := extractFileFromZip(zipDest, binaryName, dest); err != nil {
		return err
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(dest, 0755); err != nil {
			return fmt.Errorf("failed to make bun executable: %w", err)
		}
	}
	return nil
}

/*
installSystemFFmpeg detects the running operating system and attempts to
install the required FFmpeg shared libraries (libavformat/libavutil) using
the system package manager.

    returns:
          error: if installation command fails
*/
func installSystemFFmpeg() error {
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

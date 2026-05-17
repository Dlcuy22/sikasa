// Package sikasa: voice_youtube.go
// Purpose: Spawns yt-dlp to extract a YouTube (or other supported site) audio
// stream and pipes it into FFmpeg for re-encoding to Opus.
//
// Key Components:
//   - spawnYouTube(): chains yt-dlp -> FFmpeg, returning a single ffmpegProcess
//                      whose stdout yields Ogg-Opus
//
// Dependencies:
//   - os/exec: subprocess lifecycle
//
// Note: yt-dlp must be installed and on PATH. We always transcode (rather
// than passthrough) because YouTube uses several audio codecs (Opus, AAC,
// MP4A) and the chosen format depends on availability per video. Forcing
// libopus via FFmpeg keeps the pipeline uniform at minor CPU cost.
package sikasa

import (
	"fmt"
	"os/exec"
)

/*
spawnYouTube fetches a YouTube URL via yt-dlp, pipes the bytes into FFmpeg,
and returns the FFmpeg process whose stdout is Ogg-Opus.

	params:
	      url: any URL yt-dlp can resolve (YouTube, SoundCloud, etc.)
	returns:
	      *ffmpegProcess: kill this to terminate the whole pipeline
	      error:          if yt-dlp or ffmpeg cannot be spawned

Note: We capture the yt-dlp *exec.Cmd inside a wrapper goroutine so that
calling ffmpegProcess.Kill() also tears down yt-dlp through the closed pipe.
*/
func spawnYouTube(url string) (*ffmpegProcess, error) {
	ytArgs := []string{
		"--quiet", "--no-warnings",
		"-f", "bestaudio",
		"-o", "-",
		url,
	}
	yt := exec.Command("yt-dlp", ytArgs...)
	ytStdout, err := yt.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("sikasa: yt-dlp stdout pipe: %w", err)
	}
	if err := yt.Start(); err != nil {
		return nil, fmt.Errorf("sikasa: spawn yt-dlp: %w (is it installed?)", err)
	}

	ff, err := spawnFromStdin(ytStdout)
	if err != nil {
		_ = yt.Process.Kill()
		_ = yt.Wait()
		return nil, err
	}

	// Reap yt-dlp in the background so it does not become a zombie when
	// FFmpeg exits or is killed.
	go func() {
		_ = yt.Wait()
	}()

	return ff, nil
}

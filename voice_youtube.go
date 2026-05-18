// Package sikasa: voice_youtube.go
// Purpose: Spawns yt-dlp to extract a YouTube (or other supported site) audio
// stream and pipes it into FFmpeg for re-encoding to Opus. Also provides a
// metadata probe so callers can show "Title by Uploader" instead of raw URLs.
//
// Key Components:
//   - spawnYouTube():  chains yt-dlp -> FFmpeg, returning a single ffmpegProcess
//     whose stdout yields Ogg-Opus
//   - probeYouTube():  runs yt-dlp --print to fetch (title, uploader) without
//     downloading the audio
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
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
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
	ytArgs := []string{"-x",
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

	// Tie yt-dlp's lifetime to the ffmpegProcess so VoiceCtx.Stop / swap kills
	// both. Without this, yt-dlp can stay alive after ffmpeg exits because it
	// is blocked on a network read rather than the broken pipe; over a long
	// queue this leaks one yt-dlp process per track.
	ff.upstream = yt

	return ff, nil
}

/*
probeYouTubeEntries expands any yt-dlp-resolvable URL into a list of Tracks.
Single videos yield one entry; playlists yield N. Each Track gets Source
(canonical webpage URL), Title, and Author populated. Failures are
non-fatal; an empty result lets the caller fall back to enqueuing the raw
URL as a single track.

	params:
	      url: any URL yt-dlp can resolve
	returns:
	      []Track: one Track per playlist entry, in playlist order
	      error:   only set when yt-dlp itself errors

Note: --flat-playlist makes yt-dlp list playlist entries without fetching
each video's full metadata, so the call is fast even on large playlists
(typically <2s for a 100-entry list). The trade-off: titles/uploaders come
from the playlist index, which is good enough for queue display. Timeout
is 30s to absorb large playlists.
*/
func probeYouTubeEntries(url string) ([]Track, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{
		"--quiet", "--no-warnings",
		"--flat-playlist",
		"--print", "%(webpage_url)s",
		"--print", "%(title)s",
		"--print", "%(uploader)s",
		url,
	}
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sikasa: yt-dlp probe: %w", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	// 3 print lines per entry: webpage_url, title, uploader. Drop trailing
	// partial group if yt-dlp ever emits an odd count (defensive).
	count := len(lines) / 3
	if count == 0 {
		return nil, nil
	}
	tracks := make([]Track, 0, count)
	for i := range count {
		webURL := strings.TrimSpace(lines[i*3])
		title := strings.TrimSpace(lines[i*3+1])
		uploader := strings.TrimSpace(lines[i*3+2])
		if title == "NA" {
			title = ""
		}
		if uploader == "NA" {
			uploader = ""
		}
		// Fall back to original URL when yt-dlp returns "NA" for webpage_url
		// (happens with some live or DRM-protected entries).
		if webURL == "" || webURL == "NA" {
			webURL = url
		}
		tracks = append(tracks, Track{
			Kind:   TrackYouTube,
			Source: webURL,
			Title:  title,
			Author: uploader,
		})
	}
	return tracks, nil
}

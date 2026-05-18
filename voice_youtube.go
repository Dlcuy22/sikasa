// Package sikasa: voice_youtube.go
// Purpose: Spawns yt-dlp to extract a YouTube (or other supported site) audio
// stream and pipes it into FFmpeg. Also provides metadata + search probes so
// callers can show "Title by Uploader" instead of raw URLs and let users
// search by free-text query.
//
// Key Components:
//   - spawnYouTube():        chains yt-dlp -> FFmpeg, returning a single
//                            ffmpegProcess whose stdout yields Ogg-Opus
//   - probeYouTubeEntries(): expands a URL into Track lists (single video,
//                            playlist, or channel)
//   - SearchYouTube():       runs `ytsearch<n>:<query>` to return top-N
//                            candidate Tracks for an interactive picker
//   - IsHTTPURL():           cheap heuristic to decide whether user input is a
//                            URL or a free-text search query
//
// Dependencies:
//   - os/exec: subprocess lifecycle
//
// Note: We *prefer* Opus-only formats from yt-dlp (itag 251, 250, 249) so
// FFmpeg can stream-copy them into the Ogg container with zero re-encoding.
// Pure remux is ~99% cheaper than transcoding and preserves source quality.
// When yt-dlp falls back to a non-Opus codec (rare on YouTube, common on
// other sites), we transcode through libopus as before.
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
spawnYouTube fetches a YouTube URL via yt-dlp, hands the bytes to FFmpeg,
and returns the FFmpeg process whose stdout is Ogg-Opus.

	params:
	      url: any URL yt-dlp can resolve (YouTube, SoundCloud, etc.)
	returns:
	      *ffmpegProcess: kill this to terminate the whole pipeline
	      error:          if yt-dlp or ffmpeg cannot be spawned

Note: We tell yt-dlp to prefer Opus-only formats (itag 251 / 250 / 249) and
fall back to bestaudio. yt-dlp prints the chosen acodec to stderr; we sniff
the format upfront with --print so the FFmpeg pipeline can decide between
stream-copy (free) and transcode (CPU-bound). The yt-dlp *exec.Cmd is tied
to the ffmpegProcess wrapper so killing the wrapper also tears down yt-dlp,
preventing zombies on long queues.
*/
func spawnYouTube(url string) (*ffmpegProcess, error) {
	codec := probeYouTubeCodec(url)

	ytArgs := []string{
		"--quiet", "--no-warnings",
		// Prefer Opus-only itags for a clean stream-copy. Fallback to any
		// audio when no Opus track is available (e.g. some live streams).
		"-f", "251/250/249/bestaudio[acodec=opus]/bestaudio",
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

	var ff *ffmpegProcess
	if codec == "opus" {
		ff, err = spawnRemuxFromStdin(ytStdout)
	} else {
		ff, err = spawnFromStdin(ytStdout)
	}
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
probeYouTubeCodec runs a tiny yt-dlp invocation to learn the audio codec of
the format yt-dlp would actually pick. Used by spawnYouTube to decide
between stream-copy (Opus) and transcode (anything else).

	params:
	      url: any URL yt-dlp can resolve
	returns:
	      string: lower-cased codec name (e.g. "opus", "mp4a", "aac"), or
	              "" when the probe fails (caller should default to transcode)

Note: 5s timeout. If the probe times out, we return "" so the caller picks
the safer transcode path. Probe failure is non-fatal.
*/
func probeYouTubeCodec(url string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	args := []string{
		"--quiet", "--no-warnings",
		"-f", "251/250/249/bestaudio[acodec=opus]/bestaudio",
		"--print", "%(acodec)s",
		"--no-playlist",
		url,
	}
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	codec := strings.ToLower(strings.TrimSpace(out.String()))
	// yt-dlp prints "NA" when it cannot determine the codec.
	if codec == "" || codec == "na" {
		return ""
	}
	return codec
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

/*
IsHTTPURL is a cheap heuristic that decides whether a user-supplied string
should be treated as a URL or a search query. Anything starting with
"http://" or "https://" is considered a URL; everything else is search text.

	params:
	      s: the raw user input
	returns:
	      bool: true when s looks like an HTTP(S) URL
*/
func IsHTTPURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

/*
SearchYouTube runs `ytsearch<n>:<query>` through yt-dlp and returns the top-N
results as Tracks. Useful for an interactive picker; for direct enqueue use
PlayYouTube which expands either a URL or a search.

	params:
	      query: free-text search string (yt-dlp escapes it internally)
	      n:     number of results to return; clamped to [1, 25]
	returns:
	      []Track: candidate tracks in yt-dlp's relevance order
	      error:   yt-dlp invocation error (timeout, missing binary, etc.)

Note: 15s timeout is enough for top-25 search; raise via env if you need
deeper results. Search uses --flat-playlist so we get URL+title+uploader
quickly without fetching each video's full metadata.
*/
func SearchYouTube(query string, n int) ([]Track, error) {
	if n < 1 {
		n = 1
	}
	if n > 25 {
		n = 25
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	args := []string{
		"--quiet", "--no-warnings",
		"--flat-playlist",
		"--print", "%(webpage_url)s",
		"--print", "%(title)s",
		"--print", "%(uploader)s",
		fmt.Sprintf("ytsearch%d:%s", n, q),
	}
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sikasa: yt-dlp search: %w", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
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
		if webURL == "" || webURL == "NA" {
			continue
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

// Package sikasa: voice_youtube.go
// Purpose: Handles YouTube playback stream spawning and search queries.
// Streams raw Opus audio using a simplified pipeline (yt-dlp -> ffmpeg copy),
// and uses the pure-Go ytm-go library for fast search metadata resolving.
//
// Key Components:
//   - spawnYouTube():   Spawns yt-dlp (with optional Bun JS runtime) and ffmpeg to remux Opus
//   - SearchYouTube():  Performs search resolving natively via ytm-go InnerTube client
//   - IsHTTPURL():      Heuristic to check if query is a URL
//   - probeYouTubeEntries(): Extracts info for playlist expansions
//
// Dependencies:
//   - context:          Subprocess lifecycle cancellation
//   - os:               Subprocess detection and file check
//   - os/exec:          Subprocess execution
//   - path/filepath:    Cross-platform paths
//   - github.com/dlcuy22/ytm-go: Natively resolves YouTube Music metadata
//
package sikasa

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dlcuy22/ytm-go"
)

/*
spawnYouTube fetches a YouTube URL via yt-dlp, hands the bytes to FFmpeg,
and returns the FFmpeg process whose stdout is Ogg-Opus.

    params:
          url: any URL yt-dlp can resolve (YouTube, SoundCloud, etc.)
          mode: the remuxing strategy to use (FFmpeg subprocess or Native purego)
    returns:
          *ffmpegProcess: kill this to terminate the whole pipeline
          error:          if yt-dlp or ffmpeg cannot be spawned
*/
func spawnYouTube(url string, mode RemuxMode) (*ffmpegProcess, error) {
	// Look for a local Bun installation to speed up signature decryption.
	bunPath := ""
	home, err := os.UserHomeDir()
	if err == nil {
		bp := filepath.Join(home, ".bun", "bin", "bun")
		if _, err := os.Stat(bp); err == nil {
			bunPath = bp
		}
	}

	ytArgs := []string{
		"--quiet", "--no-warnings",
		"-f", "251/250/249", // Force Opus formats only
		"-o", "-",
	}
	if bunPath != "" {
		ytArgs = append(ytArgs, "--js-runtimes", "bun:"+bunPath)
	}
	ytArgs = append(ytArgs, url)

	remuxNativeSupported := false
	if mode == RemuxNative {
		if err := initNativeRemuxer(); err == nil {
			remuxNativeSupported = true
		} else {
			log.Printf("sikasa: native remuxing not available; falling back to ffmpeg: %v", err)
		}
	}

	if remuxNativeSupported {
		yt := exec.Command(ResolveBinaryPath("yt-dlp"), ytArgs...)
		ytStdout, err := yt.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("sikasa: yt-dlp stdout pipe: %w", err)
		}
		if err := yt.Start(); err != nil {
			return nil, fmt.Errorf("sikasa: spawn yt-dlp: %w (is it installed?)", err)
		}

		pr, pw := io.Pipe()
		go func() {
			remuxErr := RemuxStreamToWriter(ytStdout, pw)
			if remuxErr != nil {
				log.Printf("sikasa: native remux error: %v", remuxErr)
				_ = pw.CloseWithError(remuxErr)
			} else {
				_ = pw.Close()
			}
		}()

		return &ffmpegProcess{
			stdout:   pr,
			upstream: yt,
		}, nil
	}

	yt := exec.Command(ResolveBinaryPath("yt-dlp"), ytArgs...)
	ytStdout, err := yt.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("sikasa: yt-dlp stdout pipe: %w", err)
	}
	if err := yt.Start(); err != nil {
		return nil, fmt.Errorf("sikasa: spawn yt-dlp: %w (is it installed?)", err)
	}

	// Since we force Opus format in yt-dlp, we can always spawn in stream-copy/remux mode.
	ff, err := spawnRemuxFromStdin(ytStdout)
	if err != nil {
		_ = yt.Process.Kill()
		_ = yt.Wait()
		return nil, err
	}

	ff.upstream = yt
	return ff, nil
}

/*
probeYouTubeEntries expands any yt-dlp-resolvable URL into a list of Tracks.
Single videos yield one entry; playlists yield N.

    params:
          url: any URL yt-dlp can resolve
    returns:
          []Track: one Track per playlist entry, in playlist order
          error:   only set when yt-dlp itself errors
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
	cmd := exec.CommandContext(ctx, ResolveBinaryPath("yt-dlp"), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sikasa: yt-dlp probe: %w", err)
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
should be treated as a URL or a search query.

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
SearchYouTube queries YouTube Music using the Go-native ytm-go library
and returns the top-N results as Tracks. Bypasses yt-dlp for search.

    params:
          query: free-text search string
          n:     number of results to return
    returns:
          []Track: candidate tracks in relevance order
          error:   innerTube query error
*/
func SearchYouTube(query string, n int) ([]Track, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	client := ytm.NewClient()
	results, err := client.Search(context.Background(), q, "", false)
	if err != nil {
		return nil, fmt.Errorf("ytm-go search: %w", err)
	}

	var tracks []Track
	for _, cat := range results.Categories {
		for _, item := range cat.Layout.Items {
			if len(tracks) >= n {
				break
			}
			switch s := item.(type) {
			case *ytm.Song:
				var artists []string
				for _, a := range s.Artists {
					artists = append(artists, a.Name)
				}
				tracks = append(tracks, Track{
					Kind:   TrackYouTube,
					Source: "https://www.youtube.com/watch?v=" + s.ID,
					Title:  s.Name,
					Author: strings.Join(artists, ", "),
				})
			}
		}
	}
	return tracks, nil
}

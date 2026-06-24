// Package sikasa: songcache.go
// Purpose: Implements an asynchronous, sequential audio prefetcher and sliding-window
// cache manager for YouTube tracks. Supports persistent local caches, prioritizing
// active tracks, and evicting out-of-window cache files.
//
// Key Components:
//   - getCachePath(): Computes the MD5 filename for cache files.
//   - prefetchTrack(): Downloads YouTube streams and remuxes them to Ogg files.
//   - prefetchWorker(): Background goroutine that processes prefetches sequentially.
//   - getNextPrefetchTrack(): Selects the next prioritized track that needs caching.
//   - notifyPrefetch(): Wakes up the background worker.
//   - triggerPrefetch(): Recalculates sliding window, triggers worker, and evicts expired cache files.
//
// Dependencies:
//   - context: Handling cancellation.
//   - crypto/md5: Generating unique cache keys.
//   - os: Creating directories and managing files.
//   - os/exec: Running yt-dlp and ffmpeg.
//   - path/filepath: Building cross-platform paths.
//
package sikasa

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

/*
getCachePath computes the target Ogg-Opus cache file path for a given URL.

    params:
          url: the canonical YouTube URL
    returns:
          string: the absolute path to the cached Ogg file
*/
func (b *Bot) getCachePath(url string) string {
	hash := md5.Sum([]byte(url))
	filename := fmt.Sprintf("%x.ogg", hash)
	return filepath.Join(b.cacheDir, filename)
}

/*
prefetchTrack downloads a YouTube audio stream, remuxes it to an Ogg file,
and saves it in the cache directory. Automatically utilizes Bun if available.

    params:
          parentCtx: parent context for lifecycle cancellation
          url:       the canonical YouTube URL to download
          cachePath: the target destination path of the Ogg file
*/
func (b *Bot) prefetchTrack(parentCtx context.Context, url string, cachePath string) {
	b.cacheMu.Lock()
	if !b.cacheEnabled {
		b.cacheMu.Unlock()
		return
	}
	if _, exists := b.cacheActive[url]; exists {
		b.cacheMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parentCtx)
	b.cacheActive[url] = cancel
	b.cacheMu.Unlock()

	defer func() {
		b.cacheMu.Lock()
		delete(b.cacheActive, url)
		b.cacheMu.Unlock()
		cancel()
	}()

	if err := os.MkdirAll(b.cacheDir, 0755); err != nil {
		b.logger.Printf("sikasa: cache directory creation failed: %v", err)
		return
	}

	b.vlog().Info("voice: prefetching track", "url", url)

	tmpPath := cachePath + ".tmp"

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
		"-f", "251/250/249", // Force high-quality Opus audio formats
		"-o", "-",
	}
	if bunPath != "" {
		ytArgs = append(ytArgs, "--js-runtimes", "bun:"+bunPath)
	}
	ytArgs = append(ytArgs, url)

	yt := exec.CommandContext(ctx, "yt-dlp", ytArgs...)
	ytStdout, err := yt.StdoutPipe()
	if err != nil {
		b.vlog().Error("voice: prefetch yt-dlp pipe failed", "url", url, "err", err)
		return
	}

	remuxDone := false
	if b.remuxMode == RemuxNative {
		if err := yt.Start(); err == nil {
			remuxErr := RemuxStream(ytStdout, tmpPath)
			_ = yt.Process.Kill()
			_ = yt.Wait()
			if remuxErr == nil {
				remuxDone = true
			} else {
				b.vlog().Error("voice: native prefetch remux failed; falling back to ffmpeg", "url", url, "err", remuxErr)
				os.Remove(tmpPath)
				// Re-prepare yt-dlp for fallback
				yt = exec.CommandContext(ctx, "yt-dlp", ytArgs...)
				ytStdout, err = yt.StdoutPipe()
				if err != nil {
					b.vlog().Error("voice: prefetch fallback yt-dlp pipe failed", "url", url, "err", err)
					return
				}
			}
		} else {
			b.vlog().Error("voice: prefetch spawn yt-dlp failed for native remux", "url", url, "err", err)
		}
	}

	if !remuxDone {
		// Remux WebM/Opus format into an Ogg container on-the-fly using FFmpeg subprocess.
		ffArgs := []string{
			"-hide_banner", "-loglevel", "error",
			"-i", "pipe:0",
			"-vn",
			"-c:a", "copy",
			"-f", "ogg",
			"pipe:1",
		}
		ff := exec.CommandContext(ctx, "ffmpeg", ffArgs...)
		ff.Stdin = ytStdout
		ffStdout, err := ff.StdoutPipe()
		if err != nil {
			b.vlog().Error("voice: prefetch ffmpeg pipe failed", "url", url, "err", err)
			return
		}

		outFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			b.vlog().Error("voice: prefetch write file open failed", "url", url, "err", err)
			return
		}
		defer outFile.Close()

		if err := yt.Start(); err != nil {
			b.vlog().Error("voice: prefetch spawn yt-dlp failed", "url", url, "err", err)
			os.Remove(tmpPath)
			return
		}
		defer func() {
			_ = yt.Process.Kill()
			_ = yt.Wait()
		}()

		if err := ff.Start(); err != nil {
			b.vlog().Error("voice: prefetch spawn ffmpeg failed", "url", url, "err", err)
			os.Remove(tmpPath)
			return
		}
		defer func() {
			_ = ff.Process.Kill()
			_ = ff.Wait()
		}()

		// Stream remuxed audio from ffmpeg stdout to our temporary file.
		_, err = io.Copy(outFile, ffStdout)
		_ = outFile.Close()

		_ = ff.Wait()
		_ = yt.Wait()
	}

	if err == nil && ctx.Err() == nil {
		// Verify file size is non-zero before concluding success.
		if fi, errStat := os.Stat(tmpPath); errStat == nil && fi.Size() > 0 {
			if errRename := os.Rename(tmpPath, cachePath); errRename == nil {
				b.vlog().Info("voice: prefetch finished", "url", url, "path", cachePath)
			} else {
				b.vlog().Error("voice: prefetch rename failed", "url", url, "err", errRename)
				os.Remove(tmpPath)
			}
		} else {
			b.vlog().Error("voice: prefetch empty file", "url", url)
			os.Remove(tmpPath)
		}
	} else {
		if ctx.Err() != nil {
			b.vlog().Info("voice: prefetch cancelled", "url", url)
		} else {
			b.vlog().Error("voice: prefetch copy failed", "url", url, "err", err)
		}
		os.Remove(tmpPath)
	}
}

/*
prefetchWorker is the background loop that processes prefetches sequentially.
*/
func (b *Bot) prefetchWorker() {
	for {
		select {
		case <-b.prefetchCtx.Done():
			return
		case <-b.prefetchNotify:
			for b.cacheEnabled {
				url, cachePath, found := b.getNextPrefetchTrack()
				if !found {
					break
				}
				b.prefetchTrack(b.prefetchCtx, url, cachePath)
			}
		}
	}
}

/*
getNextPrefetchTrack retrieves the next prioritized track that needs caching
across all active voice sessions.
*/
func (b *Bot) getNextPrefetchTrack() (string, string, bool) {
	// Priority order of distances: 0, 1, 2, ..., maxAhead, -1
	var distances []int
	for d := 0; d <= b.cacheMaxAhead; d++ {
		distances = append(distances, d)
	}
	distances = append(distances, -1)

	b.voicesMu.Lock()
	defer b.voicesMu.Unlock()

	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()

	for _, d := range distances {
		for _, v := range b.voices {
			v.mu.Lock()
			cursor := v.queue.Cursor()
			tracks := v.queue.Tracks()
			v.mu.Unlock()

			idx := cursor + d
			if idx < 0 || idx >= len(tracks) {
				continue
			}
			t := tracks[idx]
			if t.Kind != TrackYouTube {
				continue
			}
			cachePath := b.getCachePath(t.Source)
			// Check if already cached
			if _, err := os.Stat(cachePath); err == nil {
				continue
			}
			// Check if already downloading
			if _, active := b.cacheActive[t.Source]; active {
				continue
			}
			return t.Source, cachePath, true
		}
	}
	return "", "", false
}

/*
notifyPrefetch triggers the sequential prefetch worker if caching is enabled.
*/
func (b *Bot) notifyPrefetch() {
	if !b.cacheEnabled {
		return
	}
	select {
	case b.prefetchNotify <- struct{}{}:
	default:
	}
}

/*
triggerPrefetch recalculates the sliding window of tracks to keep in the local
cache, initiates downloads for future tracks, cancels out-of-window downloads,
and deletes expired cache files from disk.
*/
func (v *VoiceCtx) triggerPrefetch() {
	if !v.bot.cacheEnabled {
		return
	}

	keep := make(map[string]bool)
	v.bot.voicesMu.Lock()
	for _, gCtx := range v.bot.voices {
		gCtx.mu.Lock()
		curCursor := gCtx.queue.Cursor()
		curTracks := gCtx.queue.Tracks()
		gCtx.mu.Unlock()

		cStart := max(0, curCursor-1)
		cEnd := min(len(curTracks)-1, curCursor+v.bot.cacheMaxAhead)

		for i := cStart; i <= cEnd; i++ {
			t := curTracks[i]
			if t.Kind == TrackYouTube {
				cPath := v.bot.getCachePath(t.Source)
				keep[cPath] = true
			}
		}
	}
	v.bot.voicesMu.Unlock()

	v.bot.cacheMu.Lock()
	for url, cancel := range v.bot.cacheActive {
		cachePath := v.bot.getCachePath(url)
		if !keep[cachePath] {
			cancel()
		}
	}
	v.bot.cacheMu.Unlock()

	go func() {
		files, err := os.ReadDir(v.bot.cacheDir)
		if err != nil {
			return
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			if strings.HasSuffix(name, ".ogg") {
				fullPath := filepath.Join(v.bot.cacheDir, name)
				if !keep[fullPath] {
					_ = os.Remove(fullPath)
				}
			}
		}
	}()

	v.bot.notifyPrefetch()
}

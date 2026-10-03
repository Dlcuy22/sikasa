// Package sikasa: songcache.go
// Purpose: Implements an asynchronous, sequential audio prefetcher and sliding-
// window cache manager for YouTube tracks. Supports persistent local caches,
// prioritizing active tracks, and evicting out-of-window cache files. Tracks
// are fetched from the direct media URL ytm-go resolves and framed into Ogg
// with the pure-Go WebM/Opus reader, so no external process is involved.
//
// Key Components:
//   - getCachePath(): Computes the MD5 filename for cache files.
//   - prefetchTrack(): Fetches a YouTube stream and writes it as an Ogg file.
//   - prefetchWorker(): Background goroutine that processes prefetches sequentially.
//   - getNextPrefetchTrack(): Selects the next prioritized track that needs caching.
//   - notifyPrefetch(): Wakes up the background worker.
//   - triggerPrefetch(): Recalculates sliding window, triggers worker, and evicts expired cache files.
//
// Dependencies:
//   - context: Handling cancellation.
//   - crypto/md5: Generating unique cache keys.
//   - os: Creating directories and managing files.
//   - path/filepath: Building cross-platform paths.
package sikasa

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"os"
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
	safeName := fmt.Sprintf("%x", hash)
	return filepath.Join(b.config.Cache.Dir, safeName+".ogg")
}

/*
prefetchTrack fetches a YouTube track's WebM body and frames its Opus packets
into an Ogg file saved in the cache directory.

	params:
	      parentCtx: parent context for lifecycle cancellation
	      url:       the canonical YouTube URL to download
	      cachePath: the target destination path of the Ogg file
*/
func (b *Bot) prefetchTrack(parentCtx context.Context, url string, cachePath string) {
	b.cacheMu.Lock()
	if !b.config.Cache.Enabled {
		b.cacheMu.Unlock()
		return
	}
	if _, exists := b.cacheActive[url]; exists {
		b.cacheMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parentCtx)
	doneChan := make(chan struct{})
	b.cacheActive[url] = activePrefetch{
		cancel: cancel,
		done:   doneChan,
	}
	b.cacheMu.Unlock()

	defer func() {
		b.cacheMu.Lock()
		if act, ok := b.cacheActive[url]; ok {
			close(act.done)
		}
		delete(b.cacheActive, url)
		b.cacheMu.Unlock()
		cancel()
	}()

	if err := os.MkdirAll(b.config.Cache.Dir, 0755); err != nil {
		b.logger.Printf("sikasa: cache directory creation failed: %v", err)
		return
	}

	b.vlog().Info("voice: prefetching track", "url", url)

	tmpPath := cachePath + ".tmp"

	src, err := openYouTubeSource(ctx, url)
	if err != nil {
		b.vlog().Error("voice: prefetch resolve/fetch failed", "url", url, "err", err)
		return
	}
	defer src.Close()

	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		b.vlog().Error("voice: prefetch write file open failed", "url", url, "err", err)
		return
	}

	if err := writeOggOpus(src, out); err != nil {
		_ = out.Close()
		os.Remove(tmpPath)
		if ctx.Err() != nil {
			b.vlog().Info("voice: prefetch cancelled", "url", url)
		} else {
			b.vlog().Error("voice: prefetch remux failed", "url", url, "err", err)
		}
		return
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		os.Remove(tmpPath)
		b.vlog().Error("voice: prefetch sync failed", "url", url, "err", err)
		return
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpPath)
		b.vlog().Error("voice: prefetch close failed", "url", url, "err", err)
		return
	}

	// Verify file size is non-zero before concluding success.
	if fi, errStat := os.Stat(tmpPath); errStat != nil || fi.Size() == 0 {
		b.vlog().Error("voice: prefetch empty file", "url", url)
		os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		b.vlog().Error("voice: prefetch rename failed", "url", url, "err", err)
		os.Remove(tmpPath)
		return
	}
	b.vlog().Info("voice: prefetch finished", "url", url, "path", cachePath)
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
			for b.config.Cache.Enabled {
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
	for d := 0; d <= b.config.Cache.MaxAhead; d++ {
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
	if !b.config.Cache.Enabled {
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
	if !v.bot.config.Cache.Enabled {
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
		cEnd := min(len(curTracks)-1, curCursor+v.bot.config.Cache.MaxAhead)

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
	for url, act := range v.bot.cacheActive {
		cachePath := v.bot.getCachePath(url)
		if !keep[cachePath] {
			act.cancel()
		}
	}
	v.bot.cacheMu.Unlock()

	go func() {
		files, err := os.ReadDir(v.bot.config.Cache.Dir)
		if err != nil {
			return
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			if strings.HasSuffix(name, ".ogg") {
				fullPath := filepath.Join(v.bot.config.Cache.Dir, name)
				if !keep[fullPath] {
					_ = os.Remove(fullPath)
				}
			}
		}
	}()

	v.bot.notifyPrefetch()
}

// writeOggOpus frames every Opus packet from src into a minimal Ogg Opus stream
// written to w. This is the pure-Go replacement for the former yt-dlp + ffmpeg
// remux, and it is only used to build the on-disk cache.
func writeOggOpus(src opusSource, w io.Writer) error {
	enc := newOggOpusEncoder(w)

	for {
		pkt, err := src.ReadPacket()
		if err != nil {
			if err == io.EOF {
				break
			}

			return err
		}
		if err := enc.WritePacket(pkt); err != nil {
			return err
		}
	}

	return enc.Finish()
}

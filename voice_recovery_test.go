// Package sikasa: voice_recovery_test.go
// Purpose: Implements unit tests for voice prefetch waiting logic and
// reconnect failure process recovery triggers.
//
// Key Components:
//   - TestVoice_PrefetchWait(): Verifies that spawnTrack blocks on active prefetch and plays from cache
//   - TestVoice_ReconnectRestartTrigger(): Verifies that 3 reconnect failures trigger a hard restart
//
// Dependencies:
//   - testing: standard Go testing framework
//
package sikasa

import (
	"os"
	"testing"
	"time"
)

/*
TestVoice_PrefetchWait verifies that when a track is currently being prefetched,
spawnTrack waits for the download to complete and plays from the cache.
*/
func TestVoice_PrefetchWait(t *testing.T) {
	bot, err := New("dummy_token")
	if err != nil {
		t.Fatalf("failed to create bot: %v", err)
	}
	tmpDir := t.TempDir()
	bot.WithCache(tmpDir, 3)

	vctx := &VoiceCtx{
		bot:   bot,
		queue: newQueue(),
		log:   bot.vlog(),
	}

	url := "https://www.youtube.com/watch?v=prefetch_test"
	cachePath := bot.getCachePath(url)

	// Set the track as actively prefetching
	doneChan := make(chan struct{})
	bot.cacheMu.Lock()
	bot.cacheActive[url] = activePrefetch{
		cancel: func() {},
		done:   doneChan,
	}
	bot.cacheMu.Unlock()

	// Run spawnTrack in a separate goroutine as it should block
	resChan := make(chan *ffmpegProcess, 1)
	errChan := make(chan error, 1)

	go func() {
		proc, err := vctx.spawnTrack(Track{Kind: TrackYouTube, Source: url})
		if err != nil {
			errChan <- err
			return
		}
		resChan <- proc
	}()

	// Verify it is blocking (doneChan not closed yet)
	select {
	case <-resChan:
		t.Fatal("expected spawnTrack to block while prefetching is active")
	case <-errChan:
		t.Fatal("expected spawnTrack to block while prefetching is active")
	case <-time.After(100 * time.Millisecond):
		// Success: it blocked
	}

	// Write dummy data to simulate completed download
	if err := os.WriteFile(cachePath, []byte("dummy ogg data"), 0644); err != nil {
		t.Fatalf("failed to write dummy cache file: %v", err)
	}

	// Signal prefetch completion
	close(doneChan)

	// Verify it successfully recovers and plays from cache
	select {
	case proc := <-resChan:
		if proc == nil {
			t.Fatal("expected non-nil ffmpegProcess")
		}
		proc.Kill()
	case err := <-errChan:
		t.Fatalf("expected spawnTrack to succeed, got error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for spawnTrack to complete after prefetch finished")
	}
}

/*
TestVoice_ReconnectRestartTrigger verifies that 3 reconnect failures trigger
the hard restart callback on the bot.
*/
func TestVoice_ReconnectRestartTrigger(t *testing.T) {
	bot, err := New("dummy_token")
	if err != nil {
		t.Fatalf("failed to create bot: %v", err)
	}

	vctx := &VoiceCtx{
		bot: bot,
		log: bot.vlog(),
	}

	restarted := false
	bot.onRestart = func() {
		restarted = true
	}

	// Trigger 2 failures, should not restart
	vctx.incrementFailuresAndCheck()
	vctx.incrementFailuresAndCheck()
	if restarted {
		t.Fatal("expected bot not to restart after 2 failures")
	}

	// Trigger 3rd failure, should trigger restart
	vctx.incrementFailuresAndCheck()
	if !restarted {
		t.Fatal("expected bot to restart after 3 failures")
	}
}

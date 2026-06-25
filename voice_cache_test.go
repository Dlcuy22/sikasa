// Package sikasa: voice_cache_test.go
// Purpose: Implements unit tests for the Cache configuration and hashing.
//
// Key Components:
//   - TestCache_Configuration(): Verifies default configuration and fluent setters
//   - TestCache_PathHashing(): Verifies cache path MD5 hashing
//
// Dependencies:
//   - testing: standard Go testing framework
package sikasa

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

/*
TestCache_Configuration verifies the default caching config and fluent setters.

	params:
	      t: test runner context
*/
func TestCache_Configuration(t *testing.T) {
	bot, err := New("dummy_token")
	if err != nil {
		t.Fatalf("failed to create bot: %v", err)
	}

	// Verify defaults
	if bot.cacheDir != "sikasa-data/audiocache" {
		t.Errorf("expected default cacheDir to be 'sikasa-data/audiocache', got %q", bot.cacheDir)
	}
	if bot.cacheMaxAhead != 3 {
		t.Errorf("expected default cacheMaxAhead to be 3, got %d", bot.cacheMaxAhead)
	}
	if !bot.cacheEnabled {
		t.Error("expected caching to be enabled by default")
	}

	// Test fluent WithCache
	bot.WithCache("custom-cache", 5)
	if bot.cacheDir != "custom-cache" {
		t.Errorf("expected cacheDir to be 'custom-cache', got %q", bot.cacheDir)
	}
	if bot.cacheMaxAhead != 5 {
		t.Errorf("expected cacheMaxAhead to be 5, got %d", bot.cacheMaxAhead)
	}
	if !bot.cacheEnabled {
		t.Error("expected cache to remain enabled after WithCache")
	}

	// Test fluent WithoutCache
	bot.WithoutCache()
	if bot.cacheEnabled {
		t.Error("expected cache to be disabled after WithoutCache")
	}
}

/*
TestCache_PathHashing verifies MD5 hashing and output path generation.

	params:
	      t: test runner context
*/
func TestCache_PathHashing(t *testing.T) {
	bot, err := New("dummy_token")
	if err != nil {
		t.Fatalf("failed to create bot: %v", err)
	}

	bot.WithCache("sikasa-data/audiocache", 3)
	url := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	gotPath := bot.getCachePath(url)

	wantFilename := "75170fc230cd88f32e475ff4087f81d9.ogg"
	wantPath := filepath.Join("sikasa-data", "audiocache", wantFilename)

	if gotPath != wantPath {
		t.Errorf("getCachePath(%q) = %q, want %q", url, gotPath, wantPath)
	}
}

/*
TestCache_SlidingWindow verifies that the prefetch sliding window works correctly
under queue advances, keeping in-window files and evicting out-of-window files.

	params:
	      t: test runner context
*/
func TestCache_SlidingWindow(t *testing.T) {
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
	bot.voicesMu.Lock()
	bot.voices[1] = vctx
	bot.voicesMu.Unlock()

	var urls []string
	for i := 0; i < 10; i++ {
		url := fmt.Sprintf("https://www.youtube.com/watch?v=track%d", i)
		urls = append(urls, url)
		vctx.queue.Add(Track{Kind: TrackYouTube, Source: url})

		path := bot.getCachePath(url)
		if err := os.WriteFile(path, []byte("dummy raw ogg data"), 0644); err != nil {
			t.Fatalf("failed to write dummy cache file: %v", err)
		}
	}

	exists := func(url string) bool {
		_, err := os.Stat(bot.getCachePath(url))
		return err == nil
	}

	// Move cursor to 0 (playing track 0)
	vctx.queue.Advance()

	vctx.triggerPrefetch()
	time.Sleep(50 * time.Millisecond)

	// Sliding window [0-1, 0+3] = [0, 3]. Tracks 0, 1, 2, 3 must be kept.
	for i := 0; i <= 3; i++ {
		if !exists(urls[i]) {
			t.Errorf("expected track %d to exist in cache at cursor 0", i)
		}
	}
	for i := 4; i < 10; i++ {
		if exists(urls[i]) {
			t.Errorf("expected track %d to be deleted from cache at cursor 0", i)
		}
	}

	// Restore files and advance cursor to 4
	for i := 0; i < 10; i++ {
		path := bot.getCachePath(urls[i])
		_ = os.WriteFile(path, []byte("dummy raw ogg data"), 0644)
	}

	vctx.queue.Advance() // 1
	vctx.queue.Advance() // 2
	vctx.queue.Advance() // 3
	vctx.queue.Advance() // 4

	vctx.triggerPrefetch()
	time.Sleep(50 * time.Millisecond)

	// Sliding window [4-1, 4+3] = [3, 7]. Tracks 3, 4, 5, 6, 7 must be kept.
	for i := 3; i <= 7; i++ {
		if !exists(urls[i]) {
			t.Errorf("expected track %d to exist in cache at cursor 4", i)
		}
	}
	for _, i := range []int{0, 1, 2, 8, 9} {
		if exists(urls[i]) {
			t.Errorf("expected track %d to be deleted from cache at cursor 4", i)
		}
	}
}

/*
TestCache_Shuffle verifies that shuffling the queue correctly recalibrates the sliding window
caching priorities and evicts now out-of-window files.

	params:
	      t: test runner context
*/
func TestCache_Shuffle(t *testing.T) {
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
	bot.voicesMu.Lock()
	bot.voices[1] = vctx
	bot.voicesMu.Unlock()

	var urls []string
	for i := 0; i < 10; i++ {
		url := fmt.Sprintf("https://www.youtube.com/watch?v=track%d", i)
		urls = append(urls, url)
		vctx.queue.Add(Track{Kind: TrackYouTube, Source: url})

		path := bot.getCachePath(url)
		if err := os.WriteFile(path, []byte("dummy raw ogg data"), 0644); err != nil {
			t.Fatalf("failed to write dummy cache file: %v", err)
		}
	}

	exists := func(url string) bool {
		_, err := os.Stat(bot.getCachePath(url))
		return err == nil
	}

	// Move cursor to 0 (playing track 0)
	vctx.queue.Advance()

	// Shuffle
	if err := vctx.Shuffle(); err != nil {
		t.Fatalf("failed to shuffle queue: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	shuffledTracks := vctx.queue.Tracks()

	// Tracks at indices 0, 1, 2, 3 in the shuffled queue must be kept.
	for i := 0; i <= 3; i++ {
		tr := shuffledTracks[i]
		if !exists(tr.Source) {
			t.Errorf("expected shuffled track at index %d (%s) to be kept in cache", i, tr.Source)
		}
	}
	// Tracks at indices 4 to 9 in the shuffled queue must be deleted.
	for i := 4; i < 10; i++ {
		tr := shuffledTracks[i]
		if exists(tr.Source) {
			t.Errorf("expected shuffled track at index %d (%s) to be deleted from cache", i, tr.Source)
		}
	}
}

/*
TestBot_RemuxModeConfiguration verifies configuring RemuxMode on Bot and VoiceCtx.

	params:
	      t: test runner context
*/
func TestBot_RemuxModeConfiguration(t *testing.T) {
	bot, err := New("dummy_token")
	if err != nil {
		t.Fatalf("failed to create bot: %v", err)
	}

	// 1. Verify default
	if bot.remuxMode != RemuxNativeGo {
		t.Errorf("expected default remuxMode to be %q, got %q", RemuxNativeGo, bot.remuxMode)
	}

	// 2. Test Bot.WithRemuxMode
	bot.WithRemuxMode("native")
	if bot.remuxMode != RemuxNative {
		t.Errorf("expected remuxMode to be %q after setting to native, got %q", RemuxNative, bot.remuxMode)
	}

	bot.WithRemuxMode("invalid-mode")
	if bot.remuxMode != RemuxNativeGo {
		t.Errorf("expected invalid mode to fallback to %q, got %q", RemuxNativeGo, bot.remuxMode)
	}

	// 3. Test native-go mode
	bot.WithRemuxMode("native-go")
	if bot.remuxMode != RemuxNativeGo {
		t.Errorf("expected remuxMode to be %q after setting to native-go, got %q", RemuxNativeGo, bot.remuxMode)
	}

	// 4. Test VoiceCtx inheritance and WithRemuxMode
	bot.WithRemuxMode("native")
	vctx := &VoiceCtx{
		bot:       bot,
		remuxMode: bot.remuxMode,
	}

	if vctx.remuxMode != RemuxNative {
		t.Errorf("expected VoiceCtx to inherit remuxMode %q, got %q", RemuxNative, vctx.remuxMode)
	}

	vctx.WithRemuxMode("ffmpeg")
	if vctx.remuxMode != RemuxFFmpeg {
		t.Errorf("expected VoiceCtx remuxMode to be set to %q, got %q", RemuxFFmpeg, vctx.remuxMode)
	}

	vctx.WithRemuxMode("native")
	if vctx.remuxMode != RemuxNative {
		t.Errorf("expected VoiceCtx remuxMode to be set to %q, got %q", RemuxNative, vctx.remuxMode)
	}

	vctx.WithRemuxMode("some-other-mode")
	if vctx.remuxMode != RemuxNativeGo {
		t.Errorf("expected VoiceCtx invalid mode to fallback to %q, got %q", RemuxNativeGo, vctx.remuxMode)
	}

	vctx.WithRemuxMode("native-go")
	if vctx.remuxMode != RemuxNativeGo {
		t.Errorf("expected VoiceCtx remuxMode to be set to %q, got %q", RemuxNativeGo, vctx.remuxMode)
	}
}

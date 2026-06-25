// Package sikasa: voice_provider_test.go
// Purpose: Implements unit tests for streamProvider, pause/resume behavior,
// and process memory checking.
//
// Key Components:
//   - TestStreamProvider_PauseResume(): Verifies pause/resume flow
//   - TestStreamProvider_LogSpawnedMemory(): Verifies memory safety
//
// Dependencies:
//   - testing: standard Go testing framework
package sikasa

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
)

type dummyReadCloser struct {
	io.Reader
}

/*
Close implements the Close method on the dummy reader.

	returns:
	      error: nil error
*/
func (dummyReadCloser) Close() error { return nil }

/*
TestStreamProvider_PauseResume checks that paused providers yield silence.

	params:
	      t: test runner context
*/
func TestStreamProvider_PauseResume(t *testing.T) {
	dummyData := []byte("OggS...")
	buf := bytes.NewBuffer(dummyData)
	proc := &ffmpegProcess{
		stdout: dummyReadCloser{buf},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prov := newStreamProvider(proc, logger, 5*time.Second)

	prov.SetPaused(true)
	if !prov.IsPaused() {
		t.Error("expected provider to be paused")
	}

	frame, err := prov.ProvideOpusFrame()
	if err != nil {
		t.Fatalf("ProvideOpusFrame failed: %v", err)
	}
	if !bytes.Equal(frame, voice.SilenceAudioFrame) {
		t.Error("expected silence frame when paused")
	}

	prov.SetPaused(false)
	if prov.IsPaused() {
		t.Error("expected provider to be resumed")
	}
}

/*
TestStreamProvider_LogSpawnedMemory checks that memory check handles nil proc.

	params:
	      t: test runner context
*/
func TestStreamProvider_LogSpawnedMemory(t *testing.T) {
	prov := &streamProvider{proc: nil}
	mem := prov.logSpawnedMemory()
	if mem != 0 {
		t.Errorf("expected 0 memory with nil proc, got %d", mem)
	}

	prov.proc = &ffmpegProcess{}
	mem = prov.logSpawnedMemory()
	if mem != 0 {
		t.Errorf("expected 0 memory with uninitialized process, got %d", mem)
	}
}

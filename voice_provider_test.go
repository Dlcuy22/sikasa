// Package sikasa: voice_provider_test.go
// Purpose: Implements unit tests for streamProvider pause/resume behavior.
//
// Key Components:
//   - TestStreamProvider_PauseResume(): Verifies pause/resume flow
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

// fakeOpusSource is a minimal opusSource over a fixed set of packets.
type fakeOpusSource struct {
	packets [][]byte
	idx     int
	closed  bool
}

func (f *fakeOpusSource) ReadPacket() ([]byte, error) {
	if f.idx >= len(f.packets) {
		return nil, io.EOF
	}
	pkt := f.packets[f.idx]
	f.idx++

	return pkt, nil
}

func (f *fakeOpusSource) Close() error {
	f.closed = true

	return nil
}

/*
TestStreamProvider_PauseResume checks that paused providers yield silence.

	params:
	      t: test runner context
*/
func TestStreamProvider_PauseResume(t *testing.T) {
	src := &fakeOpusSource{packets: [][]byte{{0x00}}}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prov := newStreamProvider(src, logger, 5*time.Second)

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

	// A resumed provider returns the queued packet, then EOF.
	frame, err = prov.ProvideOpusFrame()
	if err != nil {
		t.Fatalf("ProvideOpusFrame failed: %v", err)
	}
	if !bytes.Equal(frame, []byte{0x00}) {
		t.Errorf("expected the queued packet, got %v", frame)
	}
	if _, err := prov.ProvideOpusFrame(); err != io.EOF {
		t.Errorf("expected io.EOF at end of stream, got %v", err)
	}
}

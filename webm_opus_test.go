// Package sikasa: webm_opus_test.go
// Purpose: Unit tests for the pure-Go WebM/Opus reader, the Ogg/Opus reader and
// writer round-trip, and the YouTube reference parser.
//
// Dependencies:
//   - bytes, os, testing
package sikasa

import (
	"bytes"
	"os"
	"testing"
)

// TestWebMOpusReader_FramesTinyWebm verifies that the reader parses the bundled
// WebM fixture and yields Opus packets.
func TestWebMOpusReader_FramesTinyWebm(t *testing.T) {
	f, err := os.Open("tiny.webm")
	if err != nil {
		t.Skipf("tiny.webm not found: %v", err)
	}
	defer f.Close()

	r, err := newWebMOpusReader(f)
	if err != nil {
		t.Fatalf("newWebMOpusReader: %v", err)
	}
	if r.Channels() == 0 {
		t.Error("expected a non-zero channel count")
	}

	packets := 0
	for {
		pkt, err := r.ReadPacket()
		if err != nil {
			break
		}
		if len(pkt) == 0 {
			t.Error("expected a non-empty Opus packet")
		}
		packets++
	}
	if packets == 0 {
		t.Fatal("expected at least one Opus packet from tiny.webm")
	}
}

// TestOggOpusRoundTrip writes Opus packets to an Ogg stream with the encoder and
// reads them back with the reader, asserting byte-for-byte equality.
func TestOggOpusRoundTrip(t *testing.T) {
	want := [][]byte{
		{0xFC, 0x01, 0x02, 0x03},
		{0xFC, 0xAA, 0xBB},
		make([]byte, 300), // exercises the multi-segment lacing path
	}
	for i := range want[2] {
		want[2][i] = byte(i)
	}
	// A long run of tiny packets forces the encoder to split across pages when
	// the 255-segment limit is reached.
	for i := 0; i < 300; i++ {
		want = append(want, []byte{0xFC, byte(i)})
	}

	var buf bytes.Buffer
	enc := newOggOpusEncoder(&buf)
	for _, pkt := range want {
		if err := enc.WritePacket(pkt); err != nil {
			t.Fatalf("WritePacket: %v", err)
		}
	}
	if err := enc.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	r, err := newOggOpusReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("newOggOpusReader: %v", err)
	}
	if r.Channels() != 2 {
		t.Errorf("expected 2 channels, got %d", r.Channels())
	}

	for i, expected := range want {
		got, err := r.ReadPacket()
		if err != nil {
			t.Fatalf("ReadPacket %d: %v", i, err)
		}
		if !bytes.Equal(got, expected) {
			t.Errorf("packet %d mismatch: got %d bytes, want %d bytes", i, len(got), len(expected))
		}
	}
	if _, err := r.ReadPacket(); err == nil {
		t.Error("expected EOF after the last packet")
	}
}

// TestExtractVideoID covers the URL and bare-id forms a user can supply.
func TestExtractVideoID(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":     "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ":                    "dQw4w9WgXcQ",
		"https://music.youtube.com/watch?v=dQw4w9WgXcQ&x": "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":      "dQw4w9WgXcQ",
		"dQw4w9WgXcQ":     "dQw4w9WgXcQ",
		"MPEDdQw4w9WgXcQ": "dQw4w9WgXcQ",
		"https://example.com/watch?v=dQw4w9WgXcQ": "",
		"not a video": "",
	}
	for in, want := range cases {
		if got := extractVideoID(in); got != want {
			t.Errorf("extractVideoID(%q) = %q, want %q", in, got, want)
		}
	}
}

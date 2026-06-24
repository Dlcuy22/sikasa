// Package sikasa: native_remux_test.go
// Purpose: Implements unit tests for the native dynamic remuxer, including
// initialization, library loading, and callback error propagation.
//
// Key Components:
//   - TestNativeRemux_Init(): Verifies that FFmpeg shared libraries can be resolved and bound.
//   - TestNativeRemux_EmptyReader(): Verifies that empty input streams fail gracefully.
//
// Dependencies:
//   - testing: standard Go testing framework
//
package sikasa

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
TestNativeRemux_Init verifies that the FFmpeg libraries can be successfully
initialized and all dynamic C function pointers can be bound.

    params:
          t: test runner context
*/
func TestNativeRemux_Init(t *testing.T) {
	err := initNativeRemuxer()
	if err != nil {
		t.Skipf("FFmpeg shared libraries not found or failed to load: %v", err)
	}

	if libavutilHandle == 0 {
		t.Error("expected libavutilHandle to be non-zero after initialization")
	}
	if libavcodecHandle == 0 {
		t.Error("expected libavcodecHandle to be non-zero after initialization")
	}
	if libavformatHandle == 0 {
		t.Error("expected libavformatHandle to be non-zero after initialization")
	}

	if av_malloc == nil {
		t.Error("expected av_malloc to be bound")
	}
	if av_packet_alloc == nil {
		t.Error("expected av_packet_alloc to be bound")
	}
	if avformat_open_input == nil {
		t.Error("expected avformat_open_input to be bound")
	}
}

/*
TestNativeRemux_EmptyReader verifies that an empty input stream fails gracefully
at avformat_open_input instead of crashing or causing segmentation faults.

    params:
          t: test runner context
*/
func TestNativeRemux_EmptyReader(t *testing.T) {
	if err := initNativeRemuxer(); err != nil {
		t.Skipf("FFmpeg shared libraries not available: %v", err)
	}

	tmpFile := filepath.Join(t.TempDir(), "empty.ogg")
	err := RemuxStream(strings.NewReader(""), tmpFile)
	if err == nil {
		t.Error("expected error when remuxing empty input; got nil")
	}
}

/*
TestNativeRemux_ValidWebm verifies that remuxing a valid WebM/Opus file to Ogg/Opus
succeeds without error and without crashing.

    params:
          t: test runner context
*/
func TestNativeRemux_ValidWebm(t *testing.T) {
	if err := initNativeRemuxer(); err != nil {
		t.Skipf("FFmpeg shared libraries not available: %v", err)
	}

	inFile, err := os.Open("tiny.webm")
	if err != nil {
		t.Skipf("tiny.webm not found, skipping test: %v", err)
	}
	defer inFile.Close()

	tmpFile := filepath.Join(t.TempDir(), "output.ogg")
	err = RemuxStream(inFile, tmpFile)
	if err != nil {
		t.Fatalf("expected no error when remuxing valid WebM input; got %v", err)
	}

	fi, err := os.Stat(tmpFile)
	if err != nil {
		t.Fatalf("expected output file to exist: %v", err)
	}
	if fi.Size() == 0 {
		t.Error("expected output file to be non-empty")
	}
}

/*
TestNativeRemux_ToWriter verifies that RemuxStreamToWriter successfully remuxes
a valid WebM/Opus stream directly to a writer.

    params:
          t: test runner context
*/
func TestNativeRemux_ToWriter(t *testing.T) {
	if err := initNativeRemuxer(); err != nil {
		t.Skipf("FFmpeg shared libraries not available: %v", err)
	}

	inFile, err := os.Open("tiny.webm")
	if err != nil {
		t.Skipf("tiny.webm not found, skipping test: %v", err)
	}
	defer inFile.Close()

	var buf bytes.Buffer
	err = RemuxStreamToWriter(inFile, &buf)
	if err != nil {
		t.Fatalf("expected no error when remuxing valid WebM input directly to writer; got %v", err)
	}

	if buf.Len() == 0 {
		t.Error("expected written buffer to be non-empty")
	}
}

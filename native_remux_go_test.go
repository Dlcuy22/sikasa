// Package sikasa: native_remux_go_test.go
// Purpose: Implements unit tests for the pure-Go WebM/Opus to Ogg/Opus remuxer.

package sikasa

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeRemuxGo_EmptyReader(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "empty.ogg")
	err := RemuxStreamGo(strings.NewReader(""), tmpFile)
	if err == nil {
		t.Error("expected error when remuxing empty input; got nil")
	}
}

func TestNativeRemuxGo_ValidWebm(t *testing.T) {
	inFile, err := os.Open("tiny.webm")
	if err != nil {
		t.Skipf("tiny.webm not found, skipping test: %v", err)
	}
	defer inFile.Close()

	tmpFile := filepath.Join(t.TempDir(), "output.ogg")
	err = RemuxStreamGo(inFile, tmpFile)
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

func TestNativeRemuxGo_ToWriter(t *testing.T) {
	inFile, err := os.Open("tiny.webm")
	if err != nil {
		t.Skipf("tiny.webm not found, skipping test: %v", err)
	}
	defer inFile.Close()

	var buf bytes.Buffer
	err = RemuxStreamToWriterGo(inFile, &buf)
	if err != nil {
		t.Fatalf("expected no error when remuxing valid WebM input directly to writer; got %v", err)
	}

	if buf.Len() == 0 {
		t.Error("expected written buffer to be non-empty")
	}
}

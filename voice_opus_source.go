// Package sikasa: voice_opus_source.go
// Purpose: A small interface and constructors that turn a byte stream into a
// stream of Opus packets, regardless of whether the container is WebM/Matroska
// (YouTube) or Ogg (local files). The source owns the underlying body so the
// caller only has to Close it.
//
// Dependencies:
//   - io
package sikasa

import (
	"io"
	"os"
)

// opusSource yields Opus packets from a container and owns the underlying body.
type opusSource interface {
	ReadPacket() ([]byte, error)
	Close() error
}

// opusPacketReader is the read half shared by the WebM and Ogg readers.
type opusPacketReader interface {
	ReadPacket() ([]byte, error)
}

// closingOpusSource pairs a packet reader with the body it reads from.
type closingOpusSource struct {
	reader opusPacketReader
	closer io.Closer
}

func (c *closingOpusSource) ReadPacket() ([]byte, error) { return c.reader.ReadPacket() }

func (c *closingOpusSource) Close() error {
	if c.closer == nil {
		return nil
	}

	return c.closer.Close()
}

// newWebMOpusSource builds a forward-only WebM/Opus packet source over rc. On a
// parse failure the body is closed.
func newWebMOpusSource(rc io.ReadCloser) (opusSource, error) {
	r, err := newWebMOpusReader(rc)
	if err != nil {
		_ = rc.Close()

		return nil, err
	}

	return &closingOpusSource{reader: r, closer: rc}, nil
}

// newOggOpusSource builds a forward-only Ogg/Opus packet source over rc. On a
// parse failure the body is closed.
func newOggOpusSource(rc io.ReadCloser) (opusSource, error) {
	r, err := newOggOpusReader(rc)
	if err != nil {
		_ = rc.Close()

		return nil, err
	}

	return &closingOpusSource{reader: r, closer: rc}, nil
}

// openLocalOggSource opens a local .opus or .ogg file as an Opus packet source.
func openLocalOggSource(path string) (opusSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	return newOggOpusSource(f)
}

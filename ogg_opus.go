// Package sikasa: ogg_opus.go
// Purpose: Read Opus audio packets out of an Ogg container (RFC 3533 framing)
// without decoding. It is the local-file counterpart of webm_opus.go: it walks
// Ogg pages forward, reassembles packets across page boundaries, and yields one
// Opus packet at a time. The OpusHead and OpusTags header packets are consumed
// at open and never returned.
//
// Dependencies:
//   - bufio, errors, fmt, io
package sikasa

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// oggOpusReader walks an Ogg Opus stream and yields raw Opus packets. It is
// forward-only and not safe for concurrent use.
type oggOpusReader struct {
	r          *bufio.Reader
	channels   int
	preSkip    int
	gainQ78    int16
	headerSeen int

	pending [][]byte
	partial []byte
}

// newOggOpusReader wraps r and consumes the OpusHead and OpusTags header
// packets. The returned reader is positioned at the first audio packet.
func newOggOpusReader(r io.Reader) (*oggOpusReader, error) {
	if r == nil {
		return nil, errors.New("sikasa: nil Ogg Opus source")
	}
	o := &oggOpusReader{r: bufio.NewReaderSize(r, 64*1024)}

	head, err := o.nextPacket()
	if err != nil {
		return nil, fmt.Errorf("sikasa: read OpusHead: %w", err)
	}
	info, err := parseOpusHead(head)
	if err != nil {
		return nil, err
	}
	o.channels = info.channels
	o.preSkip = info.preSkip
	o.gainQ78 = info.gain

	// OpusTags is mandatory and always follows OpusHead (RFC 7845 section 5.2).
	if _, err := o.nextPacket(); err != nil {
		return nil, fmt.Errorf("sikasa: read OpusTags: %w", err)
	}

	return o, nil
}

// PreSkip is the pre-skip from the OpusHead, in 48 kHz samples.
func (o *oggOpusReader) PreSkip() int { return o.preSkip }

// OutputGainQ78 is the signed Q7.8 dB output gain from the OpusHead.
func (o *oggOpusReader) OutputGainQ78() int16 { return o.gainQ78 }

// Channels is the channel count declared by the OpusHead.
func (o *oggOpusReader) Channels() int { return o.channels }

// ReadPacket returns the next audio packet, or io.EOF at the end of the stream.
func (o *oggOpusReader) ReadPacket() ([]byte, error) {
	return o.nextPacket()
}

// nextPacket returns the next reassembled packet, reading more pages as needed.
func (o *oggOpusReader) nextPacket() ([]byte, error) {
	for {
		if len(o.pending) > 0 {
			pkt := o.pending[0]
			o.pending = o.pending[1:]

			return pkt, nil
		}
		if err := o.readPage(); err != nil {
			return nil, err
		}
	}
}

// readPage reads one Ogg page and appends the packets it completes to pending.
// A packet whose final segment is 255 continues onto the next page; the partial
// bytes are carried in o.partial until a segment smaller than 255 ends it.
func (o *oggOpusReader) readPage() error {
	header := make([]byte, 27)
	if _, err := io.ReadFull(o.r, header); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return io.EOF
		}

		return err
	}
	if string(header[:4]) != "OggS" {
		return fmt.Errorf("sikasa: ogg sync mismatch")
	}

	segCount := int(header[26])
	segTable := make([]byte, segCount)
	if _, err := io.ReadFull(o.r, segTable); err != nil {
		return err
	}

	payloadSize := 0
	for _, n := range segTable {
		payloadSize += int(n)
	}
	payload := make([]byte, payloadSize)
	if _, err := io.ReadFull(o.r, payload); err != nil {
		return err
	}

	off := 0
	for _, n := range segTable {
		seg := payload[off : off+int(n)]
		off += int(n)
		o.partial = append(o.partial, seg...)
		if n < 255 {
			o.pending = append(o.pending, o.partial)
			o.partial = nil
		}
	}

	return nil
}

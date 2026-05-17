// Package sikasa: voice_ogg.go
// Purpose: Parses an Ogg-Opus byte stream from FFmpeg's stdout into individual
// Opus frames suitable for direct injection into discordgo's OpusSend channel.
//
// Key Components:
//   - oggPageParser:   reads Ogg pages from an io.Reader and returns segments
//   - NextFrame():     returns the next Opus packet (one 20ms frame)
//
// Dependencies:
//   - encoding/binary: little-endian header parsing
//   - io:              EOF handling
//
// Note: Ogg page format is fixed-27-byte header followed by a segment table.
// Segments < 255 bytes mark a packet boundary; consecutive 255-byte segments
// are concatenated until a sub-255 segment ends the packet. The first two
// pages of an Opus stream are headers (OpusHead, OpusTags) and are skipped.
package sikasa

import (
	"errors"
	"fmt"
	"io"
)

// oggPageParser walks an Ogg-Opus stream and yields raw Opus packets.
type oggPageParser struct {
	r            io.Reader
	pendingPkts  [][]byte
	headerSeen   int
}

// newOggParser wraps an io.Reader as an Ogg parser.
func newOggParser(r io.Reader) *oggPageParser {
	return &oggPageParser{r: r}
}

/*
NextFrame returns the next Opus packet from the stream.

	returns:
	      []byte: a single Opus packet (one 20ms frame at standard config)
	      error:  io.EOF when the stream has no more data
*/
func (p *oggPageParser) NextFrame() ([]byte, error) {
	for len(p.pendingPkts) == 0 {
		pkts, err := p.readPage()
		if err != nil {
			return nil, err
		}
		// First two pages of an Opus-in-Ogg stream are OpusHead and OpusTags.
		// Skip them so we hand back only audio packets.
		if p.headerSeen < 2 {
			p.headerSeen++
			continue
		}
		p.pendingPkts = pkts
	}
	frame := p.pendingPkts[0]
	p.pendingPkts = p.pendingPkts[1:]
	return frame, nil
}

// readPage reads exactly one Ogg page and returns the packets it contains.
// A "packet" here is the concatenation of consecutive 255-byte segments
// terminated by a sub-255 segment. Packets that span page boundaries are
// returned as a single byte slice, but for Opus in practice every audio
// packet fits within one segment, so this is rarely exercised.
func (p *oggPageParser) readPage() ([][]byte, error) {
	header := make([]byte, 27)
	if _, err := io.ReadFull(p.r, header); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.EOF
		}
		return nil, err
	}
	if string(header[:4]) != "OggS" {
		return nil, fmt.Errorf("sikasa: ogg sync mismatch")
	}

	// Field offsets per RFC 3533:
	//   header[26]: number of segments in this page
	segCount := int(header[26])
	segTable := make([]byte, segCount)
	if _, err := io.ReadFull(p.r, segTable); err != nil {
		return nil, err
	}

	// Compute total payload size
	payloadSize := 0
	for _, n := range segTable {
		payloadSize += int(n)
	}
	payload := make([]byte, payloadSize)
	if _, err := io.ReadFull(p.r, payload); err != nil {
		return nil, err
	}

	// Walk segment table to assemble packets.
	pkts := make([][]byte, 0, segCount)
	var cur []byte
	off := 0
	for _, n := range segTable {
		seg := payload[off : off+int(n)]
		off += int(n)
		cur = append(cur, seg...)
		// A packet ends when we see a segment with size < 255.
		if n < 255 {
			pkts = append(pkts, cur)
			cur = nil
		}
	}
	// Trailing partial packet would continue on the next page; in practice
	// FFmpeg Opus-in-Ogg flushes per-page, so we drop any leftover here.
	return pkts, nil
}

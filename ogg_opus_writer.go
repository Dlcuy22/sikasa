// Package sikasa: ogg_opus_writer.go
// Purpose: A minimal Ogg Opus muxer. It writes an OpusHead page, an OpusTags
// page, and then batches Opus packets into audio pages with running granule
// positions. It is used only to build the on-disk prefetch cache, replacing the
// former yt-dlp + ffmpeg remux with pure Go.
//
// Dependencies:
//   - encoding/binary, fmt, io, math/rand
package sikasa

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
)

// oggAudioPageTargetBytes is the soft payload size at which an audio page is
// flushed. Batching keeps the page count low without letting a page grow large.
const oggAudioPageTargetBytes = 4096

// oggOpusCRCTable is the Ogg page checksum (CRC-32, polynomial 0x04c11db7, no
// reflection, zero initial value) in table form.
var oggOpusCRCTable = func() [256]uint32 {
	const poly = 0x04c11db7

	var table [256]uint32
	for i := range table {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = r<<1 ^ poly
			} else {
				r <<= 1
			}
		}
		table[i] = r
	}

	return table
}()

// oggOpusChecksum computes the checksum over a page whose checksum field has
// been zeroed.
func oggOpusChecksum(page []byte) uint32 {
	var crc uint32
	for _, b := range page {
		crc = crc<<8 ^ oggOpusCRCTable[byte(crc>>24)^b]
	}

	return crc
}

// oggOpusEncoder writes Opus packets to an Ogg stream.
type oggOpusEncoder struct {
	w      io.Writer
	serial uint32
	seq    uint32

	preSkip  int
	channels int
	gainQ78  int16

	granule int64

	// pending accumulates audio packet payloads for the current page.
	pending     []byte
	segments    []byte
	packetCount int

	headersWritten bool
}

// newOggOpusEncoder returns an encoder writing to w. Headers are written on the
// first WritePacket so the pre-skip from the source is already known.
func newOggOpusEncoder(w io.Writer) *oggOpusEncoder {
	return &oggOpusEncoder{
		w:        w,
		serial:   rand.Uint32(),
		preSkip:  0,
		channels: 2,
	}
}

// WritePacket appends one Opus packet. It writes the headers first when needed
// and flushes a page once the pending payload reaches the target size or the
// page is close to the 255-segment limit.
func (e *oggOpusEncoder) WritePacket(pkt []byte) error {
	if len(pkt) == 0 {
		return nil
	}
	if !e.headersWritten {
		if err := e.writeHeaders(); err != nil {
			return err
		}
	}

	// A page holds at most 255 lacing segments. Flush first when this packet
	// would overflow the page, which can happen with a run of tiny packets.
	if len(e.segments)+oggSegmentsFor(len(pkt)) > 255 {
		if err := e.flush(false); err != nil {
			return err
		}
	}

	e.appendSegments(len(pkt))

	e.pending = append(e.pending, pkt...)
	e.packetCount++
	e.granule += int64(max(opusPacketSamples48(pkt), 0))

	if len(e.pending) >= oggAudioPageTargetBytes {
		return e.flush(false)
	}

	return nil
}

// oggSegmentsFor returns the number of lacing segments a payload of size n
// occupies: one byte per 255, plus the final short segment.
func oggSegmentsFor(n int) int {
	return n/255 + 1
}

// appendSegments writes the Ogg segment table entries for a payload of size n.
func (e *oggOpusEncoder) appendSegments(n int) {
	for n >= 255 {
		e.segments = append(e.segments, 255)
		n -= 255
	}
	e.segments = append(e.segments, byte(n))
}

// Finish flushes the last audio page and marks it end-of-stream.
func (e *oggOpusEncoder) Finish() error {
	if !e.headersWritten {
		if err := e.writeHeaders(); err != nil {
			return err
		}
	}
	if e.packetCount == 0 {
		return nil
	}

	return e.flush(true)
}

// writeHeaders writes the OpusHead (BOS) and OpusTags pages.
func (e *oggOpusEncoder) writeHeaders() error {
	e.headersWritten = true

	head := buildOpusHead(e.channels, e.preSkip, e.gainQ78)
	if err := e.writePage(0x02, 0, [][]byte{head}); err != nil { // BOS
		return err
	}

	tags := buildOpusTags()
	return e.writePage(0x00, 0, [][]byte{tags})
}

// flush writes the pending audio packets as one page. eos sets the end-of-stream
// flag; the granule is the running sample count.
func (e *oggOpusEncoder) flush(eos bool) error {
	if e.packetCount == 0 {
		return nil
	}

	headerType := byte(0x00)
	if eos {
		headerType = 0x04
	}

	page, err := e.buildPage(headerType, e.granule, e.segments, e.pending)
	if err != nil {
		return err
	}
	if _, err := e.w.Write(page); err != nil {
		return err
	}

	e.pending = e.pending[:0]
	e.segments = e.segments[:0]
	e.packetCount = 0

	return nil
}

// writePage writes a page whose payload is the concatenation of packets, each
// one segment-tabled separately.
func (e *oggOpusEncoder) writePage(headerType byte, granule int64, packets [][]byte) error {
	var (
		segments []byte
		payload  []byte
	)
	for _, p := range packets {
		for n := len(p); n >= 255; n -= 255 {
			segments = append(segments, 255)
		}
		segments = append(segments, byte(len(p)%255))
		payload = append(payload, p...)
	}

	page, err := e.buildPage(headerType, granule, segments, payload)
	if err != nil {
		return err
	}
	_, err = e.w.Write(page)

	return err
}

// buildPage assembles a complete Ogg page and patches its checksum.
func (e *oggOpusEncoder) buildPage(headerType byte, granule int64, segments, payload []byte) ([]byte, error) {
	if len(segments) > 255 {
		return nil, fmt.Errorf("sikasa: ogg page needs %d segments (max 255)", len(segments))
	}

	page := make([]byte, 0, 27+len(segments)+len(payload))
	page = append(page, 'O', 'g', 'g', 'S')
	page = append(page, 0) // stream structure version
	page = append(page, headerType)

	var granuleBuf [8]byte
	binary.LittleEndian.PutUint64(granuleBuf[:], uint64(granule))
	page = append(page, granuleBuf[:]...)

	var serialBuf [4]byte
	binary.LittleEndian.PutUint32(serialBuf[:], e.serial)
	page = append(page, serialBuf[:]...)

	var seqBuf [4]byte
	binary.LittleEndian.PutUint32(seqBuf[:], e.seq)
	page = append(page, seqBuf[:]...)

	page = append(page, 0, 0, 0, 0) // checksum placeholder
	page = append(page, byte(len(segments)))
	page = append(page, segments...)
	page = append(page, payload...)

	e.seq++

	crc := oggOpusChecksum(page)
	binary.LittleEndian.PutUint32(page[22:26], crc)

	return page, nil
}

// buildOpusHead builds a family-0 OpusHead packet.
func buildOpusHead(channels, preSkip int, gainQ78 int16) []byte {
	if channels < 1 || channels > 2 {
		channels = 2
	}

	pkt := make([]byte, 0, 19)
	pkt = append(pkt, []byte("OpusHead")...)
	pkt = append(pkt, 1) // version
	pkt = append(pkt, byte(channels))

	var buf [4]byte
	binary.LittleEndian.PutUint16(buf[:2], uint16(preSkip))
	pkt = append(pkt, buf[:2]...)

	binary.LittleEndian.PutUint32(buf[:], webmOpusSampleRate)
	pkt = append(pkt, buf[:]...)

	binary.LittleEndian.PutUint16(buf[:2], uint16(gainQ78))
	pkt = append(pkt, buf[:2]...)

	pkt = append(pkt, 0) // mapping family

	return pkt
}

// buildOpusTags builds an OpusTags packet with the sikasa vendor string and no
// user comments.
func buildOpusTags() []byte {
	vendor := "sikasa"

	var buf [4]byte
	pkt := make([]byte, 0, 8+4+len(vendor)+4)
	pkt = append(pkt, []byte("OpusTags")...)

	binary.LittleEndian.PutUint32(buf[:], uint32(len(vendor)))
	pkt = append(pkt, buf[:]...)
	pkt = append(pkt, vendor...)

	pkt = append(pkt, 0, 0, 0, 0) // zero user comments

	return pkt
}

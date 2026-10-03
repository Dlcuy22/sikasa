// Package sikasa: webm_opus.go
// Purpose: Read Opus audio packets out of a WebM/Matroska bitstream (RFC 8794
// framing, Matroska codec mapping for A_OPUS) without decoding. This is a
// forward-only port of the WebM reader from github.com/dlcuy22/molo
// (decode/webmopus.go): it parses EBML, Info and Tracks from the stream and
// then walks SimpleBlock/BlockGroup payloads, yielding one Opus packet at a
// time. Seek and Cues handling are omitted because Discord playback only moves
// forward.
//
// Key Components:
//   - newWebMOpusReader(): parses the headers and stops at the first cluster
//   - webmOpusReader.ReadPacket(): one Opus packet from a SimpleBlock or Block
//   - webmTrackParser: the shared A_OPUS TrackEntry parser
//
// Dependencies:
//   - bufio, encoding/binary, errors, fmt, io
package sikasa

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Matroska/WebM element IDs, raw (marker bit kept, RFC 8794 section 5).
const (
	idEBML           = 0x1A45DFA3
	idDocType        = 0x4282
	idEBMLMaxIDLen   = 0x42F2
	idSegment        = 0x18538067
	idInfo           = 0x1549A966
	idTimestampScale = 0x2AD7B1
	idDuration       = 0x4489
	idTracks         = 0x1654AE6B
	idTrackEntry     = 0xAE
	idTrackNumber    = 0xD7
	idTrackType      = 0x83 // 2 = audio
	idCodecID        = 0x86 // "A_OPUS"
	idCodecPrivate   = 0x63A2
	idCodecDelay     = 0x56AA
	idSeekPreRoll    = 0x56BB
	idContentEnc     = 0x6D80
	idCluster        = 0x1F43B675
	idTimestamp      = 0xE7
	idSimpleBlock    = 0xA3
	idBlockGroup     = 0xA0
	idBlock          = 0xA1
	idDiscardPadding = 0x75A2
)

// webmOpusSampleRate is fixed: Opus decoding and pre-skip are defined at 48 kHz
// regardless of the rate recorded in the header.
const webmOpusSampleRate = 48000

// webmDefaultTimestampScale is 1 ms per tick, the Matroska default.
const webmDefaultTimestampScale = 1_000_000

// webmTrackTypeAudio is TrackType 2 (Matroska Codec Mapping).
const webmTrackTypeAudio = 2

// Errors returned by the reader. Callers can test them with errors.Is.
var (
	errWebMOpusNotWebM          = errors.New("sikasa: not a WebM/Matroska stream")
	errWebMOpusNotOpus          = errors.New("sikasa: no A_OPUS audio track")
	errWebMOpusMissingCodecPriv = errors.New("sikasa: A_OPUS track has no CodecPrivate")
	errWebMOpusEncrypted        = errors.New("sikasa: WebM ContentEncodings are unsupported")
	errWebMOpusNoClusters       = errors.New("sikasa: WebM stream has no clusters")
)

// webmBlock is one parsed Block or SimpleBlock payload.
type webmBlock struct {
	track      uint64
	keyframe   bool
	packets    [][]byte
	discardPad int64 // signed DiscardPadding of the enclosing BlockGroup, nanoseconds
}

// webmOpusReader reads Opus packets forward from a WebM/Matroska stream. It
// holds no index, so it cannot reposition, matching the playback use. It is not
// safe for concurrent use.
type webmOpusReader struct {
	src *bufio.Reader
	off int64

	timestampScale uint64
	preSkip        int
	gainQ78        int16
	channels       int
	trackNumber    uint64

	// segmentEnd is the exclusive end of the Segment, or -1 when unknown.
	segmentEnd    int64
	clusterEnd    int64
	clusterBaseNs int64

	pending    *webmBlock
	pendingIdx int
}

// newWebMOpusReader parses the headers straight from the stream and stops at
// the first cluster. The returned reader is positioned at the first audio
// packet.
func newWebMOpusReader(r io.Reader) (*webmOpusReader, error) {
	if r == nil {
		return nil, errors.New("sikasa: nil WebM Opus source")
	}
	f := &webmOpusReader{src: bufio.NewReaderSize(r, 64*1024), segmentEnd: -1}
	if err := f.parseHeaders(); err != nil {
		return nil, err
	}

	return f, nil
}

// readHeader reads the element header at the current offset and advances past
// it, returning an element whose offsets are absolute.
func (f *webmOpusReader) readHeader() (ebmlElement, error) {
	var buf [ebmlMaxIDLen + ebmlMaxSizeLen]byte
	start := f.off

	// ID: read its leading byte, then the rest.
	if _, err := io.ReadFull(f.src, buf[:1]); err != nil {
		if errors.Is(err, io.EOF) {
			return ebmlElement{}, io.EOF
		}

		return ebmlElement{}, fmt.Errorf("%w: read element id: %w", errEBMLTruncated, err)
	}
	f.off++
	idw := vintWidth(buf[0])
	if idw < 1 || idw > ebmlMaxIDLen {
		return ebmlElement{}, fmt.Errorf("%w: id width %d", errEBMLBadVint, idw)
	}
	if idw == 1 && buf[0] == 0xFF {
		return ebmlElement{}, fmt.Errorf("%w: reserved id 0xFF", errEBMLBadVint)
	}
	if _, err := io.ReadFull(f.src, buf[1:idw]); err != nil {
		return ebmlElement{}, fmt.Errorf("%w: read element id: %w", errEBMLTruncated, err)
	}
	f.off += int64(idw - 1)

	// Size: same discovery, starting after the ID.
	if _, err := io.ReadFull(f.src, buf[idw:idw+1]); err != nil {
		return ebmlElement{}, fmt.Errorf("%w: read element size: %w", errEBMLTruncated, err)
	}
	f.off++
	szw := vintWidth(buf[idw])
	if szw < 1 || szw > ebmlMaxSizeLen {
		return ebmlElement{}, fmt.Errorf("%w: size width %d", errEBMLBadVint, szw)
	}
	if _, err := io.ReadFull(f.src, buf[idw+1:idw+szw]); err != nil {
		return ebmlElement{}, fmt.Errorf("%w: read element size: %w", errEBMLTruncated, err)
	}
	f.off += int64(szw - 1)

	e, err := readEBMLHeader(buf[:idw+szw], 0)
	if err != nil {
		return ebmlElement{}, err
	}
	e.DataOff += start
	if !e.Unknown {
		e.Next += start
	}

	return e, nil
}

// vintWidth returns the byte width a VINT's leading byte announces.
func vintWidth(first byte) int {
	if first == 0 {
		return 0
	}
	width := 1
	mask := byte(0x80)
	for first&mask == 0 {
		mask >>= 1
		width++
	}

	return width
}

// parseHeaders reads EBML, then walks Segment's children until the first
// cluster, so Info and Tracks are loaded before playback starts.
func (f *webmOpusReader) parseHeaders() error {
	ebml, err := f.readHeader()
	if err != nil {
		return err
	}
	if ebml.ID != idEBML {
		return fmt.Errorf("%w: leading element is %#x", errWebMOpusNotWebM, ebml.ID)
	}
	body, err := f.readN(int64(ebml.Size))
	if err != nil {
		return err
	}
	if err := validateWebMEBMLHeader(body); err != nil {
		return err
	}

	seg, err := f.readHeader()
	if err != nil {
		return err
	}
	if seg.ID != idSegment {
		return fmt.Errorf("%w: element after EBML is %#x, not Segment", errWebMOpusNotWebM, seg.ID)
	}
	if !seg.Unknown {
		f.segmentEnd = seg.Next
	}

	for f.segmentEnd < 0 || f.off < f.segmentEnd {
		e, err := f.readHeader()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return err
		}
		if e.Unknown {
			e.Next = f.segmentEnd
		}
		switch e.ID {
		case idInfo:
			b, err := f.readN(int64(e.Size))
			if err != nil {
				return err
			}
			if err := f.parseInfoBody(b); err != nil {
				return err
			}
		case idTracks:
			b, err := f.readN(int64(e.Size))
			if err != nil {
				return err
			}
			if err := f.parseTracksBody(b); err != nil {
				return err
			}
		case idCluster:
			f.clusterEnd = -1
			if !e.Unknown {
				f.clusterEnd = e.Next
			}

			if f.timestampScale == 0 {
				f.timestampScale = webmDefaultTimestampScale
			}
			if f.trackNumber == 0 {
				return fmt.Errorf("%w: no audio track entry in Tracks", errWebMOpusNotOpus)
			}

			return nil
		default:
			if err := f.skipN(int64(e.Size)); err != nil {
				return err
			}
		}
	}

	return errWebMOpusNoClusters
}

func (f *webmOpusReader) readN(size int64) ([]byte, error) {
	if size < 0 || size > 1<<30 {
		return nil, fmt.Errorf("%w: element size %d", errEBMLBadVint, size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(f.src, buf); err != nil {
		return nil, fmt.Errorf("%w: read %d bytes: %w", errEBMLTruncated, size, err)
	}
	f.off += size

	return buf, nil
}

func (f *webmOpusReader) skipN(size int64) error {
	if size < 0 {
		return fmt.Errorf("%w: element size %d", errEBMLBadVint, size)
	}
	if _, err := io.CopyN(io.Discard, f.src, size); err != nil {
		return fmt.Errorf("%w: skip %d bytes: %w", errEBMLTruncated, size, err)
	}
	f.off += size

	return nil
}

func (f *webmOpusReader) parseInfoBody(body []byte) error {
	return ebmlChildren(body, 0, int64(len(body)), func(c ebmlElement) error {
		if c.ID == idTimestampScale {
			v, err := ebmlUint(body, c.DataOff, int64(c.Size))
			if err != nil {
				return err
			}
			f.timestampScale = v
		}

		return nil
	})
}

func (f *webmOpusReader) parseTracksBody(body []byte) error {
	return ebmlChildren(body, 0, int64(len(body)), func(track ebmlElement) error {
		if track.ID != idTrackEntry || track.Unknown {
			return nil
		}
		return (webmTrackParser{}).parse(body[track.DataOff:track.Next], func(fields webmTrackFields) {
			if f.trackNumber != 0 {
				return
			}
			f.trackNumber = fields.trackNumber
			f.preSkip = fields.preSkip
			f.gainQ78 = fields.gain
			f.channels = fields.channels
		})
	})
}

// PreSkip is the CodecDelay in 48 kHz samples, the amount discarded from the
// start of the decoded stream.
func (f *webmOpusReader) PreSkip() int { return f.preSkip }

// OutputGainQ78 is the signed Q7.8 dB output gain from the OpusHead CodecPrivate.
func (f *webmOpusReader) OutputGainQ78() int16 { return f.gainQ78 }

// Channels is the output channel count declared by the OpusHead CodecPrivate.
func (f *webmOpusReader) Channels() int { return f.channels }

// SampleRate is always 48000.
func (f *webmOpusReader) SampleRate() int { return webmOpusSampleRate }

// ReadPacket returns the next audio packet, or io.EOF at the end of the stream.
func (f *webmOpusReader) ReadPacket() ([]byte, error) {
	for {
		if f.pending != nil && f.pendingIdx < len(f.pending.packets) {
			pkt := f.pending.packets[f.pendingIdx]
			f.pendingIdx++

			return pkt, nil
		}

		blk, err := f.nextBlock()
		if err != nil {
			return nil, err
		}
		f.pending = &blk
		f.pendingIdx = 0
	}
}

// nextBlock reads forward to the next SimpleBlock or BlockGroup of the audio
// track, crossing cluster boundaries.
func (f *webmOpusReader) nextBlock() (webmBlock, error) {
	for {
		if f.clusterEnd >= 0 && f.off >= f.clusterEnd {
			f.clusterEnd = -1
		}
		if f.segmentEnd >= 0 && f.off >= f.segmentEnd {
			return webmBlock{}, io.EOF
		}

		e, err := f.readHeader()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return webmBlock{}, io.EOF
			}

			return webmBlock{}, err
		}
		if e.ID == idCluster {
			if !e.Unknown {
				f.clusterEnd = e.Next
			}

			continue
		}
		if e.ID == idTimestamp {
			body, err := f.readN(int64(e.Size))
			if err != nil {
				return webmBlock{}, err
			}
			ticks, err := ebmlUint(body, 0, int64(len(body)))
			if err != nil {
				return webmBlock{}, err
			}
			f.clusterBaseNs = int64(ticks) * int64(f.timestampScale)

			continue
		}
		if e.ID == ebmlIDVoid || e.ID == ebmlIDCRC32 {
			if err := f.skipN(int64(e.Size)); err != nil {
				return webmBlock{}, err
			}

			continue
		}
		if e.ID != idSimpleBlock && e.ID != idBlockGroup {
			if e.Unknown {
				return webmBlock{}, fmt.Errorf("%w: unexpected unknown-size element %#x", errEBMLTruncated, e.ID)
			}
			if err := f.skipN(int64(e.Size)); err != nil {
				return webmBlock{}, err
			}

			continue
		}

		body, err := f.readN(int64(e.Size))
		if err != nil {
			return webmBlock{}, err
		}
		blk, err := f.blockFromBody(e.ID, body)
		if err != nil {
			return webmBlock{}, err
		}
		if blk.track != f.trackNumber || len(blk.packets) == 0 {
			continue
		}

		return blk, nil
	}
}

// blockFromBody parses a SimpleBlock body directly or a BlockGroup body.
func (f *webmOpusReader) blockFromBody(id uint32, body []byte) (webmBlock, error) {
	if id == idSimpleBlock {
		return parseWebMBlock(body, f.timestampScale, f.clusterBaseNs)
	}

	var (
		blk webmBlock
		pad int64
	)
	err := ebmlChildren(body, 0, int64(len(body)), func(c ebmlElement) error {
		switch c.ID {
		case idBlock:
			b, err := parseWebMBlock(body[c.DataOff:c.Next], f.timestampScale, f.clusterBaseNs)
			if err != nil {
				return err
			}
			blk = b
		case idDiscardPadding:
			v, err := ebmlUint(body, c.DataOff, int64(c.Size))
			if err != nil {
				return err
			}
			pad = signedEBMLInt(v, int(c.Size))
		}

		return nil
	})
	if err != nil {
		return webmBlock{}, err
	}
	blk.discardPad = pad

	return blk, nil
}

// validateWebMEBMLHeader checks DocType and EBMLMaxIDLength in an already-read
// EBML header body.
func validateWebMEBMLHeader(body []byte) error {
	docType := ""
	var maxID uint64
	err := ebmlChildren(body, 0, int64(len(body)), func(c ebmlElement) error {
		switch c.ID {
		case idDocType:
			docType = string(body[c.DataOff : c.DataOff+int64(c.Size)])
		case idEBMLMaxIDLen:
			v, err := ebmlUint(body, c.DataOff, int64(c.Size))
			if err != nil {
				return err
			}
			maxID = v
		}

		return nil
	})
	if err != nil {
		return err
	}
	if docType != "webm" {
		return fmt.Errorf("%w: DocType is %q", errWebMOpusNotWebM, docType)
	}
	if maxID > ebmlMaxIDLen {
		return fmt.Errorf("%w: EBMLMaxIDLength %d exceeds %d", errWebMOpusNotWebM, maxID, ebmlMaxIDLen)
	}

	return nil
}

// --- shared track parsing ------------------------------------------

// webmTrackFields is the A_OPUS subset of a TrackEntry.
type webmTrackFields struct {
	trackNumber uint64
	preSkip     int
	seekPreRoll int
	gain        int16
	channels    int
}

// webmTrackParser parses one TrackEntry body. It is a zero-size type so the
// shared logic has one home without either reader owning the other.
type webmTrackParser struct{}

func (webmTrackParser) parse(body []byte, adopt func(webmTrackFields)) error {
	var (
		trackType uint64
		fields    webmTrackFields
		codecID   string
		codecPriv []byte
		delayNS   uint64
		seekNS    uint64
		encrypted bool
	)
	err := ebmlChildren(body, 0, int64(len(body)), func(c ebmlElement) error {
		switch c.ID {
		case idTrackNumber:
			v, err := ebmlUint(body, c.DataOff, int64(c.Size))
			if err != nil {
				return err
			}
			fields.trackNumber = v
		case idTrackType:
			v, err := ebmlUint(body, c.DataOff, int64(c.Size))
			if err != nil {
				return err
			}
			trackType = v
		case idCodecID:
			codecID = string(body[c.DataOff : c.DataOff+int64(c.Size)])
		case idCodecPrivate:
			codecPriv = body[c.DataOff : c.DataOff+int64(c.Size)]
		case idCodecDelay:
			v, err := ebmlUint(body, c.DataOff, int64(c.Size))
			if err != nil {
				return err
			}
			delayNS = v
		case idSeekPreRoll:
			v, err := ebmlUint(body, c.DataOff, int64(c.Size))
			if err != nil {
				return err
			}
			seekNS = v
		case idContentEnc:
			encrypted = true
		}

		return nil
	})
	if err != nil {
		return err
	}
	if trackType != webmTrackTypeAudio || codecID != "A_OPUS" {
		return nil
	}
	if encrypted {
		return errWebMOpusEncrypted
	}
	if len(codecPriv) == 0 {
		return fmt.Errorf("%w: track %d", errWebMOpusMissingCodecPriv, fields.trackNumber)
	}
	info, err := parseOpusHead(codecPriv)
	if err != nil {
		return fmt.Errorf("sikasa: WebM CodecPrivate: %w", err)
	}
	fields.channels = info.channels
	fields.gain = info.gain
	fields.seekPreRoll = int((seekNS*webmOpusSampleRate + 500_000_000) / 1_000_000_000)
	fields.preSkip = int((delayNS*webmOpusSampleRate + 500_000_000) / 1_000_000_000)
	if fields.preSkip == 0 {
		fields.preSkip = info.preSkip
	}
	adopt(fields)

	return nil
}

// parseWebMBlock parses a Block or SimpleBlock payload (Matroska "Block
// Structure"): a track-number VINT, a signed 16-bit timestamp relative to the
// cluster, one flag byte, then optional lacing and the packets. baseNs is the
// enclosing cluster's timestamp, so the returned block's time is absolute.
func parseWebMBlock(data []byte, timestampScale uint64, baseNs int64) (webmBlock, error) {
	track, trackW, _, err := ebmlVint(data, 0)
	if err != nil {
		return webmBlock{}, fmt.Errorf("sikasa: block track number: %w", err)
	}
	if trackW+3 > len(data) {
		return webmBlock{}, fmt.Errorf("%w: block header truncated", errEBMLTruncated)
	}
	rel := int16(binary.BigEndian.Uint16(data[trackW : trackW+2]))
	flags := data[trackW+2]
	rest := data[trackW+3:]

	blk := webmBlock{
		track:    track,
		keyframe: flags&0x80 != 0,
	}
	_ = timestampScale
	_ = baseNs
	lacing := (flags >> 1) & 0x03

	packets, err := splitWebMLacing(lacing, rest)
	if err != nil {
		return webmBlock{}, err
	}
	blk.packets = packets
	_ = rel

	return blk, nil
}

// splitWebMLacing divides a block body into packets. Lacing is optional and the
// three non-zero modes must all be accepted.
func splitWebMLacing(lacing byte, body []byte) ([][]byte, error) {
	if lacing == 0 {
		if len(body) == 0 {
			return nil, nil
		}

		return [][]byte{body}, nil
	}
	if len(body) < 1 {
		return nil, fmt.Errorf("%w: laced block has no frame count", errEBMLTruncated)
	}

	frames := int(body[0]) + 1
	rest := body[1:]

	switch lacing {
	case 1: // Xiph: sizes as runs of 255 ending in a byte < 255.
		sizes := make([]int, 0, frames-1)
		for range frames - 1 {
			size := 0
			for {
				if len(rest) == 0 {
					return nil, fmt.Errorf("%w: Xiph lacing ran out", errEBMLTruncated)
				}
				b := rest[0]
				rest = rest[1:]
				size += int(b)
				if b < 255 {
					break
				}
			}
			sizes = append(sizes, size)
		}

		return sliceWebMPackets(rest, sizes)
	case 2: // Fixed: every frame is the same size.
		if frames == 0 || len(rest)%frames != 0 {
			return nil, fmt.Errorf("sikasa: fixed lacing does not divide %d bytes into %d frames", len(rest), frames)
		}
		size := len(rest) / frames
		sizes := make([]int, frames-1)
		for i := range sizes {
			sizes[i] = size
		}

		return sliceWebMPackets(rest, sizes)
	default: // 3, EBML: first size unsigned, the rest deltas from it.
		first, w, _, err := ebmlVint(rest, 0)
		if err != nil {
			return nil, fmt.Errorf("sikasa: EBML lacing size: %w", err)
		}
		rest = rest[w:]
		sizes := make([]int, 0, frames-1)
		cur := int(first)
		sizes = append(sizes, cur)
		for range frames - 2 {
			delta, w, _, err := ebmlVint(rest, 0)
			if err != nil {
				return nil, fmt.Errorf("sikasa: EBML lacing delta: %w", err)
			}
			rest = rest[w:]
			cur += int(signedVintDelta(delta, w))
			if cur < 0 {
				return nil, errors.New("sikasa: EBML lacing frame size is negative")
			}
			sizes = append(sizes, cur)
		}

		return sliceWebMPackets(rest, sizes)
	}
}

// sliceWebMPackets cuts body into the leading sizes plus one final packet that
// takes the remainder.
func sliceWebMPackets(body []byte, sizes []int) ([][]byte, error) {
	packets := make([][]byte, 0, len(sizes)+1)
	off := 0
	for _, size := range sizes {
		if size <= 0 || off+size > len(body) {
			return nil, fmt.Errorf("%w: lacing frame of %d bytes in a %d-byte block", errEBMLTruncated, size, len(body))
		}
		packets = append(packets, body[off:off+size])
		off += size
	}
	if off >= len(body) {
		return nil, fmt.Errorf("%w: lacing leaves a zero-length final packet", errEBMLTruncated)
	}
	packets = append(packets, body[off:])

	return packets, nil
}

// signedEBMLInt sign-extends a fixed-width signed integer field read with
// ebmlUint, such as DiscardPadding (RFC 8794 section 7.3).
func signedEBMLInt(v uint64, width int) int64 {
	if width <= 0 {
		return 0
	}
	bits := 8 * width
	if bits < 64 && v&(1<<(bits-1)) != 0 {
		v |= ^uint64(0) << bits
	}

	return int64(v)
}

// signedVintDelta sign-extends an EBML lacing size delta.
func signedVintDelta(v uint64, width int) int64 {
	if width <= 0 {
		return 0
	}
	bits := 7 * width
	if bits < 64 && v&(1<<(bits-1)) != 0 {
		v |= ^uint64(0) << bits
	}

	return int64(v)
}

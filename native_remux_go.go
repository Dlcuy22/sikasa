// Package sikasa: native_remux_go.go
// Purpose: Pure Go WebM/Opus to Ogg/Opus remuxer using
// github.com/remko/go-mkvparse (Matroska/WebM parser) and
// mccoy.space/g/ogg (Ogg encoder). No CGo or FFmpeg shared libraries needed.
package sikasa

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"os"

	"github.com/remko/go-mkvparse"
	"mccoy.space/g/ogg"
)

var opusFrameSamples = [16]int{
	120, 240, 480, 960,   // 0-3: CELT 2.5/5/10/20ms
	1920, 2880,            // 4-5: CELT 40/60ms
	480, 960,              // 6-7: SILK 10/20ms
	1920, 2880,            // 8-9: SILK 40/60ms
	480, 960,              // 10-11: Hybrid 10/20ms
	1920, 2880,            // 12-13: Hybrid 40/60ms
	1920, 3840,            // 14-15: CELT 2-frame 40/80ms total
}

type webm2oggHandler struct {
	mkvparse.DefaultHandler

	audioTrackNum int64
	codecPrivate  []byte
	channels      int64
	sampleRate    float64

	inTrackEntry       bool
	entryTrackNum      int64
	entryCodecID       string
	entryCodecPrivate  []byte
	entryChannels      int64

	clusterTimecode int64
	granulePos      int64
	inBlockGroup    bool

	encoder *ogg.Encoder
	writer  io.Writer

	headersWritten bool
	done           bool
}

func (h *webm2oggHandler) HandleMasterBegin(id mkvparse.ElementID, info mkvparse.ElementInfo) (bool, error) {
	switch id {
	case mkvparse.TrackEntryElement:
		h.inTrackEntry = true
		h.entryTrackNum = 0
		h.entryCodecID = ""
		h.entryCodecPrivate = nil
		h.entryChannels = 0
	case mkvparse.ClusterElement:
		h.clusterTimecode = 0
	case mkvparse.BlockGroupElement:
		h.inBlockGroup = true
	}
	return true, nil
}

func (h *webm2oggHandler) HandleMasterEnd(id mkvparse.ElementID, info mkvparse.ElementInfo) error {
	switch id {
	case mkvparse.TrackEntryElement:
		h.inTrackEntry = false
		if h.entryCodecID == "A_OPUS" && h.entryCodecPrivate != nil {
			h.audioTrackNum = h.entryTrackNum
			h.codecPrivate = h.entryCodecPrivate
			h.channels = h.entryChannels
		}
	case mkvparse.BlockGroupElement:
		h.inBlockGroup = false
	}
	return nil
}

func (h *webm2oggHandler) HandleString(id mkvparse.ElementID, value string, info mkvparse.ElementInfo) error {
	if !h.inTrackEntry {
		return nil
	}
	if id == mkvparse.CodecIDElement {
		h.entryCodecID = value
	}
	return nil
}

func (h *webm2oggHandler) HandleInteger(id mkvparse.ElementID, value int64, info mkvparse.ElementInfo) error {
	switch {
	case h.inTrackEntry && id == mkvparse.TrackNumberElement:
		h.entryTrackNum = value
	case h.inTrackEntry && id == mkvparse.TrackTypeElement && value == 2:
		// Audio track
	case id == mkvparse.TimecodeElement:
		h.clusterTimecode = value
	case id == mkvparse.ChannelsElement:
		if !h.inTrackEntry {
			return nil
		}
		h.entryChannels = value
	}
	return nil
}

func (h *webm2oggHandler) HandleBinary(id mkvparse.ElementID, value []byte, info mkvparse.ElementInfo) error {
	if h.inTrackEntry && id == mkvparse.CodecPrivateElement {
		h.entryCodecPrivate = value
		return nil
	}
	switch id {
	case mkvparse.SimpleBlockElement:
		return h.handleBlock(value)
	case mkvparse.BlockElement:
		if h.inBlockGroup {
			return h.handleBlock(value)
		}
	}
	return nil
}

func (h *webm2oggHandler) handleBlock(data []byte) error {
	if len(data) < 4 {
		return nil
	}
	trackNum, n := decodeVint(data)
	if n == 0 {
		return fmt.Errorf("native-remux-go: invalid vint in SimpleBlock")
	}
	if int64(trackNum) != h.audioTrackNum {
		return nil
	}
	data = data[n:]
	if len(data) < 3 {
		return nil
	}
	blockTimecode := int64(int16(binary.BigEndian.Uint16(data[:2])))
	flags := data[2]
	data = data[3:]

	lacing := (flags >> 5) & 1
	_ = blockTimecode

	if lacing == 0 {
		if len(data) == 0 {
			return nil
		}
		return h.writeOpusPacket(data)
	}

	frameCount := int(data[0]) + 1
	data = data[1:]

	var sizes []int
	lacingType := (flags >> 1) & 3
	switch lacingType {
	case 1:
		var err error
		sizes, err = decodeXiphLacing(data, frameCount)
		if err != nil {
			return err
		}
	case 2:
		var err error
		sizes, err = decodeFixedLacing(data, frameCount)
		if err != nil {
			return err
		}
	case 3:
		var err error
		sizes, err = decodeEBMLLacing(data, frameCount)
		if err != nil {
			return err
		}
	default:
		return nil
	}

	off := 0
	for i, sz := range sizes {
		if off+sz > len(data) {
			break
		}
		frame := data[off : off+sz]
		off += sz
		if i == 0 {
			if err := h.writeOpusPacket(frame); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *webm2oggHandler) writeOpusPacket(packet []byte) error {
	if h.done {
		return nil
	}
	if !h.headersWritten {
		if err := h.writeHeaders(); err != nil {
			return err
		}
	}

	frameSize := packetDuration(packet)
	h.granulePos += int64(frameSize)

	if err := h.encoder.Encode(h.granulePos, [][]byte{packet}); err != nil {
		return fmt.Errorf("native-remux-go: encode: %w", err)
	}
	return nil
}

func (h *webm2oggHandler) writeHeaders() error {
	h.headersWritten = true
	serial := rand.Uint32()
	h.encoder = ogg.NewEncoder(serial, h.writer)

	if err := h.encoder.EncodeBOS(0, [][]byte{h.codecPrivate}); err != nil {
		return fmt.Errorf("native-remux-go: encode BOS: %w", err)
	}

	opusTags := buildOpusTags()
	if err := h.encoder.Encode(0, [][]byte{opusTags}); err != nil {
		return fmt.Errorf("native-remux-go: encode tags: %w", err)
	}
	return nil
}

func (h *webm2oggHandler) finish() error {
	if h.encoder != nil {
		return h.encoder.EncodeEOS(h.granulePos, nil)
	}
	return nil
}

func buildOpusTags() []byte {
	vendor := "sikasa-remuxer"
	var buf []byte
	buf = append(buf, []byte("OpusTags")...)
	// Vendor string length (32-bit LE)
	venLen := make([]byte, 4)
	binary.LittleEndian.PutUint32(venLen, uint32(len(vendor)))
	buf = append(buf, venLen...)
	buf = append(buf, vendor...)
	// Number of comments (32-bit LE) = 0
	buf = append(buf, 0, 0, 0, 0)
	return buf
}

func packetDuration(packet []byte) int {
	if len(packet) == 0 {
		return 960
	}
	toc := packet[0]
	config := (toc >> 1) & 0x0F
	if int(config) < len(opusFrameSamples) {
		return opusFrameSamples[config]
	}
	return 960
}

func decodeVint(data []byte) (uint64, int) {
	if len(data) == 0 {
		return 0, 0
	}
	b := data[0]
	var length int
	var mask byte
	switch {
	case b&0x80 != 0:
		length = 1
		mask = 0x7F
	case b&0x40 != 0:
		length = 2
		mask = 0x3F
	case b&0x20 != 0:
		length = 3
		mask = 0x1F
	case b&0x10 != 0:
		length = 4
		mask = 0x0F
	case b&0x08 != 0:
		length = 5
		mask = 0x07
	case b&0x04 != 0:
		length = 6
		mask = 0x03
	case b&0x02 != 0:
		length = 7
		mask = 0x01
	case b&0x01 != 0:
		length = 8
		mask = 0x00
	default:
		return 0, 0
	}
	if len(data) < length {
		return 0, 0
	}
	var value uint64 = uint64(data[0] & mask)
	for i := 1; i < length; i++ {
		value = (value << 8) | uint64(data[i])
	}
	return value, length
}

func decodeXiphLacing(data []byte, frameCount int) ([]int, error) {
	sizes := make([]int, 0, frameCount)
	off := 0
	for i := 0; i < frameCount-1; i++ {
		var size int
		for {
			if off >= len(data) {
				return nil, fmt.Errorf("native-remux-go: xiph lacing truncated")
			}
			size += int(data[off])
			if data[off] != 255 {
				off++
				break
			}
			off++
		}
		sizes = append(sizes, size)
	}
	remaining := len(data) - off
	sizes = append(sizes, remaining)
	return sizes, nil
}

func decodeFixedLacing(data []byte, frameCount int) ([]int, error) {
	if frameCount == 0 {
		return nil, nil
	}
	frameSize := len(data) / frameCount
	sizes := make([]int, frameCount)
	for i := range sizes {
		sizes[i] = frameSize
	}
	return sizes, nil
}

func decodeEBMLLacing(data []byte, frameCount int) ([]int, error) {
	sizes := make([]int, 0, frameCount)
	off := 0
	var prevSize int
	for i := 0; i < frameCount-1; i++ {
		val, n := decodeVint(data[off:])
		if n == 0 {
			return nil, fmt.Errorf("native-remux-go: ebml lacing truncated")
		}
		off += n
		var size int
		if i == 0 {
			size = int(val)
		} else {
			size = prevSize + int(val) - 0x20000000
		}
		prevSize = size
		sizes = append(sizes, size)
	}
	remaining := len(data) - off
	sizes = append(sizes, remaining)
	return sizes, nil
}

/*
RemuxStreamGo reads WebM/Opus data from an io.Reader and writes Ogg/Opus
to a local file using the pure-Go remuxer (go-mkvparse + mccoy.space/g/ogg).
*/
func RemuxStreamGo(reader io.Reader, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("native-remux-go: create output: %w", err)
	}
	defer f.Close()

	if err := RemuxStreamToWriterGo(reader, f); err != nil {
		return err
	}
	return f.Sync()
}

/*
RemuxStreamToWriterGo reads WebM/Opus data from an io.Reader and writes
Ogg/Opus to the provided io.Writer using the pure-Go remuxer.
*/
func RemuxStreamToWriterGo(reader io.Reader, writer io.Writer) error {
	handler := &webm2oggHandler{
		writer: writer,
	}
	if err := mkvparse.Parse(reader, handler); err != nil {
		return fmt.Errorf("native-remux-go: parse: %w", err)
	}
	if err := handler.finish(); err != nil {
		return fmt.Errorf("native-remux-go: finish: %w", err)
	}
	if handler.encoder == nil {
		return fmt.Errorf("native-remux-go: no opus track found in input")
	}
	return nil
}

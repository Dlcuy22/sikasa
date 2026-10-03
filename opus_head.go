// Package sikasa: opus_head.go
// Purpose: Parse an OpusHead packet (RFC 7845 section 5.1). This is the
// CodecPrivate of a WebM A_OPUS track and the first page of an Ogg Opus stream.
// A trimmed port of github.com/dlcuy22/molo (decode/oggopus.go).
//
// Dependencies:
//   - encoding/binary, errors, fmt
package sikasa

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// opusHeadMinLen is the minimum length of a valid OpusHead packet.
const opusHeadMinLen = 19

// opusHeadInfo is the parsed ID header.
type opusHeadInfo struct {
	preSkip  int
	gain     int16
	channels int
}

// parseOpusHead validates and decodes an OpusHead packet. Only mapping family 0
// (mono or stereo) is accepted, the case every normal encoder emits.
func parseOpusHead(packet []byte) (opusHeadInfo, error) {
	if len(packet) < opusHeadMinLen {
		return opusHeadInfo{}, fmt.Errorf("sikasa: OpusHead is %d bytes", len(packet))
	}
	if string(packet[:8]) != "OpusHead" {
		return opusHeadInfo{}, errors.New("sikasa: not an OpusHead packet")
	}
	// RFC 7845 section 5.1: the upper four version bits are the major version.
	if version := packet[8]; version>>4 != 0 {
		return opusHeadInfo{}, fmt.Errorf("sikasa: unsupported OpusHead version %d", version)
	}
	channels := int(packet[9])
	if channels == 0 {
		return opusHeadInfo{}, errors.New("sikasa: OpusHead declares zero channels")
	}
	if family := packet[18]; family != 0 {
		return opusHeadInfo{}, fmt.Errorf("sikasa: unsupported Opus mapping family %d", family)
	}
	if channels != 1 && channels != 2 {
		return opusHeadInfo{}, fmt.Errorf("sikasa: mapping family 0 with %d channels", channels)
	}

	return opusHeadInfo{
		preSkip:  int(binary.LittleEndian.Uint16(packet[10:12])),
		gain:     int16(binary.LittleEndian.Uint16(packet[16:18])),
		channels: channels,
	}, nil
}

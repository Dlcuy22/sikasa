// Package sikasa: ebml.go
// Purpose: EBML primitives (RFC 8794) used by the pure-Go WebM/Opus reader.
// This is a trimmed port of the EBML layer from github.com/dlcuy22/molo
// (decode/ebml.go): it reads element IDs and sizes, classifies the two
// variable-integer flavours, and walks a bounded tree of master elements.
//
// Key Components:
//   - ebmlID():          an Element ID VINT with the marker bit kept
//   - ebmlVint():        a Data Size VINT with the marker bit stripped
//   - ebmlElement / ebmlChildren(): one element header and a bounded walk
//
// Dependencies:
//   - encoding/binary, errors, fmt, io, math
package sikasa

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// ebmlMaxIDLen and ebmlMaxSizeLen are the RFC 8794 ceilings (WebM is stricter
// but identical in practice: 4 and 8).
const (
	ebmlMaxIDLen   = 4
	ebmlMaxSizeLen = 8
)

// Global element IDs the walker needs to recognise and skip.
const (
	ebmlIDVoid  = 0xEC
	ebmlIDCRC32 = 0xBF
)

// Errors from the EBML layer.
var (
	errEBMLBadVint   = errors.New("sikasa: invalid EBML variable-length integer")
	errEBMLTruncated = errors.New("sikasa: truncated EBML element")
)

// ebmlID reads the Element ID at off. The returned id keeps its marker bit, so
// element IDs are compared against their table values (RFC 8794 section 5).
func ebmlID(b []byte, off int) (id uint32, width int, err error) {
	if off >= len(b) {
		return 0, 0, fmt.Errorf("%w: id past end at %d", errEBMLTruncated, off)
	}
	first := b[off]
	if first == 0 {
		return 0, 0, fmt.Errorf("%w: zero leading byte at %d", errEBMLBadVint, off)
	}

	width = 1
	mask := byte(0x80)
	for first&mask == 0 {
		mask >>= 1
		width++
		if width > ebmlMaxIDLen {
			return 0, 0, fmt.Errorf("%w: id longer than %d bytes at %d", errEBMLBadVint, ebmlMaxIDLen, off)
		}
	}
	// The all-ones 1-byte ID is reserved; Matroska does not use it.
	if width == 1 && first == 0xFF {
		return 0, 0, fmt.Errorf("%w: reserved id 0xFF at %d", errEBMLBadVint, off)
	}
	if off+width > len(b) {
		return 0, 0, fmt.Errorf("%w: id needs %d bytes at %d", errEBMLTruncated, width, off)
	}

	id = 0
	for i := range width {
		id = id<<8 | uint32(b[off+i])
	}

	return id, width, nil
}

// ebmlVint reads a Data Size VINT at off. The marker bit is stripped (RFC 8794
// section 6); unknown is true when every data bit is set, which a master element
// uses to mean "size not known here".
func ebmlVint(b []byte, off int) (val uint64, width int, unknown bool, err error) {
	if off >= len(b) {
		return 0, 0, false, fmt.Errorf("%w: vint past end at %d", errEBMLTruncated, off)
	}
	first := b[off]
	if first == 0 {
		return 0, 0, false, fmt.Errorf("%w: zero leading byte at %d", errEBMLBadVint, off)
	}

	width = 1
	mask := byte(0x80)
	for first&mask == 0 {
		mask >>= 1
		width++
		if width > ebmlMaxSizeLen {
			return 0, 0, false, fmt.Errorf("%w: size longer than %d bytes at %d", errEBMLBadVint, ebmlMaxSizeLen, off)
		}
	}
	if off+width > len(b) {
		return 0, 0, false, fmt.Errorf("%w: size needs %d bytes at %d", errEBMLTruncated, width, off)
	}

	val = uint64(first & (mask - 1))
	for i := 1; i < width; i++ {
		val = val<<8 | uint64(b[off+i])
	}
	unknown = val == uint64(1)<<(7*width)-1

	return val, width, unknown, nil
}

// ebmlElement is one element whose header has been read. DataOff and Next are
// absolute offsets into the same buffer the header came from. Next is zero for
// an unknown-size element because its end is not encoded in its own header.
type ebmlElement struct {
	ID      uint32
	Size    uint64
	Unknown bool
	DataOff int64
	Next    int64
}

// readEBMLHeader parses the element header at off and resolves Next for a
// known-size element.
func readEBMLHeader(b []byte, off int64) (ebmlElement, error) {
	id, idw, err := ebmlID(b, int(off))
	if err != nil {
		return ebmlElement{}, err
	}
	size, szw, unknown, err := ebmlVint(b, int(off)+idw)
	if err != nil {
		return ebmlElement{}, err
	}

	e := ebmlElement{
		ID:      id,
		Size:    size,
		Unknown: unknown,
		DataOff: off + int64(idw+szw),
	}
	if !unknown {
		e.Next = e.DataOff + int64(size)
	}

	return e, nil
}

// ebmlChildren walks the direct children of the master element starting at
// start and ending before end, calling fn for each. Void and CRC-32 are skipped
// (RFC 8794 section 11.3); an unknown-size child is reported with Next set to
// the parent's end so fn sees a bounded extent.
func ebmlChildren(b []byte, start, end int64, fn func(ebmlElement) error) error {
	if start > end || end > int64(len(b)) {
		return fmt.Errorf("%w: child range [%d, %d) outside %d bytes", errEBMLTruncated, start, end, len(b))
	}

	off := start
	for off < end {
		e, err := readEBMLHeader(b, off)
		if err != nil {
			return err
		}
		if e.Unknown {
			e.Next = end
		} else if e.Next > end {
			return fmt.Errorf("%w: element %#x ends at %d past %d", errEBMLTruncated, e.ID, e.Next, end)
		}

		if e.ID != ebmlIDVoid && e.ID != ebmlIDCRC32 {
			if err := fn(e); err != nil {
				return err
			}
		}

		if e.Next <= off {
			return fmt.Errorf("%w: element %#x at %d did not advance", errEBMLBadVint, e.ID, off)
		}
		off = e.Next
	}

	return nil
}

// ebmlUint decodes an unsigned integer element (RFC 8794 section 7.1).
func ebmlUint(b []byte, off, size int64) (uint64, error) {
	if size < 0 || size > 8 {
		return 0, fmt.Errorf("%w: uinteger size %d", errEBMLBadVint, size)
	}
	if off < 0 || off+size > int64(len(b)) {
		return 0, fmt.Errorf("%w: uinteger at %d size %d", errEBMLTruncated, off, size)
	}

	var v uint64
	for i := int64(0); i < size; i++ {
		v = v<<8 | uint64(b[off+i])
	}

	return v, nil
}

// ebmlFloat decodes a float element (RFC 8794 section 7.2): 4 bytes are an
// IEEE 754 single, 8 are a double, both big-endian.
func ebmlFloat(b []byte, off, size int64) (float64, error) {
	if off < 0 || off+size > int64(len(b)) {
		return 0, fmt.Errorf("%w: float at %d size %d", errEBMLTruncated, off, size)
	}
	switch size {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b[off : off+4]))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b[off : off+8])), nil
	default:
		return 0, fmt.Errorf("%w: float size %d", errEBMLBadVint, size)
	}
}

// readFullAt reads size bytes at off into a fresh slice, bounded by limit. It
// turns a short read or an oversized element into errEBMLTruncated instead of
// leaking io.ErrUnexpectedEOF.
func readFullAt(r io.ReaderAt, off int64, size int64, limit int64) ([]byte, error) {
	if size < 0 || off < 0 {
		return nil, fmt.Errorf("%w: negative read off %d size %d", errEBMLTruncated, off, size)
	}
	if limit < off || off+size > limit {
		return nil, fmt.Errorf("%w: read of %d bytes at %d exceeds limit %d", errEBMLTruncated, size, off, limit)
	}
	buf := make([]byte, size)
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("%w: read %d bytes at %d: %w", errEBMLTruncated, size, off, err)
	}

	return buf, nil
}

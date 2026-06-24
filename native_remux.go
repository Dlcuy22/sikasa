// Package sikasa: native_remux.go
// Purpose: Implements dynamic bindings to FFmpeg shared libraries (libavutil,
// libavcodec, libavformat) using purego, and provides native container-level
// remuxing from WebM/Opus input to Ogg/Opus output without transcoding.
//
// Key Components:
//   - RemuxStream(): Native stream-copy remuxer that reads a WebM stream and writes Ogg/Opus to file.
//   - loadFFmpegLibraries(): Automatically resolves and loads shared library handles.
//   - findCodecParOffset(): Dynamically finds offset of codecpar field in AVStream.
//
// Dependencies:
//   - github.com/ebitengine/purego: dynamically registers C functions without Cgo
//
package sikasa

import (
	"fmt"
	"io"
	"log"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

type AVRational struct {
	Num int32
	Den int32
}

var (
	libavutilHandle   uintptr
	libavcodecHandle  uintptr
	libavformatHandle uintptr
	remuxInitOnce     sync.Once
	remuxInitErr      error

	// libavutil functions
	av_malloc       func(size uintptr) uintptr
	av_packet_alloc func() uintptr
	av_packet_free  func(pkt *uintptr)
	av_packet_unref func(pkt uintptr)

	// libavformat functions
	avformat_version               func() uint32
	avio_alloc_context             func(buffer uintptr, bufferSize int32, writeFlag int32, opaque uintptr, readPacket uintptr, writePacket uintptr, seek uintptr) uintptr
	avformat_alloc_context         func() uintptr
	avformat_open_input            func(ps *uintptr, url *byte, fmt uintptr, options *uintptr) int32
	avformat_find_stream_info      func(ic uintptr, options *uintptr) int32
	avformat_alloc_output_context2 func(ctx *uintptr, oformat uintptr, formatName *byte, fileName *byte) int32
	avformat_new_stream            func(s uintptr, c uintptr) uintptr
	avcodec_parameters_copy        func(dst uintptr, src uintptr) int32
	avformat_write_header          func(s uintptr, options *uintptr) int32
	av_read_frame                  func(s uintptr, pkt uintptr) int32
	av_interleaved_write_frame     func(s uintptr, pkt uintptr) int32
	av_write_trailer               func(s uintptr) int32
	avformat_close_input           func(ps *uintptr)
	avformat_free_context          func(s uintptr)
	avio_open2                     func(pb *uintptr, url *byte, flags int32, intput uintptr, options *uintptr) int32
	avio_close                     func(pb uintptr) int32

	// Custom I/O registry for read callbacks
	readersMu    sync.Mutex
	readers      = make(map[uintptr]io.Reader)
	nextReaderID uintptr = 1

	// Custom I/O registry for write callbacks
	writersMu    sync.Mutex
	writers      = make(map[uintptr]io.Writer)
	nextWriterID uintptr = 1
)

/*
registerReader registers a reader and returns a unique ID.

    params:
          r: Go reader to register
    returns:
          uintptr: unique ID to map with opaque C parameter
*/
func registerReader(r io.Reader) uintptr {
	readersMu.Lock()
	defer readersMu.Unlock()
	id := nextReaderID
	nextReaderID++
	readers[id] = r
	return id
}

/*
unregisterReader unregisters a reader by its ID.

    params:
          id: registry key
*/
func unregisterReader(id uintptr) {
	readersMu.Lock()
	defer readersMu.Unlock()
	delete(readers, id)
}

/*
getReader retrieves a registered reader by ID.

    params:
          id: registry key
    returns:
          io.Reader: the mapped reader
*/
func getReader(id uintptr) io.Reader {
	readersMu.Lock()
	defer readersMu.Unlock()
	return readers[id]
}

/*
getAvutilNames returns candidate library names for libavutil based on OS.

    returns:
          []string: slice of naming candidates
*/
func getAvutilNames() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			"avutil-60.dll",
			"avutil-59.dll",
			"avutil-58.dll",
			"avutil-57.dll",
			"avutil-56.dll",
		}
	case "darwin":
		return []string{
			"libavutil.dylib",
			"libavutil.59.dylib",
			"libavutil.58.dylib",
			"libavutil.57.dylib",
			"libavutil.56.dylib",
		}
	default:
		return []string{
			"libavutil.so.59",
			"libavutil.so.58",
			"libavutil.so.57",
			"libavutil.so.56",
			"libavutil.so",
		}
	}
}

/*
getAvcodecNames returns candidate library names for libavcodec based on OS.

    returns:
          []string: slice of naming candidates
*/
func getAvcodecNames() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			"avcodec-61.dll",
			"avcodec-60.dll",
			"avcodec-59.dll",
			"avcodec-58.dll",
		}
	case "darwin":
		return []string{
			"libavcodec.dylib",
			"libavcodec.61.dylib",
			"libavcodec.60.dylib",
			"libavcodec.59.dylib",
			"libavcodec.58.dylib",
		}
	default:
		return []string{
			"libavcodec.so.61",
			"libavcodec.so.60",
			"libavcodec.so.59",
			"libavcodec.so.58",
			"libavcodec.so",
		}
	}
}

/*
getAvformatNames returns candidate library names for libavformat based on OS.

    returns:
          []string: slice of naming candidates
*/
func getAvformatNames() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			"avformat-61.dll",
			"avformat-60.dll",
			"avformat-59.dll",
			"avformat-58.dll",
		}
	case "darwin":
		return []string{
			"libavformat.dylib",
			"libavformat.61.dylib",
			"libavformat.60.dylib",
			"libavformat.59.dylib",
			"libavformat.58.dylib",
		}
	default:
		return []string{
			"libavformat.so.61",
			"libavformat.so.60",
			"libavformat.so.59",
			"libavformat.so.58",
			"libavformat.so",
		}
	}
}

/*
loadLibHelper attempts to load a library from a list of candidate names.

    params:
          names: slice of name variations
    returns:
          uintptr: library handle
          error: if none of the candidates could be loaded
*/
func loadLibHelper(names []string) (uintptr, error) {
	var lastErr error
	for _, name := range names {
		handle, err := purego.Dlopen(name, purego.RTLD_LAZY)
		if err == nil {
			return handle, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

/*
loadFFmpegLibraries tries to load util, codec, and format shared libraries.

    returns:
          error: if loading fails for any of the libraries
*/
func loadFFmpegLibraries() error {
	var err error
	libavutilHandle, err = loadLibHelper(getAvutilNames())
	if err != nil {
		return fmt.Errorf("failed to load libavutil: %w", err)
	}

	libavcodecHandle, err = loadLibHelper(getAvcodecNames())
	if err != nil {
		return fmt.Errorf("failed to load libavcodec: %w", err)
	}

	libavformatHandle, err = loadLibHelper(getAvformatNames())
	if err != nil {
		return fmt.Errorf("failed to load libavformat: %w", err)
	}

	// Register libavutil symbols
	purego.RegisterLibFunc(&av_malloc, libavutilHandle, "av_malloc")

	// Register libavcodec symbols
	purego.RegisterLibFunc(&av_packet_alloc, libavcodecHandle, "av_packet_alloc")
	purego.RegisterLibFunc(&av_packet_free, libavcodecHandle, "av_packet_free")
	purego.RegisterLibFunc(&av_packet_unref, libavcodecHandle, "av_packet_unref")

	// Register libavformat symbols
	purego.RegisterLibFunc(&avformat_version, libavformatHandle, "avformat_version")
	purego.RegisterLibFunc(&avio_alloc_context, libavformatHandle, "avio_alloc_context")
	purego.RegisterLibFunc(&avformat_alloc_context, libavformatHandle, "avformat_alloc_context")
	purego.RegisterLibFunc(&avformat_open_input, libavformatHandle, "avformat_open_input")
	purego.RegisterLibFunc(&avformat_find_stream_info, libavformatHandle, "avformat_find_stream_info")
	purego.RegisterLibFunc(&avformat_alloc_output_context2, libavformatHandle, "avformat_alloc_output_context2")
	purego.RegisterLibFunc(&avformat_new_stream, libavformatHandle, "avformat_new_stream")
	purego.RegisterLibFunc(&avcodec_parameters_copy, libavformatHandle, "avcodec_parameters_copy")
	purego.RegisterLibFunc(&avformat_write_header, libavformatHandle, "avformat_write_header")
	purego.RegisterLibFunc(&av_read_frame, libavformatHandle, "av_read_frame")
	purego.RegisterLibFunc(&av_interleaved_write_frame, libavformatHandle, "av_interleaved_write_frame")
	purego.RegisterLibFunc(&av_write_trailer, libavformatHandle, "av_write_trailer")
	purego.RegisterLibFunc(&avformat_close_input, libavformatHandle, "avformat_close_input")
	purego.RegisterLibFunc(&avformat_free_context, libavformatHandle, "avformat_free_context")
	purego.RegisterLibFunc(&avio_open2, libavformatHandle, "avio_open2")
	purego.RegisterLibFunc(&avio_close, libavformatHandle, "avio_close")

	return nil
}

/*
initNativeRemuxer initializes libraries and triggers automated dependency
installation if libraries are missing.

    returns:
          error: if initialization fails
*/
func initNativeRemuxer() error {
	remuxInitOnce.Do(func() {
		// First attempt
		err := loadFFmpegLibraries()
		if err == nil {
			return
		}

		// Fallback to auto-installer
		if errInstall := ensureDependencies(); errInstall != nil {
			remuxInitErr = fmt.Errorf("failed to auto-install dependencies: %w; original loading error: %v", errInstall, err)
			return
		}

		// Second attempt after install
		err = loadFFmpegLibraries()
		if err != nil {
			remuxInitErr = fmt.Errorf("dependencies installed but still failed to load FFmpeg libraries: %w", err)
			return
		}
	})
	return remuxInitErr
}

/*
getCodecParOffset returns the offset of codecpar within AVStream based on FFmpeg version.

    returns:
          uintptr: offset of codecpar
*/
func getCodecParOffset() uintptr {
	if avformat_version == nil {
		return 16 // default to FFmpeg 7.x
	}
	ver := avformat_version()
	major := ver >> 16
	switch major {
	case 58:
		return 120 // FFmpeg 4.x
	case 59, 60:
		return 104 // FFmpeg 5.x, 6.x
	default:
		return 16 // FFmpeg 7.x+
	}
}

/*
getStreamCodecID reads the codec_id from AVCodecParameters.

    params:
          streamPtr: pointer to AVStream
          codecParOffset: offset of codecpar in AVStream
    returns:
          int32: codec ID value
*/
func getStreamCodecID(streamPtr uintptr, codecParOffset uintptr) int32 {
	codecParPtr := *(*uintptr)(unsafe.Pointer(streamPtr + codecParOffset))
	if codecParPtr == 0 {
		return 0
	}
	return *(*int32)(unsafe.Pointer(codecParPtr + 4))
}

/*
getStreamCodecType reads the codec_type from AVCodecParameters.

    params:
          streamPtr: pointer to AVStream
          codecParOffset: offset of codecpar in AVStream
    returns:
          int32: codec type value
*/
func getStreamCodecType(streamPtr uintptr, codecParOffset uintptr) int32 {
	codecParPtr := *(*uintptr)(unsafe.Pointer(streamPtr + codecParOffset))
	if codecParPtr == 0 {
		return -1
	}
	return *(*int32)(unsafe.Pointer(codecParPtr))
}

/*
getStreamTimeBase reads the time_base AVRational struct from AVStream.

    params:
          streamPtr: pointer to AVStream
    returns:
          AVRational: read value
*/
func getStreamTimeBase(streamPtr uintptr) AVRational {
	var offset uintptr = 32 // default to FFmpeg 7.x
	if avformat_version != nil {
		ver := avformat_version()
		major := ver >> 16
		if major <= 60 {
			offset = 16 // FFmpeg 4.x, 5.x, 6.x
		}
	}
	ptr := unsafe.Pointer(streamPtr + offset)
	return *(*AVRational)(ptr)
}

/*
avRescale performs basic scaling logic.

    params:
          a: value to scale
          b: numerator
          c: denominator
    returns:
          int64: rescaled value
*/
func avRescale(a, b, c int64) int64 {
	if c == 0 {
		return 0
	}
	return (a * b) / c
}

/*
avRescaleQ rescales raw timestamps using source/destination timebases.

    params:
          a: raw value
          bq: source AVRational
          cq: target AVRational
    returns:
          int64: rescaled timestamp value
*/
func avRescaleQ(a int64, bq, cq AVRational) int64 {
	return avRescale(a, int64(bq.Num)*int64(cq.Den), int64(bq.Den)*int64(cq.Num))
}

/*
RemuxStream reads input WebM/Opus data from a Go reader and writes Ogg/Opus container
to a local file path natively using FFmpeg shared libraries.

    params:
          reader:  source stream reader
          outPath: destination path for Ogg file
    returns:
          error:   on initialization or remuxing failure
*/
func RemuxStream(reader io.Reader, outPath string) error {
	if err := initNativeRemuxer(); err != nil {
		return fmt.Errorf("native remuxer not available: %w", err)
	}

	// 1. Setup custom input I/O context
	const ioBufSize = 32768
	avioBuffer := av_malloc(ioBufSize)
	if avioBuffer == 0 {
		return fmt.Errorf("failed to allocate avio buffer")
	}

	readerID := registerReader(reader)
	defer unregisterReader(readerID)

	readCallback := purego.NewCallback(func(opaque uintptr, buf uintptr, bufSize int32) int32 {
		r := getReader(opaque)
		if r == nil {
			return -1 // EIO
		}
		goBuf := unsafe.Slice((*byte)(unsafe.Pointer(buf)), bufSize)
		n, err := r.Read(goBuf)
		if n > 0 {
			return int32(n)
		}
		if err == io.EOF {
			return -541478725 // AVERROR_EOF
		}
		return -5 // EIO
	})

	pb := avio_alloc_context(avioBuffer, ioBufSize, 0, readerID, readCallback, 0, 0)
	if pb == 0 {
		return fmt.Errorf("failed to allocate avio context")
	}

	// 2. Open input format context
	inFormatCtx := avformat_alloc_context()
	if inFormatCtx == 0 {
		return fmt.Errorf("failed to allocate input format context")
	}
	*(*uintptr)(unsafe.Pointer(inFormatCtx + 32)) = pb // Set pb field at offset 32

	inFormatCtxPtr := inFormatCtx
	if ret := avformat_open_input(&inFormatCtxPtr, nil, 0, nil); ret < 0 {
		return fmt.Errorf("avformat_open_input failed: %d", ret)
	}

	if ret := avformat_find_stream_info(inFormatCtxPtr, nil); ret < 0 {
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("avformat_find_stream_info failed: %d", ret)
	}

	// 3. Find input audio stream
	nbStreams := *(*uint32)(unsafe.Pointer(inFormatCtxPtr + 44))
	streamsPtr := *(*uintptr)(unsafe.Pointer(inFormatCtxPtr + 48))
	var audioStreamIndex int = -1
	var audioStreamPtr uintptr
	codecParOffset := getCodecParOffset()

	log.Printf("DEBUG RemuxStream: nbStreams=%d codecParOffset=%d", nbStreams, codecParOffset)
	for i := uint32(0); i < nbStreams; i++ {
		streamPtr := *(*uintptr)(unsafe.Pointer(streamsPtr + uintptr(i)*8))
		codecID := getStreamCodecID(streamPtr, codecParOffset)
		codecType := getStreamCodecType(streamPtr, codecParOffset)
		log.Printf("DEBUG Stream %d: streamPtr=%x codecID=%d codecType=%d", i, streamPtr, codecID, codecType)
		if codecType == 1 && codecID == 86076 {
			audioStreamIndex = int(i)
			audioStreamPtr = streamPtr
			break
		}
	}

	if audioStreamIndex == -1 {
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("opus audio stream not found in input")
	}

	// 4. Setup output format context
	var outFormatCtx uintptr
	cOutPath := append([]byte(outPath), 0)
	if ret := avformat_alloc_output_context2(&outFormatCtx, 0, nil, &cOutPath[0]); ret < 0 {
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("avformat_alloc_output_context2 failed: %d", ret)
	}

	outStreamPtr := avformat_new_stream(outFormatCtx, 0)
	if outStreamPtr == 0 {
		avformat_free_context(outFormatCtx)
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("failed to allocate output stream")
	}

	inCodecPar := *(*uintptr)(unsafe.Pointer(audioStreamPtr + codecParOffset))
	outCodecPar := *(*uintptr)(unsafe.Pointer(outStreamPtr + codecParOffset))

	if ret := avcodec_parameters_copy(outCodecPar, inCodecPar); ret < 0 {
		avformat_free_context(outFormatCtx)
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("avcodec_parameters_copy failed: %d", ret)
	}
	*(*uint32)(unsafe.Pointer(outCodecPar + 8)) = 0 // Reset codec_tag (offset 8) to bypass strict tag validation

	// 5. Open output file I/O
	var outPb uintptr
	if ret := avio_open2(&outPb, &cOutPath[0], 2, 0, nil); ret < 0 {
		avformat_free_context(outFormatCtx)
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("avio_open2 failed for output %s: %d", outPath, ret)
	}
	*(*uintptr)(unsafe.Pointer(outFormatCtx + 32)) = outPb // Set pb field at offset 32 for output context

	// 6. Write stream header
	if ret := avformat_write_header(outFormatCtx, nil); ret < 0 {
		avio_close(outPb)
		avformat_free_context(outFormatCtx)
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("avformat_write_header failed: %d", ret)
	}

	// 7. Remux packets
	pkt := av_packet_alloc()
	if pkt == 0 {
		avio_close(outPb)
		avformat_free_context(outFormatCtx)
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("failed to allocate packet")
	}

	for {
		if ret := av_read_frame(inFormatCtxPtr, pkt); ret < 0 {
			break // EOF or error
		}

		streamIdx := *(*int32)(unsafe.Pointer(pkt + 36))
		if int(streamIdx) == audioStreamIndex {
			// Map packet to output stream (index 0)
			*(*int32)(unsafe.Pointer(pkt + 36)) = 0

			// Rescale timestamps
			inTB := getStreamTimeBase(audioStreamPtr)
			outTB := getStreamTimeBase(outStreamPtr)

			pts := *(*int64)(unsafe.Pointer(pkt + 8))
			*(*int64)(unsafe.Pointer(pkt + 8)) = avRescaleQ(pts, inTB, outTB)

			dts := *(*int64)(unsafe.Pointer(pkt + 16))
			*(*int64)(unsafe.Pointer(pkt + 16)) = avRescaleQ(dts, inTB, outTB)

			duration := *(*int64)(unsafe.Pointer(pkt + 48))
			*(*int64)(unsafe.Pointer(pkt + 48)) = avRescaleQ(duration, inTB, outTB)

			_ = av_interleaved_write_frame(outFormatCtx, pkt)
		}
		av_packet_unref(pkt)
	}

	// 8. Write trailer and finalize
	av_write_trailer(outFormatCtx)
	av_packet_free(&pkt)

	// Clean up resources
	avio_close(outPb)
	avformat_free_context(outFormatCtx)
	avformat_close_input(&inFormatCtxPtr)

	runtime.KeepAlive(readCallback)
	return nil
}

/*
registerWriter registers a writer and returns a unique ID.

    params:
          w: Go writer to register
    returns:
          uintptr: unique ID to map with opaque C parameter
*/
func registerWriter(w io.Writer) uintptr {
	writersMu.Lock()
	defer writersMu.Unlock()
	id := nextWriterID
	nextWriterID++
	writers[id] = w
	return id
}

/*
unregisterWriter unregisters a writer by its ID.

    params:
          id: registry key
*/
func unregisterWriter(id uintptr) {
	writersMu.Lock()
	defer writersMu.Unlock()
	delete(writers, id)
}

/*
getWriter retrieves a registered writer by ID.

    params:
          id: registry key
    returns:
          io.Writer: the mapped writer
*/
func getWriter(id uintptr) io.Writer {
	writersMu.Lock()
	defer writersMu.Unlock()
	return writers[id]
}

/*
RemuxStreamToWriter reads WebM/Opus data from reader, remuxes it, and writes
Ogg/Opus container data directly to writer using FFmpeg shared libraries.

    params:
          reader: source reader
          writer: target writer
    returns:
          error:  on failure
*/
func RemuxStreamToWriter(reader io.Reader, writer io.Writer) error {
	if err := initNativeRemuxer(); err != nil {
		return fmt.Errorf("native remuxer not available: %w", err)
	}

	const ioBufSize = 32768

	// 1. Input pb setup
	inAvioBuffer := av_malloc(ioBufSize)
	if inAvioBuffer == 0 {
		return fmt.Errorf("failed to allocate input avio buffer")
	}
	readerID := registerReader(reader)
	defer unregisterReader(readerID)

	readCallback := purego.NewCallback(func(opaque uintptr, buf uintptr, bufSize int32) int32 {
		r := getReader(opaque)
		if r == nil {
			return -1
		}
		goBuf := unsafe.Slice((*byte)(unsafe.Pointer(buf)), bufSize)
		n, err := r.Read(goBuf)
		if n > 0 {
			return int32(n)
		}
		if err == io.EOF {
			return -541478725 // AVERROR_EOF
		}
		return -5 // EIO
	})

	inPb := avio_alloc_context(inAvioBuffer, ioBufSize, 0, readerID, readCallback, 0, 0)
	if inPb == 0 {
		return fmt.Errorf("failed to allocate input avio context")
	}

	// 2. Output pb setup
	outAvioBuffer := av_malloc(ioBufSize)
	if outAvioBuffer == 0 {
		return fmt.Errorf("failed to allocate output avio buffer")
	}
	writerID := registerWriter(writer)
	defer unregisterWriter(writerID)

	writeCallback := purego.NewCallback(func(opaque uintptr, buf uintptr, bufSize int32) int32 {
		w := getWriter(opaque)
		if w == nil {
			return -1
		}
		goBuf := unsafe.Slice((*byte)(unsafe.Pointer(buf)), bufSize)
		n, _ := w.Write(goBuf)
		if n > 0 {
			return int32(n)
		}
		return -5 // EIO
	})

	outPb := avio_alloc_context(outAvioBuffer, ioBufSize, 1, writerID, 0, writeCallback, 0)
	if outPb == 0 {
		return fmt.Errorf("failed to allocate output avio context")
	}

	// 3. Open contexts
	inFormatCtx := avformat_alloc_context()
	if inFormatCtx == 0 {
		return fmt.Errorf("failed to allocate input format context")
	}
	*(*uintptr)(unsafe.Pointer(inFormatCtx + 32)) = inPb // Set pb field at offset 32

	inFormatCtxPtr := inFormatCtx
	if ret := avformat_open_input(&inFormatCtxPtr, nil, 0, nil); ret < 0 {
		return fmt.Errorf("avformat_open_input failed: %d", ret)
	}

	if ret := avformat_find_stream_info(inFormatCtxPtr, nil); ret < 0 {
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("avformat_find_stream_info failed: %d", ret)
	}

	// 4. Find input audio stream
	nbStreams := *(*uint32)(unsafe.Pointer(inFormatCtxPtr + 44))
	streamsPtr := *(*uintptr)(unsafe.Pointer(inFormatCtxPtr + 48))
	var audioStreamIndex int = -1
	var audioStreamPtr uintptr
	codecParOffset := getCodecParOffset()

	log.Printf("DEBUG RemuxStreamToWriter: nbStreams=%d codecParOffset=%d", nbStreams, codecParOffset)
	for i := uint32(0); i < nbStreams; i++ {
		streamPtr := *(*uintptr)(unsafe.Pointer(streamsPtr + uintptr(i)*8))
		codecID := getStreamCodecID(streamPtr, codecParOffset)
		codecType := getStreamCodecType(streamPtr, codecParOffset)
		log.Printf("DEBUG Stream %d: streamPtr=%x codecID=%d codecType=%d", i, streamPtr, codecID, codecType)
		if codecType == 1 && codecID == 86076 {
			audioStreamIndex = int(i)
			audioStreamPtr = streamPtr
			break
		}
	}

	if audioStreamIndex == -1 {
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("opus audio stream not found in input")
	}

	// 5. Setup output format context
	var outFormatCtx uintptr
	cOgg := append([]byte("ogg"), 0)
	if ret := avformat_alloc_output_context2(&outFormatCtx, 0, &cOgg[0], nil); ret < 0 {
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("avformat_alloc_output_context2 failed: %d", ret)
	}
	*(*uintptr)(unsafe.Pointer(outFormatCtx + 32)) = outPb // Set pb field at offset 32

	outStreamPtr := avformat_new_stream(outFormatCtx, 0)
	if outStreamPtr == 0 {
		avformat_free_context(outFormatCtx)
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("failed to allocate output stream")
	}

	inCodecPar := *(*uintptr)(unsafe.Pointer(audioStreamPtr + codecParOffset))
	outCodecPar := *(*uintptr)(unsafe.Pointer(outStreamPtr + codecParOffset))

	if ret := avcodec_parameters_copy(outCodecPar, inCodecPar); ret < 0 {
		avformat_free_context(outFormatCtx)
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("avcodec_parameters_copy failed: %d", ret)
	}
	*(*uint32)(unsafe.Pointer(outCodecPar + 8)) = 0 // Reset codec_tag (offset 8) to bypass strict tag validation

	// 6. Write stream header
	if ret := avformat_write_header(outFormatCtx, nil); ret < 0 {
		avformat_free_context(outFormatCtx)
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("avformat_write_header failed: %d", ret)
	}

	// 7. Remux packets
	pkt := av_packet_alloc()
	if pkt == 0 {
		avformat_free_context(outFormatCtx)
		avformat_close_input(&inFormatCtxPtr)
		return fmt.Errorf("failed to allocate packet")
	}

	for {
		if ret := av_read_frame(inFormatCtxPtr, pkt); ret < 0 {
			break // EOF or error
		}

		streamIdx := *(*int32)(unsafe.Pointer(pkt + 36))
		if int(streamIdx) == audioStreamIndex {
			// Map packet to output stream (index 0)
			*(*int32)(unsafe.Pointer(pkt + 36)) = 0

			// Rescale timestamps
			inTB := getStreamTimeBase(audioStreamPtr)
			outTB := getStreamTimeBase(outStreamPtr)

			pts := *(*int64)(unsafe.Pointer(pkt + 8))
			*(*int64)(unsafe.Pointer(pkt + 8)) = avRescaleQ(pts, inTB, outTB)

			dts := *(*int64)(unsafe.Pointer(pkt + 16))
			*(*int64)(unsafe.Pointer(pkt + 16)) = avRescaleQ(dts, inTB, outTB)

			duration := *(*int64)(unsafe.Pointer(pkt + 48))
			*(*int64)(unsafe.Pointer(pkt + 48)) = avRescaleQ(duration, inTB, outTB)

			_ = av_interleaved_write_frame(outFormatCtx, pkt)
		}
		av_packet_unref(pkt)
	}

	// 8. Write trailer and finalize
	av_write_trailer(outFormatCtx)
	av_packet_free(&pkt)

	// Clean up resources
	avformat_free_context(outFormatCtx)
	avformat_close_input(&inFormatCtxPtr)

	runtime.KeepAlive(readCallback)
	runtime.KeepAlive(writeCallback)
	return nil
}

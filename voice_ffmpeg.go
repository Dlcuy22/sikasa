// Package sikasa: voice_ffmpeg.go
// Purpose: Manages FFmpeg subprocesses that produce Ogg-Opus output suitable
// for direct streaming into a Discord voice connection.
//
// Key Components:
//   - ffmpegProcess:        wraps an *exec.Cmd plus its stdout pipe
//   - spawnTranscode():     decode any input format and re-encode to Opus
//   - spawnPassthrough():   stream-copy already-Opus input (zero CPU cost)
//   - spawnFromStdin():     accept piped input from another process (yt-dlp)
//
// Dependencies:
//   - os/exec: subprocess lifecycle
//
// Note: All spawn functions configure FFmpeg to write Ogg-Opus to stdout.
// Stderr is silenced to avoid noise; for debugging, route it to os.Stderr.
package sikasa

import (
	"fmt"
	"io"
	"os/exec"
)

// ffmpegProcess holds a running FFmpeg invocation and its stdout reader.
//
// Key Fields:
//   - cmd:      the os/exec command, kept for Kill() and Wait()
//   - stdout:   Ogg-Opus byte stream that the parser consumes
//   - upstream: optional process feeding ffmpeg's stdin (e.g. yt-dlp). Tracked
//               so Kill() can tear it down explicitly; SIGPIPE alone is not
//               enough when the upstream is blocked on a network read
type ffmpegProcess struct {
	cmd      *exec.Cmd
	stdout   io.ReadCloser
	upstream *exec.Cmd
}

// Stdout returns the Ogg-Opus byte stream produced by FFmpeg.
func (p *ffmpegProcess) Stdout() io.ReadCloser { return p.stdout }

// Kill terminates the FFmpeg process and any upstream feeder (yt-dlp), then
// closes stdout. Safe to call multiple times; subsequent calls are no-ops.
func (p *ffmpegProcess) Kill() {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	if p.upstream != nil && p.upstream.Process != nil {
		_ = p.upstream.Process.Kill()
	}
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
	// Reap to prevent zombies. Wait calls are safe even after Kill.
	if p.cmd != nil {
		_ = p.cmd.Wait()
	}
	if p.upstream != nil {
		_ = p.upstream.Wait()
	}
}

/*
spawnTranscode runs FFmpeg to decode an arbitrary audio source and re-encode
it as Opus inside an Ogg container, written to stdout.

	params:
	      input: file path or URL FFmpeg can ingest
	returns:
	      *ffmpegProcess: live process; caller must call Kill() to clean up
	      error:          if the binary cannot be found or stdout pipe fails
*/
func spawnTranscode(input string) (*ffmpegProcess, error) {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", input,
		"-vn",
		"-c:a", "libopus",
		"-ar", "48000",
		"-ac", "2",
		"-b:a", "96k",
		"-f", "ogg",
		"pipe:1",
	}
	return run("ffmpeg", args, nil)
}

/*
spawnPassthrough runs FFmpeg with stream-copy semantics for inputs that are
already Opus. The audio is repackaged into Ogg without re-encoding, so CPU
usage is negligible.

	params:
	      input: file path or URL containing an Opus stream
	returns:
	      *ffmpegProcess, error
*/
func spawnPassthrough(input string) (*ffmpegProcess, error) {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", input,
		"-vn",
		"-c:a", "copy",
		"-f", "ogg",
		"pipe:1",
	}
	return run("ffmpeg", args, nil)
}

/*
spawnFromStdin starts FFmpeg reading from the given stdin reader. Used when
chaining with yt-dlp or any process that produces an audio byte stream.

	params:
	      stdin: source byte stream to feed into FFmpeg's stdin
	returns:
	      *ffmpegProcess, error
*/
func spawnFromStdin(stdin io.Reader) (*ffmpegProcess, error) {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0",
		"-vn",
		"-c:a", "libopus",
		"-ar", "48000",
		"-ac", "2",
		"-b:a", "96k",
		"-f", "ogg",
		"pipe:1",
	}
	return run("ffmpeg", args, stdin)
}

/*
spawnRemuxFromStdin starts FFmpeg in stream-copy mode for inputs that are
already Opus (e.g. YouTube itag 251 in WebM). FFmpeg only repackages the
audio into Ogg without touching the codec, which is ~99% cheaper than
transcoding and preserves source quality.

	params:
	      stdin: source byte stream containing an Opus track
	returns:
	      *ffmpegProcess, error

Note: We still go through FFmpeg here because YouTube serves Opus inside a
WebM/Matroska container and Discord expects raw Opus pages. FFmpeg handles
the container conversion at minimal cost. Use spawnFromStdin only when the
upstream codec is unknown or non-Opus.
*/
func spawnRemuxFromStdin(stdin io.Reader) (*ffmpegProcess, error) {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0",
		"-vn",
		"-c:a", "copy",
		"-f", "ogg",
		"pipe:1",
	}
	return run("ffmpeg", args, stdin)
}

// run is the shared spawn helper. It wires up stdout (always) and stdin
// (when provided), starts the process, and returns the wrapper.
func run(bin string, args []string, stdin io.Reader) (*ffmpegProcess, error) {
	cmd := exec.Command(bin, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("sikasa: %s stdout pipe: %w", bin, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sikasa: spawn %s: %w (is it installed?)", bin, err)
	}
	return &ffmpegProcess{cmd: cmd, stdout: stdout}, nil
}

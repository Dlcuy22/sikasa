# Changelog

All notable changes to the `sikasa` package will be documented in this file.

## [Unreleased]

### Changed
- YouTube playback is now fully pure Go. `ytm-go` v1.2.1 resolves a direct media URL, the new WebM/Opus reader (`webm_opus.go`, `ebml.go`, `opus_packet.go`) frames Opus packets, and the provider pushes them straight to Discord. yt-dlp, ffmpeg, the JS runtime, and the native/purego remuxer are no longer used or required.
- The prefetch cache is built with the pure-Go Ogg Opus muxer (`ogg_opus_writer.go`) instead of a yt-dlp + ffmpeg remux.
- Local playback supports Opus only: `.opus` and `.ogg` are read directly by `ogg_opus.go`. Other local formats are rejected.

### Removed
- `RemuxMode` (and `Bot.WithRemuxMode` / `VoiceCtx.WithRemuxMode`), `Bot.WithJSRuntime`, the `ytdlp` config section, `js_runtime_name`, `js_runtime_path`, and `remux_mode`.
- `voice_ffmpeg.go`, `voice_ogg.go`, `native_remux.go`, `native_remux_stub.go`, `native_remux_go.go`, `install_deps.go`, and `dep_installer.go`.

### Added
- `Bot.AvailableContainers()` reports the Opus containers the pure-Go readers support.

## [1.1.1] - 2026-06-27

### Added
- Persistent saving of music playback state (queue, cursor, announce channel, and play/pause state) under the `sikasa-data/state` directory as JSON files.
- A background recovery worker thread running every 15 seconds to automatically rejoin voice channels and resume playback on bot startup.
- Tracking of `channelID` directly inside `VoiceCtx` to ensure reconnect attempts survive closed or cleared voice connection states.

### Fixed
- Fixed an issue where temporary voice gateway close events (e.g., close code 4006) stopped reconnection retries.
- Fixed a recovery issue where setting the announce channel during rejoining triggered an intermediate save that cleared the persisted queue on disk.
- Added `isReconnecting` atomic state flags to prevent duplicate overlapping reconnection runs.

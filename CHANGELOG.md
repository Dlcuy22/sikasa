# Changelog

All notable changes to the `sikasa` package will be documented in this file.

## [1.1.1] - 2026-06-27

### Added
- Persistent saving of music playback state (queue, cursor, announce channel, and play/pause state) under the `sikasa-data/state` directory as JSON files.
- A background recovery worker thread running every 15 seconds to automatically rejoin voice channels and resume playback on bot startup.
- Tracking of `channelID` directly inside `VoiceCtx` to ensure reconnect attempts survive closed or cleared voice connection states.

### Fixed
- Fixed an issue where temporary voice gateway close events (e.g., close code 4006) stopped reconnection retries.
- Fixed a recovery issue where setting the announce channel during rejoining triggered an intermediate save that cleared the persisted queue on disk.
- Added `isReconnecting` atomic state flags to prevent duplicate overlapping reconnection runs.

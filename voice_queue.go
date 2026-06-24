// Package sikasa: voice_queue.go
// Purpose: Per-guild playback queue. Owned by VoiceCtx so each guild session
// has its own track list and cursor; multi-guild concurrency falls out of the
// existing voices map[snowflake.ID]*VoiceCtx.
//
// Key Components:
//   - TrackKind:  enum distinguishing local files from streamed URLs
//   - Track:      a single queue entry (kind + source descriptor)
//   - queue:      slice + cursor; supports advance, rewind, jump, clear
//
// Note: queue is not exported. Callers manipulate the queue through VoiceCtx
// methods (Enqueue, Next, Prev, Skip, Queue, ClearQueue), which serialize
// access through VoiceCtx.mu. The queue itself is therefore not internally
// locked.
package sikasa

import (
	"math/rand"
	"time"
)


// TrackKind tags how a Track should be played back.
type TrackKind int

const (
	// TrackFile is a local audio file path. Routed to PlayFile semantics.
	TrackFile TrackKind = iota
	// TrackYouTube is any URL yt-dlp can resolve. Routed to PlayYouTube.
	TrackYouTube
)

// Track is a single queue entry.
//
// Key Fields:
//   - Kind:   selects the spawn strategy when this track plays
//   - Source: file path (TrackFile) or URL (TrackYouTube); used as the fallback
//             label when Title is not populated
//   - Title:  optional human-readable title; populated by metadata probes for
//             TrackYouTube. Empty for TrackFile by default
//   - Author: optional uploader / channel / artist; populated alongside Title
type Track struct {
	Kind   TrackKind
	Source string
	Title  string
	Author string
}

// Label returns a human-friendly description of the track. If Title is set,
// formats as "Title by Author" (or just "Title" when Author is empty);
// otherwise falls back to Source. Use this for any user-facing reply so the
// bot does not spam raw URLs.
func (t Track) Label() string {
	if t.Title == "" {
		return t.Source
	}
	if t.Author == "" {
		return t.Title
	}
	return t.Title + " by " + t.Author
}

// queue holds the ordered track list and a cursor pointing at the currently
// loaded track. cursor == -1 means nothing has been started yet.
//
// Note: cursor follows the *currently playing* track, not the next one to
// play. Advance() increments cursor and returns the new track; Rewind()
// decrements. This makes Now() trivial (return tracks[cursor]) and lets
// Prev work intuitively even after a track finishes naturally.
type queue struct {
	tracks []Track
	cursor int
}

func newQueue() *queue {
	return &queue{cursor: -1}
}

// Add appends a track and returns its 0-based index in the queue.
func (q *queue) Add(t Track) int {
	q.tracks = append(q.tracks, t)
	return len(q.tracks) - 1
}

// Len returns the total number of tracks in the queue.
func (q *queue) Len() int { return len(q.tracks) }

// Cursor returns the index of the currently playing track, or -1 if none has
// started yet.
func (q *queue) Cursor() int { return q.cursor }

// Now returns the currently playing track and true, or a zero Track and false
// if nothing is loaded.
func (q *queue) Now() (Track, bool) {
	if q.cursor < 0 || q.cursor >= len(q.tracks) {
		return Track{}, false
	}
	return q.tracks[q.cursor], true
}

// Tracks returns a copy of the underlying slice. Safe to hand out to callers.
func (q *queue) Tracks() []Track {
	out := make([]Track, len(q.tracks))
	copy(out, q.tracks)
	return out
}

// Advance moves the cursor forward by one and returns the newly loaded track.
// Returns ok=false when the cursor is already past the last track, signalling
// the caller to stop playback.
func (q *queue) Advance() (Track, bool) {
	if q.cursor+1 >= len(q.tracks) {
		return Track{}, false
	}
	q.cursor++
	return q.tracks[q.cursor], true
}

// Rewind moves the cursor back by one and returns the newly loaded track.
// Returns ok=false when there is no previous track (cursor already at 0 or
// queue empty).
func (q *queue) Rewind() (Track, bool) {
	if q.cursor <= 0 {
		return Track{}, false
	}
	q.cursor--
	return q.tracks[q.cursor], true
}

// Jump sets the cursor to a specific index and returns the track at that
// position. Out-of-range indices return ok=false without mutating state.
func (q *queue) Jump(i int) (Track, bool) {
	if i < 0 || i >= len(q.tracks) {
		return Track{}, false
	}
	q.cursor = i
	return q.tracks[q.cursor], true
}

// Clear empties the queue and resets the cursor. Does not affect any
// currently-running provider; the caller is responsible for calling Stop()
// on the VoiceCtx if desired.
func (q *queue) Clear() {
	q.tracks = nil
	q.cursor = -1
}

// HasNext reports whether Advance would succeed.
func (q *queue) HasNext() bool { return q.cursor+1 < len(q.tracks) }

// HasPrev reports whether Rewind would succeed.
func (q *queue) HasPrev() bool { return q.cursor > 0 }

// InsertAfter inserts t at position after+1, shifting any subsequent tracks
// one slot down. Returns the new index of the inserted track. When after is
// out of range, the track is appended to the tail. The cursor itself is not
// touched, even if it sits at or past the insertion point; advance/rewind
// flow naturally to the new track on the next step.
//
//	params:
//	      after: 0-based index to insert *after* (use Cursor() for "next up")
//	      t:     track to insert
//	returns:
//	      int: 0-based index of the inserted track
func (q *queue) InsertAfter(after int, t Track) int {
	pos := after + 1
	if pos < 0 {
		pos = 0
	}
	if pos > len(q.tracks) {
		pos = len(q.tracks)
	}
	q.tracks = append(q.tracks, Track{})
	copy(q.tracks[pos+1:], q.tracks[pos:])
	q.tracks[pos] = t
	return pos
}

// InsertBatchAfter inserts a batch of tracks starting after `after`, in
// order. Returns the index of the first inserted track. When after is out
// of range, the batch is appended.
//
//	params:
//	      after:  0-based index to insert *after*
//	      batch:  tracks to insert in order
//	returns:
//	      int: index of the first newly-inserted track
func (q *queue) InsertBatchAfter(after int, batch []Track) int {
	if len(batch) == 0 {
		return after + 1
	}
	pos := after + 1
	if pos < 0 {
		pos = 0
	}
	if pos > len(q.tracks) {
		pos = len(q.tracks)
	}
	q.tracks = append(q.tracks, batch...)
	copy(q.tracks[pos+len(batch):], q.tracks[pos:len(q.tracks)-len(batch)])
	for i, t := range batch {
		q.tracks[pos+i] = t
	}
	return pos
}

/*
Shuffle randomizes the order of all tracks in the queue after the current cursor.
This leaves the currently playing track and historical tracks untouched.
*/
func (q *queue) Shuffle() {
	if q.cursor+2 >= len(q.tracks) {
		return
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	start := q.cursor + 1
	n := len(q.tracks) - start
	r.Shuffle(n, func(i, j int) {
		q.tracks[start+i], q.tracks[start+j] = q.tracks[start+j], q.tracks[start+i]
	})
}


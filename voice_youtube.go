// Package sikasa: voice_youtube.go
// Purpose: Resolves YouTube playback in pure Go. ytm-go resolves a track id to
// a direct googlevideo media URL (no yt-dlp, no JS runtime), and the WebM/Opus
// reader in webm_opus.go frames the Opus packets out of the fetched body. The
// result is a stream of Opus packets ready for Discord, with no external
// process anywhere in the path.
//
// Key Components:
//   - openYouTubeSource():  resolve, ranged-fetch, and frame one track
//   - resolveYouTubeStream(): GetStream against the VISIONOS client
//   - SearchYouTube():      catalogue search resolving via the InnerTube client
//   - probeYouTubeEntries(): expands a URL or search query into queue tracks
//   - IsHTTPURL():          heuristic to check whether a query is a URL
//
// Dependencies:
//   - context, errors, fmt, io, net/http, net/url, strings, sync, time
//   - github.com/dlcuy22/ytm-go: YouTube Music metadata and direct media URLs
package sikasa

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	ytm "github.com/dlcuy22/ytm-go"
)

// ytmMaxTrackBytes bounds one track body held in memory. A long set at the top
// music bitrate is well under this; the bound exists so a wrong Content-Length
// or an endless body cannot exhaust memory.
const ytmMaxTrackBytes = 256 << 20

// ytmSearchTimeout bounds one catalogue search or metadata resolve.
const ytmSearchTimeout = 20 * time.Second

// ytmMediaClient streams the media body. It deliberately has no total timeout:
// the body is read over the whole duration of a track, so a client deadline
// would cut a long track off mid-playback. Cancellation comes from the request
// context and from closing the body, and the transport still fails fast on a
// dead connection via the dial and response-header timeouts.
var ytmMediaClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

var (
	ytmClientOnce sync.Once
	ytmClient     *ytm.Client
)

// sharedYTMClient returns the process-wide ytm-go client. It is created lazily
// so a build that never touches YouTube pays nothing, and shared because the
// client carries its own response cache.
func sharedYTMClient() *ytm.Client {
	ytmClientOnce.Do(func() {
		ytmClient = ytm.NewClient()
	})

	return ytmClient
}

// ytmUserAgent is the user agent the signed media URL is bound to. It must
// match the VISIONOS client context used for the GetStream call.
func ytmUserAgent() string {
	return ytm.GetContextVisionOS("en").UserAgent
}

/*
openYouTubeSource resolves a YouTube track URL or id to a direct audio URL,
fetches the WebM body with a ranged GET, and returns an Opus packet source over
it. The body is read whole because the WebM reader walks the stream forward and
Discord playback never seeks.

	params:
	      ctx:    lifecycle context; cancels the resolve and the fetch
	      urlOrID: a YouTube watch URL, a youtu.be short link, or a bare id
	returns:
	      opusSource: Opus packets; Close() releases the body
	      error:      resolve, network, or container error
*/
func openYouTubeSource(ctx context.Context, urlOrID string) (opusSource, error) {
	id := extractVideoID(urlOrID)
	if id == "" {
		return nil, fmt.Errorf("sikasa: %q is not a YouTube track reference", urlOrID)
	}

	// Bound the metadata resolve so a hung lookup cannot block the caller. The
	// media fetch below keeps the caller's context because its body streams for
	// the whole track and must not be cut off by this deadline.
	resolveCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		resolveCtx, cancel = context.WithTimeout(ctx, ytmSearchTimeout)
		defer cancel()
	}

	streams, err := resolveYouTubeStream(resolveCtx, id)
	if err != nil {
		return nil, err
	}
	best, ok := streams.Best()
	if !ok {
		return nil, fmt.Errorf("sikasa: no audio format for %s", id)
	}

	body, err := fetchRangedBody(ctx, best.URL, ytmMaxTrackBytes)
	if err != nil {
		return nil, err
	}

	return newWebMOpusSource(body)
}

/*
resolveYouTubeStream resolves the playable formats for a song id, preferring
Opus.

	params:
	      ctx:    execution context
	      songID: YouTube track id (an "MPED" prefix is tolerated)
	returns:
	      *ytm.Streams: resolved formats, best first
	      error:        network or availability error
*/
func resolveYouTubeStream(ctx context.Context, songID string) (*ytm.Streams, error) {
	return sharedYTMClient().GetStreamWithOptions(ctx, songID, ytm.StreamOptions{Codec: ytm.CodecOpus})
}

// fetchRangedBody issues the ranged GET every media request needs. googlevideo
// throttles a GET without a Range header to roughly 32 KiB/s, so the header is
// not an optimisation. The body is bounded by limit.
func fetchRangedBody(ctx context.Context, rawURL string, limit int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes=0-")
	req.Header.Set("User-Agent", ytmUserAgent())
	req.Header.Set("Accept", "*/*")

	resp, err := ytmMediaClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()

		return nil, fmt.Errorf("sikasa: media request returned %s", resp.Status)
	}

	return &limitedReadCloser{Reader: io.LimitReader(resp.Body, limit), closer: resp.Body}, nil
}

// limitedReadCloser bounds a response body while keeping the real body closeable.
type limitedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (l *limitedReadCloser) Close() error { return l.closer.Close() }

/*
extractVideoID pulls the video id out of a YouTube URL or accepts a bare id.

	params:
	      raw: watch URL, youtu.be link, shorts/embed URL, or a bare id
	returns:
	      string: the video id, or "" when none is found
*/
func extractVideoID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if !IsHTTPURL(raw) {
		// A bare id, possibly with the catalogue's MPED prefix.
		id := ytm.CleanSongID(raw)
		if isVideoID(id) {
			return id
		}

		return ""
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.HasSuffix(host, "youtu.be"):
		id := strings.Trim(u.Path, "/")
		if isVideoID(id) {
			return id
		}
	case strings.HasSuffix(host, "youtube.com"), host == "youtube-nocookie.com":
		if v := u.Query().Get("v"); isVideoID(v) {
			return v
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live") && isVideoID(parts[1]) {
			return parts[1]
		}
	}

	return ""
}

// isVideoID reports whether s looks like a YouTube video id: 11 characters from
// the URL-safe alphabet.
func isVideoID(s string) bool {
	if len(s) != 11 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}

	return true
}

/*
SearchYouTube queries YouTube Music using the Go-native ytm-go library and
returns the top-N results as Tracks.

	params:
	      query: free-text search string
	      n:     number of results to return
	returns:
	      []Track: candidate tracks in relevance order
	      error:   InnerTube query error
*/
func SearchYouTube(query string, n int) ([]Track, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ytmSearchTimeout)
	defer cancel()

	results, err := sharedYTMClient().Search(ctx, q, "", false)
	if err != nil {
		return nil, fmt.Errorf("ytm-go search: %w", err)
	}

	var tracks []Track
	for _, cat := range results.Categories {
		for _, item := range cat.Layout.Items {
			if len(tracks) >= n {
				break
			}
			s, ok := item.(*ytm.Song)
			if !ok || s == nil {
				continue
			}
			var artists []string
			for _, a := range s.Artists {
				if name := strings.TrimSpace(a.Name); name != "" {
					artists = append(artists, name)
				}
			}
			id := ytm.CleanSongID(s.ID)
			tracks = append(tracks, Track{
				Kind:   TrackYouTube,
				Source: "https://www.youtube.com/watch?v=" + id,
				Title:  s.Name,
				Author: strings.Join(artists, ", "),
			})
		}
	}

	return tracks, nil
}

/*
probeYouTubeEntries expands a URL or search query into a list of Tracks.

	params:
	      query: a YouTube URL, a bare video id, or a free-text search string
	returns:
	      []Track: one or more candidate tracks
	      error:   resolve or search error
*/
func probeYouTubeEntries(query string) ([]Track, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	if IsHTTPURL(query) || isVideoID(ytm.CleanSongID(query)) {
		id := extractVideoID(query)
		if id == "" {
			return nil, fmt.Errorf("sikasa: %q is not a YouTube track reference", query)
		}
		ctx, cancel := context.WithTimeout(context.Background(), ytmSearchTimeout)
		defer cancel()

		streams, err := resolveYouTubeStream(ctx, id)
		if err != nil {
			return nil, err
		}

		return []Track{{
			Kind:   TrackYouTube,
			Source: "https://www.youtube.com/watch?v=" + id,
			Title:  streams.Title,
			Author: streams.Author,
		}}, nil
	}

	return SearchYouTube(query, 1)
}

/*
IsHTTPURL is a cheap heuristic that decides whether a user-supplied string
should be treated as a URL or a search query.

	params:
	      s: the raw user input
	returns:
	      bool: true when s looks like an HTTP(S) URL
*/
func IsHTTPURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

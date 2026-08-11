// Package catalog proxies Deezer (metadata: search, track, album, artist)
// and lrclib.net (lyrics). Deezer never gives out raw audio — that's the
// yt-dlp pipeline in internal/stream, Phase 8.
package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

const deezerAPIBase = "https://api.deezer.com"

// DeezerClient fetches from Deezer and forwards its JSON verbatim.
//
// Deliberately not decoded into narrow Go structs: the old server's route
// handlers type their Deezer responses narrowly (DeezerTrack, DeezerAlbumDetail...)
// for their own internal use, but then call NextResponse.json(track) on the
// *original* fetched object — and JS doesn't strip unlisted properties at
// runtime, so every field Deezer actually sent reaches the client, not just
// the ones the TS type happens to name. The Android DTOs read some of those
// "extra" fields (e.g. an artist's picture_big, absent from the old
// server's own DeezerArtistDetail type but present in Deezer's real
// response and in the Android DTO). Decoding into a fixed Go struct and
// re-marshaling would silently drop them; json.RawMessage passthrough
// reproduces the old server's actual wire behavior instead of its
// internal type's.
type DeezerClient struct {
	http    *http.Client
	baseURL string
}

func NewDeezerClient() *DeezerClient {
	// The old server sets no explicit timeout on these calls at all. A
	// bound is still worth having — nothing depends on "hangs forever" —
	// but it's a Go-side engineering default, not a documented contract.
	return &DeezerClient{http: &http.Client{Timeout: 15 * time.Second}, baseURL: deezerAPIBase}
}

type errorProbe struct {
	Error json.RawMessage `json:"error"`
}

// fetch calls a Deezer endpoint and returns its raw JSON body. ok is false
// on a transport failure, a non-2xx status, or a Deezer-shaped
// {"error": ...} payload (Deezer's own way of saying "bad id" or similar)
// — collapsing exactly the same three failure modes the old server's
// fetchDeezerUrl collapses into a single null.
func (c *DeezerClient) fetch(ctx context.Context, path string) (json.RawMessage, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}

	var probe errorProbe
	if err := json.Unmarshal(body, &probe); err == nil {
		if len(probe.Error) > 0 && string(probe.Error) != "null" {
			return nil, false
		}
	}
	return json.RawMessage(body), true
}

// fetchDataArray calls a Deezer endpoint expected to answer {"data": [...]}
// (search results, an artist's top tracks) and returns just that array,
// defaulting to an empty one if the field is absent — matching
// `payload.data ?? []` in the old server.
func (c *DeezerClient) fetchDataArray(ctx context.Context, path string) (json.RawMessage, bool) {
	body, ok := c.fetch(ctx, path)
	if !ok {
		return nil, false
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false
	}
	if len(envelope.Data) == 0 {
		return json.RawMessage("[]"), true
	}
	return envelope.Data, true
}

// FetchTrack, FetchAlbum: direct passthrough of a single Deezer object.
func (c *DeezerClient) FetchTrack(ctx context.Context, id string) (json.RawMessage, bool) {
	return c.fetch(ctx, "/track/"+id)
}

func (c *DeezerClient) FetchAlbum(ctx context.Context, id string) (json.RawMessage, bool) {
	return c.fetch(ctx, "/album/"+id)
}

// artistTopTracksLimit matches the old server's catalog.ts exactly — kept
// equal so /artist/{id} (API) and the web's own /artist/{id} page (if it
// ever comes back) can never drift on how many top tracks they show.
const artistTopTracksLimit = 25

// FetchArtistPage returns the artist object and their top tracks. Only the
// artist fetch failing is a 404 for the caller — a failed top-tracks call
// degrades to an empty array rather than failing the whole page, matching
// `topTracks?.data ?? []` in the old server.
func (c *DeezerClient) FetchArtistPage(ctx context.Context, id string) (artist json.RawMessage, topTracks json.RawMessage, ok bool) {
	artist, ok = c.fetch(ctx, "/artist/"+id)
	if !ok {
		return nil, nil, false
	}
	topTracks, tracksOK := c.fetchDataArray(ctx, "/artist/"+id+"/top?limit="+strconv.Itoa(artistTopTracksLimit))
	if !tracksOK {
		topTracks = json.RawMessage("[]")
	}
	return artist, topTracks, true
}

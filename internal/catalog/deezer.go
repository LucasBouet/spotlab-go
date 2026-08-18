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

// NewDeezerClientForTesting points a client at an arbitrary base URL (an
// httptest server) — for tests in *other* packages (internal/stats) that
// need to stub Deezer over HTTP without exposing DeezerClient's fields.
func NewDeezerClientForTesting(httpClient *http.Client, baseURL string) *DeezerClient {
	return &DeezerClient{http: httpClient, baseURL: baseURL}
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
	return c.FetchURL(ctx, c.baseURL+path)
}

// FetchURL is fetch, but for an already-absolute URL — needed to follow a
// paginated response's own `next` cursor (playlist track pages), which
// Deezer hands back as a full URL rather than a path to re-prefix.
func (c *DeezerClient) FetchURL(ctx context.Context, url string) (json.RawMessage, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

// FetchArtistRelated, FetchArtistTopTracks, FetchArtistAlbums and
// FetchChart back internal/stats' recommendation build (Phase 7) — unlike
// the rest of this file they're consumed by narrow typed decodes there,
// not passthrough, matching recommendations.ts's own RecTrack/RecAlbum
// normalization rather than the wide-open shape the rest of the catalog
// forwards to Android.
func (c *DeezerClient) FetchArtistRelated(ctx context.Context, artistID string, limit int) (json.RawMessage, bool) {
	return c.fetchDataArray(ctx, "/artist/"+artistID+"/related?limit="+strconv.Itoa(limit))
}

func (c *DeezerClient) FetchArtistTopTracks(ctx context.Context, artistID string, limit int) (json.RawMessage, bool) {
	return c.fetchDataArray(ctx, "/artist/"+artistID+"/top?limit="+strconv.Itoa(limit))
}

func (c *DeezerClient) FetchArtistAlbums(ctx context.Context, artistID string, limit int) (json.RawMessage, bool) {
	return c.fetchDataArray(ctx, "/artist/"+artistID+"/albums?limit="+strconv.Itoa(limit))
}

// FetchArtistRadio is Deezer's own "radio" mix seeded from one artist — a
// ready-made blend of that artist and similar-sounding ones, not just their
// own catalogue (that's FetchArtistTopTracks). Backs the "<Artist> Radio"
// smart playlist (internal/stats/smartplaylists.go); same {"data": [...]}
// envelope as the other artist list endpoints above.
func (c *DeezerClient) FetchArtistRadio(ctx context.Context, artistID string, limit int) (json.RawMessage, bool) {
	return c.fetchDataArray(ctx, "/artist/"+artistID+"/radio?limit="+strconv.Itoa(limit))
}

func (c *DeezerClient) FetchChart(ctx context.Context, limit int) (json.RawMessage, bool) {
	return c.fetch(ctx, "/chart?limit="+strconv.Itoa(limit))
}

// FetchPlaylist and FetchPlaylistTracksPage back internal/playlists' Deezer
// playlist import — narrow typed decodes there, like the recommendations
// build above, not passthrough. The `tracks` field embedded on
// /playlist/{id} is capped at 400 items by Deezer with no `next` cursor
// once truncated; FetchPlaylistTracksPage (the dedicated /tracks
// collection endpoint) paginates through the full playlist instead.
func (c *DeezerClient) FetchPlaylist(ctx context.Context, id string) (json.RawMessage, bool) {
	return c.fetch(ctx, "/playlist/"+id)
}

func (c *DeezerClient) FetchPlaylistTracksPage(ctx context.Context, id string, limit int) (json.RawMessage, bool) {
	return c.fetch(ctx, "/playlist/"+id+"/tracks?limit="+strconv.Itoa(limit)+"&index=0")
}

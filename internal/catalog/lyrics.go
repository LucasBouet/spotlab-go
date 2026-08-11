package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	lrclibBase      = "https://lrclib.net/api"
	lrclibUserAgent = "Spotlab/1.0 (self-hosted music player)"
	lrclibTimeout   = 15 * time.Second
)

// LyricsClient proxies lrclib.net, a free keyless lyrics database. Like
// DeezerClient, it forwards the winning result's JSON verbatim rather than
// decoding into a narrow struct — the old server does the same (passes the
// parsed-then-reserialized JS object through), and lrclib's payload has
// fields beyond the three the Android LyricsDto reads.
type LyricsClient struct {
	http    *http.Client
	baseURL string

	mu sync.Mutex
	// Keyed by track::artist::album::duration. A key present with a nil
	// value means "confirmed not found" — a real, cacheable answer,
	// distinct from the key being absent (never looked up, or the lookup
	// hit a network/timeout error and must be retried).
	cache map[string]json.RawMessage
}

func NewLyricsClient() *LyricsClient {
	return &LyricsClient{
		http:    &http.Client{Timeout: lrclibTimeout},
		baseURL: lrclibBase,
		cache:   make(map[string]json.RawMessage),
	}
}

// Lookup returns the winning lyrics object's raw JSON, or found=false if
// nothing was found. See docs/PLAN.md and the old server's lyrics/route.ts
// for why the caching rule below is the subtle part of this function.
func (c *LyricsClient) Lookup(ctx context.Context, track, artist, album, duration string) (json.RawMessage, bool) {
	cacheKey := track + "::" + artist + "::" + album + "::" + duration

	c.mu.Lock()
	cached, ok := c.cache[cacheKey]
	c.mu.Unlock()
	if ok {
		return cached, cached != nil
	}

	exactQuery := url.Values{"track_name": {track}, "artist_name": {artist}}
	if album != "" {
		exactQuery.Set("album_name", album)
	}
	if duration != "" {
		exactQuery.Set("duration", duration)
	}
	searchQuery := url.Values{"track_name": {track}, "artist_name": {artist}}

	// Fired together rather than exact-then-fallback: lrclib's exact
	// endpoint is picky about duration and misses often, and running the
	// two sequentially doubled the wait on that (common) path.
	var wg sync.WaitGroup
	var exactBody json.RawMessage
	var exactDefinite bool
	var searchResults []json.RawMessage
	var searchDefinite bool

	wg.Add(2)
	go func() {
		defer wg.Done()
		exactBody, exactDefinite = c.fetchLrclib(ctx, "/get?"+exactQuery.Encode())
	}()
	go func() {
		defer wg.Done()
		var raw json.RawMessage
		raw, searchDefinite = c.fetchLrclib(ctx, "/search?"+searchQuery.Encode())
		if searchDefinite && raw != nil {
			_ = json.Unmarshal(raw, &searchResults)
		}
	}()
	wg.Wait()

	var result json.RawMessage
	if exactBody != nil {
		result = exactBody
	} else if searchDefinite && len(searchResults) > 0 {
		result = searchResults[0]
	}

	// A genuine "nothing found" (both lookups came back definite — 404 or
	// a real empty answer) is worth caching. A network/timeout failure on
	// either one is not: it should be retried on the next request, never
	// remembered as "no lyrics" for the rest of the process's life.
	if exactDefinite && searchDefinite {
		c.mu.Lock()
		c.cache[cacheKey] = result
		c.mu.Unlock()
	}

	return result, result != nil
}

// fetchLrclib returns (body, definite). definite=false means a transport
// error or timeout — the old server's fetchLrclib returning `undefined` —
// which must never be cached. definite=true with a nil body means a real
// 404 ("confirmed not found"); definite=true with a body means success.
func (c *LyricsClient) fetchLrclib(ctx context.Context, path string) (json.RawMessage, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", lrclibUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, true
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(body), true
}

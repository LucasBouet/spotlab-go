package stats

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

const lastFMAPIBase = "https://ws.audioscrobbler.com/2.0/"

// lastFMClient resolves a granular genre from Last.fm's community tags,
// ported from fetchLastFmGenre in track-genre.ts. It's only a fallback
// source when LASTFM_API_KEY is configured — Deezer's own coarse per-album
// genre (deezerGenre in track_genre.go) is what's used otherwise.
type lastFMClient struct {
	http    *http.Client
	apiKey  string
	baseURL string
}

func newLastFMClient(apiKey string) *lastFMClient {
	return &lastFMClient{http: &http.Client{Timeout: 15 * time.Second}, apiKey: apiKey, baseURL: lastFMAPIBase}
}

func (c *lastFMClient) hasKey() bool { return c.apiKey != "" }

type lastFMTopTagsResponse struct {
	TopTags struct {
		Tag json.RawMessage `json:"tag"`
	} `json:"toptags"`
}

type lastFMTag struct {
	Name string `json:"name"`
}

// fetchGenre mirrors fetchLastFmGenre: "" when no key/artist is given, the
// request fails, or no tag is a recognized genre.
func (c *lastFMClient) fetchGenre(ctx context.Context, artistName string) string {
	if !c.hasKey() || artistName == "" {
		return ""
	}

	target := c.baseURL + "?method=artist.gettoptags&artist=" + url.QueryEscape(artistName) +
		"&autocorrect=1&api_key=" + url.QueryEscape(c.apiKey) + "&format=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ""
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	var payload lastFMTopTagsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	// Last.fm answers `tag` as a single object when there's exactly one tag
	// and an array otherwise — the same normalization track-genre.ts does
	// (`Array.isArray(raw) ? raw : raw ? [raw] : []`).
	var tags []lastFMTag
	if len(payload.TopTags.Tag) > 0 && string(payload.TopTags.Tag) != "null" {
		if err := json.Unmarshal(payload.TopTags.Tag, &tags); err != nil {
			var single lastFMTag
			if err := json.Unmarshal(payload.TopTags.Tag, &single); err == nil {
				tags = []lastFMTag{single}
			}
		}
	}

	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	return pickGenre(names)
}

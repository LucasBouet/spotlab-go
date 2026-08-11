package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lucasbouet/spotlab-go/internal/catalog"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

// Constants below are a direct port of recommendations.ts's tuning knobs —
// same names, same values, so behavior doesn't silently drift from the
// values that were tuned against real Deezer data.
const (
	// cacheTTL: a build is dozens of Deezer round-trips, so we don't
	// recompute on every homepage visit. The "refresh" query param bypasses
	// this.
	cacheTTL = 6 * time.Hour

	maxSeedArtists = 5 // how many of the user's most-played artists seed the build
	minSeedArtists = 2 // below this many distinct seeds in the *requested* window, widen

	relatedPerSeed     = 6 // fetched per seed, then trimmed round-robin
	maxRelatedArtists  = 8 // discovery artists kept across all seeds
	topTracksPerArtist = 6
	albumsPerArtist    = 6
	maxTracks          = 24
	maxAlbums          = 18
	chartLimit         = 40

	// deezerConcurrency caps in-flight Deezer requests during a build —
	// Deezer rate-limits (~50 req/5s) and a build makes two calls per artist.
	deezerConcurrency = 4
)

type recWindow string

const (
	windowDay  recWindow = "day"
	windowWeek recWindow = "week"
	windowAll  recWindow = "all"
)

func parseWindow(v string) (recWindow, bool) {
	switch recWindow(v) {
	case windowDay, windowWeek, windowAll:
		return recWindow(v), true
	}
	return "", false
}

// windowStart mirrors windowStart in recommendations.ts; nil means no lower
// bound (the "all" window).
func windowStart(w recWindow) *time.Time {
	now := time.Now()
	switch w {
	case windowDay:
		t := now.Add(-24 * time.Hour)
		return &t
	case windowWeek:
		t := now.Add(-7 * 24 * time.Hour)
		return &t
	default:
		return nil
	}
}

func albumKey(title, artistName string) string {
	return strings.ToLower(title + " " + artistName)
}

// recPayload is the cached/built shape — same as RecommendationsDTO minus
// the requested `window`, which is added back by getRecommendations. This
// mirrors RecPayload/Recommendations in recommendations.ts, where
// `Recommendations = RecPayload & { window }`.
type recPayload struct {
	EffectiveWindow string        `json:"effectiveWindow"`
	BasedOn         []string      `json:"basedOn"`
	Tracks          []RecTrackDTO `json:"tracks"`
	Albums          []RecAlbumDTO `json:"albums"`
}

// mapPool runs fn over items with at most limit goroutines in flight at
// once — the Go equivalent of mapPool in recommendations.ts, keeping Deezer
// calls bounded during a build.
func mapPool[T any, R any](items []T, limit int, fn func(T) R) []R {
	results := make([]R, len(items))
	if len(items) == 0 {
		return results
	}
	workerCount := limit
	if workerCount > len(items) {
		workerCount = len(items)
	}
	var cursor int64 = -1
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			for {
				i := atomic.AddInt64(&cursor, 1)
				if int(i) >= len(items) {
					return
				}
				results[i] = fn(items[i])
			}
		}()
	}
	wg.Wait()
	return results
}

// interleave round-robins across groups so the result is spread over many
// artists rather than front-loaded with one artist's whole catalogue.
func interleave[T any](groups [][]T, max int) []T {
	out := make([]T, 0, max)
	for round := 0; len(out) < max; round++ {
		advanced := false
		for _, group := range groups {
			if round < len(group) {
				out = append(out, group[round])
				advanced = true
				if len(out) >= max {
					break
				}
			}
		}
		if !advanced {
			break
		}
	}
	return out
}

type artistRef struct {
	ID   int64
	Name string
}

type playedSets struct {
	trackIDs map[int64]bool
	albums   map[string]bool
}

// getPlayedSets mirrors getPlayedSets in recommendations.ts: everything the
// user has *ever* played, so recommendations never surface a track or
// album they've already heard, regardless of the current window.
func getPlayedSets(ctx context.Context, queries *db.Queries, userID string) (playedSets, error) {
	rows, err := queries.ListPlayedTracksAndAlbums(ctx, userID)
	if err != nil {
		return playedSets{}, err
	}
	played := playedSets{trackIDs: make(map[int64]bool, len(rows)), albums: make(map[string]bool, len(rows))}
	for _, r := range rows {
		played.trackIDs[r.DeezerTrackID] = true
		played.albums[albumKey(r.AlbumTitle, r.ArtistName)] = true
	}
	return played, nil
}

type seedArtist struct {
	ArtistName    string
	SampleTrackID int64
}

// getSeedArtists mirrors getSeedArtists in recommendations.ts.
func getSeedArtists(ctx context.Context, queries *db.Queries, userID string, since *time.Time) ([]seedArtist, error) {
	var sinceParam sql.NullTime
	if since != nil {
		sinceParam = sql.NullTime{Time: *since, Valid: true}
	}
	rows, err := queries.SeedArtists(ctx, userID, sinceParam, maxSeedArtists)
	if err != nil {
		return nil, err
	}
	seeds := make([]seedArtist, 0, len(rows))
	for _, r := range rows {
		if r.ArtistName == "" || r.SampleTrackID == 0 {
			continue
		}
		seeds = append(seeds, seedArtist{ArtistName: r.ArtistName, SampleTrackID: r.SampleTrackID})
	}
	return seeds, nil
}

// --- Deezer response decoding (narrow, unlike catalog's passthrough — see
// deezer.go's comment on why these calls are typed here) ---

type deezerArtistRefJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type deezerTrackJSON struct {
	ID       int64               `json:"id"`
	Title    string              `json:"title"`
	Duration int64               `json:"duration"`
	Artist   deezerArtistRefJSON `json:"artist"`
	Album    deezerAlbumRefJSON  `json:"album"`
}

type deezerAlbumRefJSON struct {
	Title       string `json:"title"`
	CoverMedium string `json:"cover_medium"`
}

func (t deezerTrackJSON) toDTO() RecTrackDTO {
	dto := RecTrackDTO{
		ID:       t.ID,
		Title:    t.Title,
		Duration: t.Duration,
		Album:    RecAlbumRefDTO{Title: t.Album.Title, CoverMedium: t.Album.CoverMedium},
	}
	dto.Artist.Name = t.Artist.Name
	if t.Artist.ID != 0 {
		id := t.Artist.ID
		dto.Artist.ID = &id
	}
	return dto
}

type deezerAlbumJSON struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	CoverMedium string `json:"cover_medium"`
}

func resolveArtist(ctx context.Context, deezer *catalog.DeezerClient, sampleTrackID int64) *artistRef {
	raw, ok := deezer.FetchTrack(ctx, strconv.FormatInt(sampleTrackID, 10))
	if !ok {
		return nil
	}
	var t struct {
		Artist deezerArtistRefJSON `json:"artist"`
	}
	if err := json.Unmarshal(raw, &t); err != nil || t.Artist.ID == 0 {
		return nil
	}
	return &artistRef{ID: t.Artist.ID, Name: t.Artist.Name}
}

func fetchRelated(ctx context.Context, deezer *catalog.DeezerClient, artistID int64) []artistRef {
	raw, ok := deezer.FetchArtistRelated(ctx, strconv.FormatInt(artistID, 10), relatedPerSeed)
	if !ok {
		return nil
	}
	var list []deezerArtistRefJSON
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	out := make([]artistRef, len(list))
	for i, a := range list {
		out[i] = artistRef{ID: a.ID, Name: a.Name}
	}
	return out
}

func fetchArtistCatalogue(ctx context.Context, deezer *catalog.DeezerClient, artist artistRef) (tracks []RecTrackDTO, albums []RecAlbumDTO) {
	topRaw, topOK := deezer.FetchArtistTopTracks(ctx, strconv.FormatInt(artist.ID, 10), topTracksPerArtist)
	if topOK {
		var list []deezerTrackJSON
		if json.Unmarshal(topRaw, &list) == nil {
			for _, t := range list {
				tracks = append(tracks, t.toDTO())
			}
		}
	}

	albumsRaw, albumsOK := deezer.FetchArtistAlbums(ctx, strconv.FormatInt(artist.ID, 10), albumsPerArtist)
	if albumsOK {
		var list []deezerAlbumJSON
		if json.Unmarshal(albumsRaw, &list) == nil {
			artistID := artist.ID
			for _, a := range list {
				albums = append(albums, RecAlbumDTO{
					ID: a.ID, Title: a.Title, CoverMedium: a.CoverMedium,
					Artist: RecArtistDTO{ID: &artistID, Name: artist.Name},
				})
			}
		}
	}
	return tracks, albums
}

// pickRelated picks the discovery artists: spread round-robin across each
// seed's related list so no single seed dominates, excluding the seeds
// themselves and any duplicates. Mirrors pickRelated in recommendations.ts.
func pickRelated(seedIDs map[int64]bool, lists [][]artistRef) []artistRef {
	seen := make(map[int64]bool, len(seedIDs))
	for id := range seedIDs {
		seen[id] = true
	}
	perSeed := make([][]artistRef, len(lists))
	for i, list := range lists {
		var kept []artistRef
		for _, a := range list {
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			kept = append(kept, a)
		}
		perSeed[i] = kept
	}

	var picked []artistRef
	for round := 0; len(picked) < maxRelatedArtists; round++ {
		advanced := false
		for _, list := range perSeed {
			if round < len(list) {
				picked = append(picked, list[round])
				advanced = true
				if len(picked) >= maxRelatedArtists {
					break
				}
			}
		}
		if !advanced {
			break
		}
	}
	return picked
}

// buildPersonal mirrors buildPersonal in recommendations.ts: discovery
// from related artists PLUS not-yet-heard material from the user's own top
// artists. Returns nil when no seed could be resolved to a Deezer artist.
func buildPersonal(ctx context.Context, deezer *catalog.DeezerClient, seeds []seedArtist, played playedSets) *recPayload {
	resolved := mapPool(seeds, deezerConcurrency, func(seed seedArtist) *artistRef {
		return resolveArtist(ctx, deezer, seed.SampleTrackID)
	})

	var seedArtists []artistRef
	seedIDs := make(map[int64]bool)
	for _, artist := range resolved {
		if artist != nil && !seedIDs[artist.ID] {
			seedIDs[artist.ID] = true
			seedArtists = append(seedArtists, *artist)
		}
	}
	if len(seedArtists) == 0 {
		return nil
	}

	relatedLists := mapPool(seedArtists, deezerConcurrency, func(artist artistRef) []artistRef {
		return fetchRelated(ctx, deezer, artist.ID)
	})
	related := pickRelated(seedIDs, relatedLists)

	// Seed artists first (loved → their unheard catalogue), then discovery.
	sourceArtists := append(append([]artistRef{}, seedArtists...), related...)
	type catalogue struct {
		tracks []RecTrackDTO
		albums []RecAlbumDTO
	}
	catalogues := mapPool(sourceArtists, deezerConcurrency, func(artist artistRef) catalogue {
		tracks, albums := fetchArtistCatalogue(ctx, deezer, artist)
		return catalogue{tracks: tracks, albums: albums}
	})

	trackSeen := make(map[int64]bool)
	trackGroups := make([][]RecTrackDTO, len(catalogues))
	for i, cat := range catalogues {
		var kept []RecTrackDTO
		for _, track := range cat.tracks {
			if track.ID == 0 || played.trackIDs[track.ID] || trackSeen[track.ID] {
				continue
			}
			trackSeen[track.ID] = true
			kept = append(kept, track)
		}
		trackGroups[i] = kept
	}

	albumSeen := make(map[int64]bool)
	albumGroups := make([][]RecAlbumDTO, len(catalogues))
	for i, cat := range catalogues {
		var kept []RecAlbumDTO
		for _, album := range cat.albums {
			if album.ID == 0 || albumSeen[album.ID] || played.albums[albumKey(album.Title, album.Artist.Name)] {
				continue
			}
			albumSeen[album.ID] = true
			kept = append(kept, album)
		}
		albumGroups[i] = kept
	}

	basedOn := make([]string, 0, len(seedArtists))
	for _, a := range seedArtists {
		if a.Name != "" {
			basedOn = append(basedOn, a.Name)
		}
	}

	return &recPayload{
		EffectiveWindow: "", // filled in by the caller, which knows the candidate window
		BasedOn:         basedOn,
		Tracks:          interleave(trackGroups, maxTracks),
		Albums:          interleave(albumGroups, maxAlbums),
	}
}

// buildFromCharts is the cold-start / last-resort path: Deezer global
// charts, minus anything already heard. Mirrors buildFromCharts in
// recommendations.ts.
func buildFromCharts(ctx context.Context, deezer *catalog.DeezerClient, played playedSets) recPayload {
	raw, ok := deezer.FetchChart(ctx, chartLimit)
	payload := recPayload{EffectiveWindow: "charts", BasedOn: []string{}, Tracks: []RecTrackDTO{}, Albums: []RecAlbumDTO{}}
	if !ok {
		return payload
	}

	var chart struct {
		Tracks struct {
			Data []deezerTrackJSON `json:"data"`
		} `json:"tracks"`
		Albums struct {
			Data []struct {
				ID     int64               `json:"id"`
				Title  string              `json:"title"`
				Cover  string              `json:"cover_medium"`
				Artist deezerArtistRefJSON `json:"artist"`
			} `json:"data"`
		} `json:"albums"`
	}
	if err := json.Unmarshal(raw, &chart); err != nil {
		return payload
	}

	for _, t := range chart.Tracks.Data {
		if t.ID == 0 || played.trackIDs[t.ID] {
			continue
		}
		payload.Tracks = append(payload.Tracks, t.toDTO())
		if len(payload.Tracks) >= maxTracks {
			break
		}
	}
	for _, a := range chart.Albums.Data {
		if a.ID == 0 || played.albums[albumKey(a.Title, a.Artist.Name)] {
			continue
		}
		artistID := a.Artist.ID
		dto := RecAlbumDTO{ID: a.ID, Title: a.Title, CoverMedium: a.Cover, Artist: RecArtistDTO{Name: a.Artist.Name}}
		if artistID != 0 {
			dto.Artist.ID = &artistID
		}
		payload.Albums = append(payload.Albums, dto)
		if len(payload.Albums) >= maxAlbums {
			break
		}
	}
	return payload
}

// buildRecommendations tries the requested window, widening (day → week →
// all) when there's too little history, and finally falls back to charts —
// so the homepage is never empty. Mirrors buildRecommendations in
// recommendations.ts.
func buildRecommendations(ctx context.Context, queries *db.Queries, deezer *catalog.DeezerClient, userID string, window recWindow) (recPayload, error) {
	played, err := getPlayedSets(ctx, queries, userID)
	if err != nil {
		return recPayload{}, err
	}

	var order []recWindow
	switch window {
	case windowDay:
		order = []recWindow{windowDay, windowWeek, windowAll}
	case windowWeek:
		order = []recWindow{windowWeek, windowAll}
	default:
		order = []recWindow{windowAll}
	}

	for _, candidate := range order {
		seeds, err := getSeedArtists(ctx, queries, userID, windowStart(candidate))
		if err != nil {
			return recPayload{}, err
		}
		needed := 1
		if candidate == window {
			needed = minSeedArtists
		}
		if len(seeds) < needed {
			continue
		}

		personal := buildPersonal(ctx, deezer, seeds, played)
		if personal != nil && (len(personal.Tracks) > 0 || len(personal.Albums) > 0) {
			personal.EffectiveWindow = string(candidate)
			return *personal, nil
		}
	}

	return buildFromCharts(ctx, deezer, played), nil
}

// getRecommendations is the cache-or-build entry point, mirroring
// getRecommendations in recommendations.ts.
func getRecommendations(ctx context.Context, queries *db.Queries, deezer *catalog.DeezerClient, userID string, window recWindow, forceRefresh bool) (RecommendationsDTO, error) {
	if !forceRefresh {
		cached, err := queries.GetRecommendation(ctx, userID, string(window))
		if err == nil && time.Since(cached.ComputedAt) < cacheTTL {
			var payload recPayload
			if jsonErr := json.Unmarshal([]byte(cached.Payload), &payload); jsonErr == nil {
				return toRecommendationsDTO(window, payload), nil
			}
			// Corrupt cache row — fall through and rebuild.
		}
	}

	payload, err := buildRecommendations(ctx, queries, deezer, userID, window)
	if err != nil {
		return RecommendationsDTO{}, err
	}

	serialized, err := json.Marshal(payload)
	if err == nil {
		_ = queries.UpsertRecommendation(ctx, db.UpsertRecommendationParams{
			ID: idgen.New(), UserID: userID, Window: string(window), Payload: string(serialized),
		})
	}
	return toRecommendationsDTO(window, payload), nil
}

func toRecommendationsDTO(window recWindow, payload recPayload) RecommendationsDTO {
	dto := RecommendationsDTO{
		Window:          string(window),
		EffectiveWindow: payload.EffectiveWindow,
		BasedOn:         payload.BasedOn,
		Tracks:          payload.Tracks,
		Albums:          payload.Albums,
	}
	if dto.BasedOn == nil {
		dto.BasedOn = []string{}
	}
	if dto.Tracks == nil {
		dto.Tracks = []RecTrackDTO{}
	}
	if dto.Albums == nil {
		dto.Albums = []RecAlbumDTO{}
	}
	return dto
}

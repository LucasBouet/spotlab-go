package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lucasbouet/spotlab-go/internal/catalog"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// Tuning knobs, same spirit as recommendations.go's: a handful of the
// account's top artists get an "all their tracks" + "radio" pair (Deezer's
// own catalogue, not limited to what's been played — discovery, seeded by
// history); a couple of top genres and the single top decade get a mix
// drawn *only* from tracks this account has actually played (history,
// nothing wider) — the "un peu basé sur nos écoutes, mais pas entièrement"
// split the user asked for.
const (
	smartPlaylistsCacheTTL = 24 * time.Hour

	smartPlaylistSeedArtists      = 3
	smartPlaylistArtistTrackLimit = 25

	smartPlaylistSeedGenres      = 2
	smartPlaylistGenreTrackLimit = 30

	smartPlaylistSeedDecades      = 1
	smartPlaylistDecadeTrackLimit = 30
)

func slugify(value string) string {
	var b strings.Builder
	lastDash := true // avoid a leading dash
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// fetchTracksByID re-fetches each track from Deezer rather than trusting
// what's cached in play_events — see DistinctTrackIDsByGenreForUser's
// comment. Missing/failed lookups are dropped silently, matching how a
// failed related-artist or top-tracks call degrades in recommendations.go.
func fetchTracksByID(ctx context.Context, deezer *catalog.DeezerClient, ids []int64) []RecTrackDTO {
	fetched := mapPool(ids, deezerConcurrency, func(id int64) *RecTrackDTO {
		raw, ok := deezer.FetchTrack(ctx, strconv.FormatInt(id, 10))
		if !ok {
			return nil
		}
		var t deezerTrackJSON
		if json.Unmarshal(raw, &t) != nil || t.ID == 0 {
			return nil
		}
		dto := t.toDTO()
		return &dto
	})
	tracks := make([]RecTrackDTO, 0, len(fetched))
	for _, t := range fetched {
		if t != nil {
			tracks = append(tracks, *t)
		}
	}
	return tracks
}

func decodeTrackList(raw json.RawMessage, ok bool) []RecTrackDTO {
	if !ok {
		return nil
	}
	var list []deezerTrackJSON
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	tracks := make([]RecTrackDTO, 0, len(list))
	for _, t := range list {
		if t.ID != 0 {
			tracks = append(tracks, t.toDTO())
		}
	}
	return tracks
}

func coverOf(tracks []RecTrackDTO) string {
	if len(tracks) == 0 {
		return ""
	}
	return tracks[0].Album.CoverMedium
}

// buildArtistPlaylists turns one artist — a top-listened seed (the home
// shelf build) or an arbitrary one a search just resolved (see
// handleArtistPlaylists) — into its "100%" and "Radio" pair. Either half
// can come back nil (a Deezer call failed, or came back empty) without
// dropping the other. The subtitle deliberately doesn't claim "based on
// your listening": unlike the genre/decade builds below, this one is
// reused for artists with no listening history behind them at all.
func buildArtistPlaylists(ctx context.Context, deezer *catalog.DeezerClient, artist artistRef) []SmartPlaylistDTO {
	var out []SmartPlaylistDTO

	topRaw, topOK := deezer.FetchArtistTopTracks(ctx, strconv.FormatInt(artist.ID, 10), smartPlaylistArtistTrackLimit)
	if tracks := decodeTrackList(topRaw, topOK); len(tracks) > 0 {
		out = append(out, SmartPlaylistDTO{
			ID: "artist-all-" + strconv.FormatInt(artist.ID, 10), Kind: "artist_all",
			Title: "100% " + artist.Name, Subtitle: "Titres populaires",
			Cover: coverOf(tracks), Tracks: tracks,
		})
	}

	radioRaw, radioOK := deezer.FetchArtistRadio(ctx, strconv.FormatInt(artist.ID, 10), smartPlaylistArtistTrackLimit)
	if tracks := decodeTrackList(radioRaw, radioOK); len(tracks) > 0 {
		out = append(out, SmartPlaylistDTO{
			ID: "artist-radio-" + strconv.FormatInt(artist.ID, 10), Kind: "artist_radio",
			Title: artist.Name + " Radio", Subtitle: "Titres similaires à " + artist.Name,
			Cover: coverOf(tracks), Tracks: tracks,
		})
	}

	return out
}

func buildGenrePlaylist(ctx context.Context, queries *db.Queries, deezer *catalog.DeezerClient, userID, genreName string) *SmartPlaylistDTO {
	ids, err := queries.DistinctTrackIDsByGenreForUser(ctx, userID, genreName, smartPlaylistGenreTrackLimit)
	if err != nil || len(ids) == 0 {
		return nil
	}
	tracks := fetchTracksByID(ctx, deezer, ids)
	if len(tracks) == 0 {
		return nil
	}
	return &SmartPlaylistDTO{
		ID: "genre-" + slugify(genreName), Kind: "genre",
		Title: genreName, Subtitle: "D'après vos écoutes",
		Cover: coverOf(tracks), Tracks: tracks,
	}
}

func buildDecadePlaylist(ctx context.Context, queries *db.Queries, deezer *catalog.DeezerClient, userID string, decade int64) *SmartPlaylistDTO {
	ids, err := queries.DistinctTrackIDsByDecadeForUser(ctx, userID, decade, decade+9, smartPlaylistDecadeTrackLimit)
	if err != nil || len(ids) == 0 {
		return nil
	}
	tracks := fetchTracksByID(ctx, deezer, ids)
	if len(tracks) == 0 {
		return nil
	}
	return &SmartPlaylistDTO{
		ID: fmt.Sprintf("decade-%d", decade), Kind: "decade",
		Title: fmt.Sprintf("Années %d", decade), Subtitle: "D'après vos écoutes",
		Cover: coverOf(tracks), Tracks: tracks,
	}
}

// buildSmartPlaylists mirrors buildRecommendations' shape (best-effort,
// never fails the whole build over one missing piece) but not its
// seed/window logic — this always looks at the account's all-time history,
// since these are meant to be a stable "your playlists" shelf, not
// something that reshuffles depending on how recently you listened.
func buildSmartPlaylists(ctx context.Context, queries *db.Queries, deezer *catalog.DeezerClient, userID string) (SmartPlaylistsDTO, error) {
	var playlists []SmartPlaylistDTO

	seedRows, err := queries.SeedArtists(ctx, userID, sql.NullTime{}, smartPlaylistSeedArtists)
	if err != nil {
		return SmartPlaylistsDTO{}, err
	}
	seeds := make([]seedArtist, 0, len(seedRows))
	for _, r := range seedRows {
		if r.ArtistName != "" && r.SampleTrackID != 0 {
			seeds = append(seeds, seedArtist{ArtistName: r.ArtistName, SampleTrackID: r.SampleTrackID})
		}
	}
	resolved := mapPool(seeds, deezerConcurrency, func(seed seedArtist) *artistRef {
		return resolveArtist(ctx, deezer, seed.SampleTrackID)
	})
	seenArtists := make(map[int64]bool)
	for _, artist := range resolved {
		if artist == nil || seenArtists[artist.ID] {
			continue
		}
		seenArtists[artist.ID] = true
		playlists = append(playlists, buildArtistPlaylists(ctx, deezer, *artist)...)
	}

	genres, err := queries.TopGenres(ctx, userID, smartPlaylistSeedGenres)
	if err != nil {
		return SmartPlaylistsDTO{}, err
	}
	for _, g := range genres {
		if playlist := buildGenrePlaylist(ctx, queries, deezer, userID, g.GenreName); playlist != nil {
			playlists = append(playlists, *playlist)
		}
	}

	decades, err := queries.TopDecades(ctx, userID, smartPlaylistSeedDecades)
	if err != nil {
		return SmartPlaylistsDTO{}, err
	}
	for _, d := range decades {
		if playlist := buildDecadePlaylist(ctx, queries, deezer, userID, d.Decade); playlist != nil {
			playlists = append(playlists, *playlist)
		}
	}

	if playlists == nil {
		playlists = []SmartPlaylistDTO{}
	}
	return SmartPlaylistsDTO{Playlists: playlists}, nil
}

// getSmartPlaylists is the cache-or-build entry point, same TTL/refresh
// contract as getRecommendations.
func getSmartPlaylists(ctx context.Context, queries *db.Queries, deezer *catalog.DeezerClient, userID string, forceRefresh bool) (SmartPlaylistsDTO, error) {
	if !forceRefresh {
		cached, err := queries.GetSmartPlaylistsCache(ctx, userID)
		if err == nil && time.Since(cached.ComputedAt) < smartPlaylistsCacheTTL {
			var dto SmartPlaylistsDTO
			if jsonErr := json.Unmarshal([]byte(cached.Payload), &dto); jsonErr == nil {
				return dto, nil
			}
			// Corrupt cache row — fall through and rebuild.
		}
	}

	dto, err := buildSmartPlaylists(ctx, queries, deezer, userID)
	if err != nil {
		return SmartPlaylistsDTO{}, err
	}
	if serialized, err := json.Marshal(dto); err == nil {
		_ = queries.UpsertSmartPlaylistsCache(ctx, userID, string(serialized))
	}
	return dto, nil
}

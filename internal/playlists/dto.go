// Package playlists implements playlist CRUD and track membership —
// /api/playlists/**.
package playlists

import (
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

type PlaylistSummaryDTO struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	TrackCount int64    `json:"trackCount"`
	Covers     []string `json:"covers"`
	CreatedAt  string   `json:"createdAt"`
	UpdatedAt  string   `json:"updatedAt"`
}

func playlistSummaryDTO(p db.PlaylistWithCovers) PlaylistSummaryDTO {
	covers := p.Covers
	if covers == nil {
		covers = []string{}
	}
	return PlaylistSummaryDTO{
		ID:         p.ID,
		Name:       p.Name,
		TrackCount: p.TrackCount,
		Covers:     covers,
		CreatedAt:  p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  p.UpdatedAt.Format(time.RFC3339),
	}
}

func playlistSummaryDTOs(playlists []db.PlaylistWithCovers) []PlaylistSummaryDTO {
	out := make([]PlaylistSummaryDTO, 0, len(playlists))
	for _, p := range playlists {
		out = append(out, playlistSummaryDTO(p))
	}
	return out
}

// deezerArtistRef / deezerAlbumRef mirror the Android client's
// DeezerArtistDto / DeezerAlbumRefDto shapes closely enough for the two
// fields a playlist track carries (id/name, title/cover_medium) — a
// playlist row doesn't have the rest of those DTOs' fields (pictures, fan
// counts) since it's built from our own denormalized columns, not a live
// Deezer response.
type trackArtistDTO struct {
	ID   *int64 `json:"id"`
	Name string `json:"name"`
}

type trackAlbumDTO struct {
	Title       string `json:"title"`
	CoverMedium string `json:"cover_medium"`
}

// PlaylistTrackDTO mirrors PlaylistTrackDto exactly. `RowKey` (the
// PlaylistTrack row id) is the only safe handle for removal — `ID` is the
// Deezer track id and the same track may appear more than once in a
// playlist.
type PlaylistTrackDTO struct {
	RowKey   string         `json:"rowKey"`
	ID       int64          `json:"id"`
	Title    string         `json:"title"`
	Duration int64          `json:"duration"`
	Artist   trackArtistDTO `json:"artist"`
	Album    trackAlbumDTO  `json:"album"`
	AddedAt  string         `json:"addedAt"`
}

func playlistTrackDTO(t db.PlaylistTrack) PlaylistTrackDTO {
	var artistID *int64
	if t.ArtistID.Valid {
		artistID = &t.ArtistID.Int64
	}
	return PlaylistTrackDTO{
		RowKey:   t.ID,
		ID:       t.DeezerTrackID,
		Title:    t.Title,
		Duration: t.Duration,
		Artist:   trackArtistDTO{ID: artistID, Name: t.ArtistName},
		Album:    trackAlbumDTO{Title: t.AlbumTitle, CoverMedium: t.AlbumCover},
		AddedAt:  t.AddedAt.Format(time.RFC3339),
	}
}

func playlistTrackDTOs(tracks []db.PlaylistTrack) []PlaylistTrackDTO {
	out := make([]PlaylistTrackDTO, 0, len(tracks))
	for _, t := range tracks {
		out = append(out, playlistTrackDTO(t))
	}
	return out
}

type PlaylistDetailDTO struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	CreatedAt string             `json:"createdAt"`
	UpdatedAt string             `json:"updatedAt"`
	Tracks    []PlaylistTrackDTO `json:"tracks"`
}

func playlistDetailDTO(p db.Playlist, tracks []db.PlaylistTrack) PlaylistDetailDTO {
	return PlaylistDetailDTO{
		ID:        p.ID,
		Name:      p.Name,
		CreatedAt: p.CreatedAt.Format(time.RFC3339),
		UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
		Tracks:    playlistTrackDTOs(tracks),
	}
}

type PlaylistRefDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func playlistRefDTO(p db.Playlist) PlaylistRefDTO {
	return PlaylistRefDTO{ID: p.ID, Name: p.Name}
}

type PlaylistMembershipDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	HasTrack bool   `json:"hasTrack"`
}

func playlistMembershipDTOs(rows []db.PlaylistMembership) []PlaylistMembershipDTO {
	out := make([]PlaylistMembershipDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, PlaylistMembershipDTO{ID: row.ID, Name: row.Name, HasTrack: row.HasTrack})
	}
	return out
}

// Package library implements liked tracks — GET/PUT/DELETE /api/library/*.
package library

import (
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// LikedTrackDTO mirrors the Android client's LikedTrackDto exactly
// (data/remote/dto/LibraryDto.kt).
type LikedTrackDTO struct {
	ID            string `json:"id"`
	DeezerTrackID int64  `json:"deezerTrackId"`
	Title         string `json:"title"`
	ArtistName    string `json:"artistName"`
	ArtistID      *int64 `json:"artistId"`
	AlbumTitle    string `json:"albumTitle"`
	AlbumCover    string `json:"albumCover"`
	Duration      int64  `json:"duration"`
	CreatedAt     string `json:"createdAt"`
}

func likedTrackDTO(t db.LikedTrack) LikedTrackDTO {
	var artistID *int64
	if t.ArtistID.Valid {
		artistID = &t.ArtistID.Int64
	}
	return LikedTrackDTO{
		ID:            t.ID,
		DeezerTrackID: t.DeezerTrackID,
		Title:         t.Title,
		ArtistName:    t.ArtistName,
		ArtistID:      artistID,
		AlbumTitle:    t.AlbumTitle,
		AlbumCover:    t.AlbumCover,
		Duration:      t.Duration,
		CreatedAt:     t.CreatedAt.Format(time.RFC3339),
	}
}

func likedTrackDTOs(tracks []db.LikedTrack) []LikedTrackDTO {
	// Never nil: an empty liked library must serialize as `"tracks": []`,
	// not an absent/null field the Android DTO would just default anyway,
	// but "explicit empty list" is the honest shape of "you have zero".
	out := make([]LikedTrackDTO, 0, len(tracks))
	for _, t := range tracks {
		out = append(out, likedTrackDTO(t))
	}
	return out
}

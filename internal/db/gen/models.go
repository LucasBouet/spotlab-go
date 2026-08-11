// Row types for every table. See db.go's package doc for why this is
// hand-maintained rather than sqlc-generated.
package db

import (
	"database/sql"
	"time"
)

type ActivationUse struct {
	CodeHash string         `db:"code_hash" json:"code_hash"`
	UsedAt   time.Time      `db:"used_at" json:"used_at"`
	UserID   sql.NullString `db:"user_id" json:"user_id"`
}

type AppSetting struct {
	Key       string    `db:"key" json:"key"`
	Value     string    `db:"value" json:"value"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

type Device struct {
	ID         string    `db:"id" json:"id"`
	UserID     string    `db:"user_id" json:"user_id"`
	DeviceID   string    `db:"device_id" json:"device_id"`
	Name       string    `db:"name" json:"name"`
	Platform   string    `db:"platform" json:"platform"`
	LastSeenAt time.Time `db:"last_seen_at" json:"last_seen_at"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
}

type Friendship struct {
	ID          string    `db:"id" json:"id"`
	RequesterID string    `db:"requester_id" json:"requester_id"`
	AddresseeID string    `db:"addressee_id" json:"addressee_id"`
	Status      string    `db:"status" json:"status"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

type LikedTrack struct {
	ID            string        `db:"id" json:"id"`
	UserID        string        `db:"user_id" json:"user_id"`
	DeezerTrackID int64         `db:"deezer_track_id" json:"deezer_track_id"`
	Title         string        `db:"title" json:"title"`
	ArtistName    string        `db:"artist_name" json:"artist_name"`
	ArtistID      sql.NullInt64 `db:"artist_id" json:"artist_id"`
	AlbumTitle    string        `db:"album_title" json:"album_title"`
	AlbumCover    string        `db:"album_cover" json:"album_cover"`
	Duration      int64         `db:"duration" json:"duration"`
	CreatedAt     time.Time     `db:"created_at" json:"created_at"`
}

type PlayEvent struct {
	ID            string    `db:"id" json:"id"`
	UserID        string    `db:"user_id" json:"user_id"`
	DeezerTrackID int64     `db:"deezer_track_id" json:"deezer_track_id"`
	Title         string    `db:"title" json:"title"`
	ArtistName    string    `db:"artist_name" json:"artist_name"`
	AlbumTitle    string    `db:"album_title" json:"album_title"`
	AlbumCover    string    `db:"album_cover" json:"album_cover"`
	Duration      int64     `db:"duration" json:"duration"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

type Playlist struct {
	ID        string    `db:"id" json:"id"`
	UserID    string    `db:"user_id" json:"user_id"`
	Name      string    `db:"name" json:"name"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

type PlaylistTrack struct {
	ID            string        `db:"id" json:"id"`
	PlaylistID    string        `db:"playlist_id" json:"playlist_id"`
	DeezerTrackID int64         `db:"deezer_track_id" json:"deezer_track_id"`
	Title         string        `db:"title" json:"title"`
	ArtistName    string        `db:"artist_name" json:"artist_name"`
	ArtistID      sql.NullInt64 `db:"artist_id" json:"artist_id"`
	AlbumTitle    string        `db:"album_title" json:"album_title"`
	AlbumCover    string        `db:"album_cover" json:"album_cover"`
	Duration      int64         `db:"duration" json:"duration"`
	AddedAt       time.Time     `db:"added_at" json:"added_at"`
}

type Recommendation struct {
	ID         string    `db:"id" json:"id"`
	UserID     string    `db:"user_id" json:"user_id"`
	Window     string    `db:"window" json:"window"`
	Payload    string    `db:"payload" json:"payload"`
	ComputedAt time.Time `db:"computed_at" json:"computed_at"`
}

type Session struct {
	ID        string    `db:"id" json:"id"`
	UserID    string    `db:"user_id" json:"user_id"`
	ExpiresAt time.Time `db:"expires_at" json:"expires_at"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type TrackGenre struct {
	DeezerTrackID int64          `db:"deezer_track_id" json:"deezer_track_id"`
	AlbumID       sql.NullInt64  `db:"album_id" json:"album_id"`
	GenreID       sql.NullInt64  `db:"genre_id" json:"genre_id"`
	GenreName     sql.NullString `db:"genre_name" json:"genre_name"`
	Source        sql.NullString `db:"source" json:"source"`
	UpdatedAt     time.Time      `db:"updated_at" json:"updated_at"`
}

type User struct {
	ID           string         `db:"id" json:"id"`
	Email        string         `db:"email" json:"email"`
	Name         sql.NullString `db:"name" json:"name"`
	PasswordHash string         `db:"password_hash" json:"password_hash"`
	Role         string         `db:"role" json:"role"`
	CreatedAt    time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time      `db:"updated_at" json:"updated_at"`
}

type UserSetting struct {
	UserID    string    `db:"user_id" json:"user_id"`
	Key       string    `db:"key" json:"key"`
	Value     string    `db:"value" json:"value"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

package playlists

import (
	"database/sql"
	"testing"
	"time"

	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

func TestParsePositiveTrackID(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
		ok   bool
	}{
		{"111", 111, true},
		{"", 0, false}, // absent param must fail exactly like a malformed one
		{"0", 0, true},
		{"-1", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, ok := parsePositiveTrackID(c.raw)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parsePositiveTrackID(%q) = (%d, %v), attendu (%d, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestPlaylistTrackDTORowKeyIsRowIDNotDeezerID(t *testing.T) {
	track := dbgen.PlaylistTrack{
		ID: "row-abc", DeezerTrackID: 999, Title: "T", ArtistName: "A",
		AlbumTitle: "Alb", AlbumCover: "c.jpg", Duration: 200, AddedAt: time.Unix(0, 0),
	}
	dto := playlistTrackDTO(track)
	if dto.RowKey != "row-abc" {
		t.Errorf("RowKey = %q, attendu row-abc", dto.RowKey)
	}
	if dto.ID != 999 {
		t.Errorf("ID = %d, attendu 999 — rowKey et id Deezer ne doivent jamais être confondus", dto.ID)
	}
}

func TestPlaylistTrackDTOOmitsArtistIDWhenNull(t *testing.T) {
	track := dbgen.PlaylistTrack{
		ID: "row1", DeezerTrackID: 1, Title: "T", ArtistName: "A",
		ArtistID: sql.NullInt64{Valid: false}, AlbumTitle: "Alb", AlbumCover: "c.jpg",
		Duration: 200, AddedAt: time.Unix(0, 0),
	}
	dto := playlistTrackDTO(track)
	if dto.Artist.ID != nil {
		t.Errorf("Artist.ID = %v, attendu nil", *dto.Artist.ID)
	}
}

func TestPlaylistSummaryDTONeverHasNilCovers(t *testing.T) {
	p := dbgen.PlaylistWithCovers{
		Playlist: dbgen.Playlist{ID: "p1", Name: "P", CreatedAt: time.Unix(0, 0), UpdatedAt: time.Unix(0, 0)},
		Covers:   nil,
	}
	dto := playlistSummaryDTO(p)
	if dto.Covers == nil {
		t.Error("Covers = nil, attendu un slice vide non-nil")
	}
}

package library

import (
	"database/sql"
	"testing"
	"time"

	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

func TestParseTrackID(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
		ok   bool
	}{
		{"123", 123, true},
		{"0", 0, true},
		{"", 0, false},
		{"-1", 0, false},
		{"abc", 0, false},
		{"12.5", 0, false},
		{"12abc", 0, false},
	}
	for _, c := range cases {
		got, ok := parseTrackID(c.raw)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseTrackID(%q) = (%d, %v), attendu (%d, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestLikedTrackDTOOmitsArtistIDWhenNull(t *testing.T) {
	track := dbgen.LikedTrack{
		ID: "row1", DeezerTrackID: 1, Title: "T", ArtistName: "A",
		ArtistID: sql.NullInt64{Valid: false}, AlbumTitle: "Alb", AlbumCover: "c.jpg",
		Duration: 200, CreatedAt: time.Unix(0, 0),
	}
	dto := likedTrackDTO(track)
	if dto.ArtistID != nil {
		t.Errorf("ArtistID = %v, attendu nil — Android decode un artistId absent en null, jamais en 0", *dto.ArtistID)
	}
}

func TestLikedTrackDTOKeepsArtistIDWhenPresent(t *testing.T) {
	track := dbgen.LikedTrack{
		ID: "row1", DeezerTrackID: 1, Title: "T", ArtistName: "A",
		ArtistID: sql.NullInt64{Int64: 42, Valid: true}, AlbumTitle: "Alb", AlbumCover: "c.jpg",
		Duration: 200, CreatedAt: time.Unix(0, 0),
	}
	dto := likedTrackDTO(track)
	if dto.ArtistID == nil || *dto.ArtistID != 42 {
		t.Errorf("ArtistID = %v, attendu 42", dto.ArtistID)
	}
}

func TestLikedTrackDTOsNeverReturnsNilSlice(t *testing.T) {
	// An empty library must serialize as "tracks": [], not "tracks": null —
	// both decode fine under ignoreUnknownKeys-style leniency, but null
	// would be a needless divergence from what the old server always sent.
	dtos := likedTrackDTOs(nil)
	if dtos == nil {
		t.Error("likedTrackDTOs(nil) = nil, attendu un slice vide non-nil")
	}
	if len(dtos) != 0 {
		t.Errorf("len = %d, attendu 0", len(dtos))
	}
}

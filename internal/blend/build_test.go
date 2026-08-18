package blend

import (
	"database/sql"
	"math/rand"
	"testing"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

func likedTrack(id int64, title string) db.LikedTrack {
	return db.LikedTrack{
		DeezerTrackID: id, Title: title, ArtistName: "Artist", AlbumTitle: "Album", AlbumCover: "c.jpg", Duration: 200,
	}
}

func TestBuildTrackListDedupesTrackLikedByBothMembersKeepingFirstAttribution(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	shared := likedTrack(1, "Shared Song")
	// The shared track sits at index 1 in A but index 0 in B: B's copy is
	// necessarily processed first (round-robin advances both lists' indices
	// together), so by the time A reaches its own copy, seen[1] is already
	// set by B — exercising A's own `!seen[...]` check, not just B's. A
	// same-index arrangement wouldn't: A always goes first within a round,
	// so it would "win" regardless of whether its own check actually works.
	a := []db.LikedTrack{likedTrack(3, "Alice Only"), shared}
	b := []db.LikedTrack{shared, likedTrack(2, "Bob Only")}

	tracks := buildTrackList(rng, a, "alice-id", "Alice", b, "bob-id", "Bob")

	var seen1 int
	for _, tr := range tracks {
		if tr.ID == 1 {
			seen1++
		}
	}
	if seen1 != 1 {
		t.Errorf("le morceau partagé apparaît %d fois, attendu 1 (pas de doublon avec deux attributions)", seen1)
	}
	if len(tracks) != 3 {
		t.Errorf("len(tracks) = %d, attendu 3 (Alice Only + partagé (une fois) + Bob Only)", len(tracks))
	}
}

func TestBuildTrackListCapsPerMemberBeforeInterleaving(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var a, b []db.LikedTrack
	for i := int64(0); i < maxPerMemberTracks+10; i++ {
		a = append(a, likedTrack(i, "A"))
	}
	for i := int64(1000); i < 1000+maxPerMemberTracks+10; i++ {
		b = append(b, likedTrack(i, "B"))
	}

	tracks := buildTrackList(rng, a, "a", "Alice", b, "b", "Bob")

	var fromA, fromB int
	for _, tr := range tracks {
		if tr.AddedByUserID == "a" {
			fromA++
		} else {
			fromB++
		}
	}
	if fromA > maxPerMemberTracks || fromB > maxPerMemberTracks {
		t.Errorf("fromA=%d fromB=%d, aucun ne doit dépasser maxPerMemberTracks=%d", fromA, fromB, maxPerMemberTracks)
	}
	if len(tracks) > maxBlendTracks {
		t.Errorf("len(tracks) = %d, dépasse maxBlendTracks=%d", len(tracks), maxBlendTracks)
	}
}

func TestDailySeedIsStablePerDayButDiffersAcrossDays(t *testing.T) {
	today := dailySeed("blend-1", "2026-08-18")
	todayAgain := dailySeed("blend-1", "2026-08-18")
	tomorrow := dailySeed("blend-1", "2026-08-19")
	otherBlend := dailySeed("blend-2", "2026-08-18")

	if today != todayAgain {
		t.Error("la seed doit être identique pour le même blend le même jour")
	}
	if today == tomorrow {
		t.Error("la seed doit changer d'un jour à l'autre")
	}
	if today == otherBlend {
		t.Error("la seed doit différer entre deux blends distincts")
	}
}

func TestDisplayNameFallsBackToEmailWhenNameIsNull(t *testing.T) {
	if got := displayName(sql.NullString{Valid: false}, "a@example.com"); got != "a@example.com" {
		t.Errorf("displayName = %q, attendu l'email en repli", got)
	}
	if got := displayName(sql.NullString{String: "Alice", Valid: true}, "a@example.com"); got != "Alice" {
		t.Errorf("displayName = %q, attendu le nom quand présent", got)
	}
}

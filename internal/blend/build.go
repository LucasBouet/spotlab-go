// Package blend implements Blend: a 2-person shared playlist mixing both
// members' liked tracks, refreshed once per calendar day, each track
// labeled with whose liked list it came from — POST/GET/DELETE /api/blends.
//
// No invite/accept step: creating a Blend requires the two accounts
// already be ACCEPTED friends (internal/social), which is itself already a
// mutual-consent ceremony — a second one here would just be friction.
package blend

import (
	"context"
	"database/sql"
	"encoding/json"
	"hash/fnv"
	"math/rand"
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// maxPerMemberTracks bounds how many of one member's liked tracks feed a
// single day's mix (shuffled first, so it's a different subset most days
// rather than always "your 20 oldest likes"); maxBlendTracks bounds the
// interleaved result. Both arbitrary, tuned to feel like a real playlist
// rather than a firehose.
const (
	maxPerMemberTracks = 20
	maxBlendTracks     = 40
)

// BlendArtistDTO/BlendAlbumRefDTO deliberately duplicate the shape of
// stats.RecArtistDTO/RecAlbumRefDTO rather than importing internal/stats
// for two small structs — this package has no other reason to depend on
// stats, and the two feature areas are otherwise unrelated.
type BlendArtistDTO struct {
	ID   *int64 `json:"id,omitempty"`
	Name string `json:"name"`
}

type BlendAlbumRefDTO struct {
	Title       string `json:"title"`
	CoverMedium string `json:"cover_medium"`
}

// BlendTrackDTO is structurally a DeezerTrack (see catalog.DeezerClient's
// doc comment on why the rest of this API forwards that shape verbatim)
// plus the two attribution fields Android needs for the "Ajouté par X"
// caption — that pair is the whole reason this isn't just RecTrackDTO.
type BlendTrackDTO struct {
	ID            int64            `json:"id"`
	Title         string           `json:"title"`
	Duration      int64            `json:"duration"`
	Artist        BlendArtistDTO   `json:"artist"`
	Album         BlendAlbumRefDTO `json:"album"`
	AddedByUserID string           `json:"addedByUserId"`
	AddedByName   string           `json:"addedByName"`
}

// BlendDTO is one row of GET /api/blends, from the requesting user's point
// of view — Title/Subtitle/PartnerUserID name the *other* member, so the
// same underlying blend row answers differently depending on who asks.
type BlendDTO struct {
	ID            string          `json:"id"`
	Title         string          `json:"title"`
	Subtitle      string          `json:"subtitle"`
	Cover         string          `json:"cover"`
	PartnerUserID string          `json:"partnerUserId"`
	Tracks        []BlendTrackDTO `json:"tracks"`
}

// blendCachePayload is what actually gets cached — just the track list.
// The rest of BlendDTO (title, partner) is symmetric-but-not-identical
// between the two members and is always computed fresh per request; caching
// the whole DTO would bake in whichever member happened to trigger the
// build as "you", and show the wrong name to the other one.
type blendCachePayload struct {
	Tracks []BlendTrackDTO `json:"tracks"`
}

func displayName(name sql.NullString, email string) string {
	if name.Valid && name.String != "" {
		return name.String
	}
	return email
}

func todayUTC() string {
	return time.Now().UTC().Format("2006-01-02")
}

// dailySeed makes the day's shuffle deterministic for a given blend+date
// (so concurrent requests the same day see the same mix) but different
// from every other day, without needing an actual cron job — the build
// just checks blend_cache.computed_for_date against today's date and
// rebuilds with a new seed once it's stale, the same lazy-TTL shape
// smart_playlists_cache/recommendations already use.
func dailySeed(blendID, forDate string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(blendID + "|" + forDate))
	return int64(h.Sum64())
}

func shuffledCopy(rng *rand.Rand, tracks []db.LikedTrack) []db.LikedTrack {
	out := append([]db.LikedTrack(nil), tracks...)
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func trackDTOFromLiked(t db.LikedTrack, addedByUserID, addedByName string) BlendTrackDTO {
	var artistID *int64
	if t.ArtistID.Valid {
		id := t.ArtistID.Int64
		artistID = &id
	}
	return BlendTrackDTO{
		ID: t.DeezerTrackID, Title: t.Title, Duration: t.Duration,
		Artist:        BlendArtistDTO{ID: artistID, Name: t.ArtistName},
		Album:         BlendAlbumRefDTO{Title: t.AlbumTitle, CoverMedium: t.AlbumCover},
		AddedByUserID: addedByUserID, AddedByName: addedByName,
	}
}

// buildTrackList round-robins the two (already day-shuffled, capped)
// liked lists, skipping a track a second time if both members happen to
// have liked it — it keeps whichever member's turn came first that day
// rather than showing the same track twice with two different
// attributions.
func buildTrackList(rng *rand.Rand, likedA []db.LikedTrack, idA, nameA string, likedB []db.LikedTrack, idB, nameB string) []BlendTrackDTO {
	a := shuffledCopy(rng, likedA)
	if len(a) > maxPerMemberTracks {
		a = a[:maxPerMemberTracks]
	}
	b := shuffledCopy(rng, likedB)
	if len(b) > maxPerMemberTracks {
		b = b[:maxPerMemberTracks]
	}

	seen := make(map[int64]bool, len(a)+len(b))
	out := make([]BlendTrackDTO, 0, maxBlendTracks)
	ai, bi := 0, 0
	for (ai < len(a) || bi < len(b)) && len(out) < maxBlendTracks {
		if ai < len(a) {
			t := a[ai]
			ai++
			if !seen[t.DeezerTrackID] {
				seen[t.DeezerTrackID] = true
				out = append(out, trackDTOFromLiked(t, idA, nameA))
				if len(out) >= maxBlendTracks {
					break
				}
			}
		}
		if bi < len(b) {
			t := b[bi]
			bi++
			if !seen[t.DeezerTrackID] {
				seen[t.DeezerTrackID] = true
				out = append(out, trackDTOFromLiked(t, idB, nameB))
			}
		}
	}
	return out
}

// getOrBuildTracks is the cache-or-build entry point: same shape as
// getRecommendations/getSmartPlaylists, but "stale" means "not computed
// for today's date" instead of a rolling TTL.
func getOrBuildTracks(ctx context.Context, queries *db.Queries, row db.BlendWithUsers, forceRefresh bool) ([]BlendTrackDTO, error) {
	today := todayUTC()
	if !forceRefresh {
		cached, err := queries.GetBlendCache(ctx, row.ID)
		if err == nil && cached.ComputedForDate == today {
			var payload blendCachePayload
			if json.Unmarshal([]byte(cached.Payload), &payload) == nil {
				return payload.Tracks, nil
			}
			// Corrupt cache row — fall through and rebuild.
		}
	}

	likedA, err := queries.ListLikedTracksRecent(ctx, row.UserAID)
	if err != nil {
		return nil, err
	}
	likedB, err := queries.ListLikedTracksRecent(ctx, row.UserBID)
	if err != nil {
		return nil, err
	}

	rng := rand.New(rand.NewSource(dailySeed(row.ID, today)))
	tracks := buildTrackList(rng, likedA, row.UserAID, displayName(row.UserAName, row.UserAEmail),
		likedB, row.UserBID, displayName(row.UserBName, row.UserBEmail))

	if serialized, err := json.Marshal(blendCachePayload{Tracks: tracks}); err == nil {
		_ = queries.UpsertBlendCache(ctx, db.UpsertBlendCacheParams{
			BlendID: row.ID, Payload: string(serialized), ComputedForDate: today,
		})
	}
	return tracks, nil
}

// buildBlendDTO assembles the requester-facing view: getOrBuildTracks for
// the shared part, then names the *other* member as the partner.
func buildBlendDTO(ctx context.Context, queries *db.Queries, row db.BlendWithUsers, requestingUserID string, forceRefresh bool) (BlendDTO, error) {
	tracks, err := getOrBuildTracks(ctx, queries, row, forceRefresh)
	if err != nil {
		return BlendDTO{}, err
	}
	if tracks == nil {
		tracks = []BlendTrackDTO{}
	}

	partnerID, partnerName := row.UserBID, displayName(row.UserBName, row.UserBEmail)
	if requestingUserID == row.UserBID {
		partnerID, partnerName = row.UserAID, displayName(row.UserAName, row.UserAEmail)
	}

	cover := ""
	if len(tracks) > 0 {
		cover = tracks[0].Album.CoverMedium
	}

	return BlendDTO{
		ID:            "blend-" + row.ID,
		Title:         "Blend avec " + partnerName,
		Subtitle:      "Mis à jour aujourd'hui",
		Cover:         cover,
		PartnerUserID: partnerID,
		Tracks:        tracks,
	}, nil
}

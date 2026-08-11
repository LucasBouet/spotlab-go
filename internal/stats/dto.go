package stats

// StatEntryDTO is one ranked row in the "Statistiques" tab (a track, album,
// or genre) — same shape for all three so the client renders them with one
// component, matching StatEntry in stats.ts.
type StatEntryDTO struct {
	Label    string `json:"label"`
	Sublabel string `json:"sublabel,omitempty"`
	Cover    string `json:"cover,omitempty"`
	Count    int64  `json:"count"`
}

// ListeningStatsDTO is the GET /api/stats payload, matching ListeningStats
// in stats.ts field for field.
type ListeningStatsDTO struct {
	TotalPlays   int64          `json:"totalPlays"`
	TotalSeconds int64          `json:"totalSeconds"`
	TopTracks    []StatEntryDTO `json:"topTracks"`
	TopAlbums    []StatEntryDTO `json:"topAlbums"`
	TopGenres    []StatEntryDTO `json:"topGenres"`
}

// RecArtistDTO's ID is a pointer so an unresolved artist id is omitted
// entirely rather than serialized as 0 — Deezer ids are never 0, so Android
// treats a present artistId as trustworthy.
type RecArtistDTO struct {
	ID   *int64 `json:"id,omitempty"`
	Name string `json:"name"`
}

type RecAlbumRefDTO struct {
	Title       string `json:"title"`
	CoverMedium string `json:"cover_medium"`
}

// RecTrackDTO is structurally a DeezerTrack (same field names/casing as the
// rest of the catalog's passthrough responses), so the client's existing
// track parser reads it with no special-casing — mirrors RecTrack in
// recommendations.ts, which leans on the exact same trick for the web UI.
type RecTrackDTO struct {
	ID       int64          `json:"id"`
	Title    string         `json:"title"`
	Duration int64          `json:"duration"`
	Artist   RecArtistDTO   `json:"artist"`
	Album    RecAlbumRefDTO `json:"album"`
}

type RecAlbumDTO struct {
	ID          int64        `json:"id"`
	Title       string       `json:"title"`
	CoverMedium string       `json:"cover_medium"`
	Artist      RecArtistDTO `json:"artist"`
}

// RecommendationsDTO is the GET /api/recommendations payload — window is
// always the requested window; effectiveWindow is what was actually used
// ("day"/"week"/"all", possibly widened, or "charts" on cold start).
type RecommendationsDTO struct {
	Window          string        `json:"window"`
	EffectiveWindow string        `json:"effectiveWindow"`
	BasedOn         []string      `json:"basedOn"`
	Tracks          []RecTrackDTO `json:"tracks"`
	Albums          []RecAlbumDTO `json:"albums"`
}

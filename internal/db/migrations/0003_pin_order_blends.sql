-- Manual reordering/pinning (playlists tab + home shelf) and Blend
-- (2-person shared playlist, daily-refreshed mix of both members' liked
-- tracks — see internal/blend).

-- +goose Up

-- Real playlists gain a manual sort position and a pin flag directly on the
-- row they already are — no join table needed, unlike shelf_prefs below.
ALTER TABLE playlists ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;
ALTER TABLE playlists ADD COLUMN position INTEGER NOT NULL DEFAULT 0;

-- The home shelf's cards (smart playlists, blends) are computed, not rows —
-- there is nothing to add a column to, so pin/order preferences live in
-- their own table instead, keyed by whatever stable string id the shelf
-- item already has ("artist-all-123", "genre-metalcore", "blend-abc...").
-- An id with no row here just means "never touched, use the default
-- build order" — see internal/shelf.
CREATE TABLE shelf_prefs (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_id    TEXT NOT NULL,
    pinned     INTEGER NOT NULL DEFAULT 0,
    position   INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, item_id)
);

-- One row per pair of blended friends. user_a_id/user_b_id are stored in a
-- canonical order (lexicographically smaller id first — see
-- internal/blend) precisely so the UNIQUE constraint catches "this blend
-- already exists" no matter which of the two creates it.
CREATE TABLE blends (
    id         TEXT NOT NULL PRIMARY KEY,
    user_a_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_b_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_a_id, user_b_id)
);

-- computed_for_date ("2026-08-18", UTC) rather than a rolling TTL like
-- recommendations/smart_playlists_cache: a Blend is meant to flip over on
-- the calendar day, not 24h after whoever last opened it.
CREATE TABLE blend_cache (
    blend_id          TEXT NOT NULL PRIMARY KEY REFERENCES blends(id) ON DELETE CASCADE,
    payload           TEXT NOT NULL,
    computed_for_date TEXT NOT NULL,
    computed_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE blend_cache;
DROP TABLE blends;
DROP TABLE shelf_prefs;
ALTER TABLE playlists DROP COLUMN position;
ALTER TABLE playlists DROP COLUMN pinned;

-- Auto-generated "home screen" playlists (100% <Artist>, <Artist> Radio,
-- genre and decade mixes) — see internal/stats/smartplaylists.go. Two new
-- caches, same shape as their Phase 7 siblings: track_release_years mirrors
-- track_genres (global, resolved lazily on a qualified play, one extra
-- Deezer field read off the same track object) and smart_playlists_cache
-- mirrors recommendations (per-user, TTL'd, rebuilt on demand).

-- +goose Up

CREATE TABLE track_release_years (
    deezer_track_id INTEGER NOT NULL PRIMARY KEY,
    year            INTEGER NOT NULL,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE smart_playlists_cache (
    user_id     TEXT NOT NULL PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    payload     TEXT NOT NULL,
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE smart_playlists_cache;
DROP TABLE track_release_years;

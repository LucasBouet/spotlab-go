-- Schéma initial. Écrit librement pour Go (base vide actée, voir docs/PLAN.md
-- §0) plutôt que transcrit des 12 migrations Prisma historiques, qui ne sont
-- que des artefacts d'évolution incrémentale sans valeur pour un projet neuf.
--
-- Correspondance de référence avec schema.prisma (spotlab_) — mêmes entités,
-- ids en TEXT (cuid côté Prisma ; ici générés côté Go, voir internal/idgen).
-- Passkey est délibérément absent : cérémonie WebAuthn web-only, hors v1.

-- +goose Up

CREATE TABLE users (
    id            TEXT NOT NULL PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    name          TEXT,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'USER' CHECK (role IN ('ADMIN', 'USER')),
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Le token brut EST la clé primaire (64 hex chars, randomBytes(32)) — pas de
-- signature séparée, la présence de la ligne fait foi. Reproduit le modèle
-- de session.ts de l'ancien serveur.
CREATE TABLE sessions (
    id         TEXT NOT NULL PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_sessions_user_id ON sessions(user_id);

CREATE TABLE liked_tracks (
    id               TEXT NOT NULL PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    deezer_track_id  INTEGER NOT NULL,
    title            TEXT NOT NULL,
    artist_name      TEXT NOT NULL,
    artist_id        INTEGER,
    album_title      TEXT NOT NULL,
    album_cover      TEXT NOT NULL,
    duration         INTEGER NOT NULL,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, deezer_track_id)
);

CREATE TABLE playlists (
    id         TEXT NOT NULL PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_playlists_user_id ON playlists(user_id);

-- Pas de contrainte unique sur (playlist_id, deezer_track_id) : un même
-- titre peut apparaître plusieurs fois dans une playlist. `id` est le seul
-- handle sûr pour cibler une ligne précise (le "rowKey" côté API/Android).
CREATE TABLE playlist_tracks (
    id               TEXT NOT NULL PRIMARY KEY,
    playlist_id      TEXT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
    deezer_track_id  INTEGER NOT NULL,
    title            TEXT NOT NULL,
    artist_name      TEXT NOT NULL,
    artist_id        INTEGER,
    album_title      TEXT NOT NULL,
    album_cover      TEXT NOT NULL,
    duration         INTEGER NOT NULL,
    added_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_playlist_tracks_playlist_id ON playlist_tracks(playlist_id);

CREATE TABLE app_settings (
    key        TEXT NOT NULL PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE user_settings (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, key)
);

CREATE TABLE devices (
    id            TEXT NOT NULL PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id     TEXT NOT NULL,
    name          TEXT NOT NULL,
    platform      TEXT NOT NULL,
    last_seen_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, device_id)
);

-- Directionnelle : requester -> addressee. Une demande dans l'autre sens
-- alors qu'une ligne PENDING existe déjà doit être auto-acceptée côté
-- service, pas créer une deuxième ligne concurrente (même règle que
-- l'ancien serveur, src/lib/friends.ts).
CREATE TABLE friendships (
    id            TEXT NOT NULL PRIMARY KEY,
    requester_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    addressee_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status        TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'ACCEPTED')),
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (requester_id, addressee_id)
);
CREATE INDEX idx_friendships_addressee_id ON friendships(addressee_id);

-- Une ligne par écoute qualifiée (~30s), pas par lecture démarrée — la
-- validation du seuil reste côté client comme aujourd'hui.
CREATE TABLE play_events (
    id               TEXT NOT NULL PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    deezer_track_id  INTEGER NOT NULL,
    title            TEXT NOT NULL,
    artist_name      TEXT NOT NULL,
    album_title      TEXT NOT NULL,
    album_cover      TEXT NOT NULL,
    duration         INTEGER NOT NULL,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_play_events_user_id ON play_events(user_id);
CREATE INDEX idx_play_events_user_track ON play_events(user_id, deezer_track_id);

-- Cache global (pas par utilisateur) : un genre résolu une fois sert à tout
-- le monde. `deezer_track_id` est directement la clé primaire, pas un cuid.
CREATE TABLE track_genres (
    deezer_track_id INTEGER NOT NULL PRIMARY KEY,
    album_id        INTEGER,
    genre_id        INTEGER,
    genre_name      TEXT,
    source          TEXT CHECK (source IN ('lastfm', 'deezer')),
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE recommendations (
    id          TEXT NOT NULL PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    window      TEXT NOT NULL CHECK (window IN ('day', 'week', 'all')),
    payload     TEXT NOT NULL,
    computed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, window)
);

-- Activation par clé RSA (nouveau, anti-abus — voir docs/PLAN.md §5).
-- `code_hash` en clé primaire fait office de garde anti-rejeu atomique :
-- un INSERT en conflit rejette la tentative sans race check-then-insert.
CREATE TABLE activation_uses (
    code_hash  TEXT NOT NULL PRIMARY KEY,
    used_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_id    TEXT
);

-- +goose Down
DROP TABLE activation_uses;
DROP TABLE recommendations;
DROP TABLE track_genres;
DROP TABLE play_events;
DROP TABLE friendships;
DROP TABLE devices;
DROP TABLE user_settings;
DROP TABLE app_settings;
DROP TABLE playlist_tracks;
DROP TABLE playlists;
DROP TABLE liked_tracks;
DROP TABLE sessions;
DROP TABLE users;

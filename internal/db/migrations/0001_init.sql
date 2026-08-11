-- Placeholder for Phase 0, so goose/go:embed have a real migration to wire
-- up end to end. Phase 1 replaces the body of this file with the actual
-- schema (User, Session, LikedTrack, Playlist, ... — see schema.prisma on
-- the old server for the field-by-field reference, written freely for Go
-- since we're starting from an empty database).

-- +goose Up
CREATE TABLE schema_meta (
    key   TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE schema_meta;

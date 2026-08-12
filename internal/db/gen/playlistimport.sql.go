// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import "context"

// ListLikedTrackIDsAmong returns which of trackIDs the user has already
// liked — the "liked" import destination's dedup check. Callers must chunk
// trackIDs themselves for very large playlists (see LookupChunkSize in
// internal/playlists) — this issues one query for whatever slice it's
// given, with no chunking of its own.
func (q *Queries) ListLikedTrackIDsAmong(ctx context.Context, userID string, trackIDs []int64) (map[int64]bool, error) {
	result := make(map[int64]bool, len(trackIDs))
	if len(trackIDs) == 0 {
		return result, nil
	}

	placeholders := make([]byte, 0, len(trackIDs)*2)
	args := make([]any, 0, len(trackIDs)+1)
	args = append(args, userID)
	for i, id := range trackIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, id)
	}

	rows, err := q.db.QueryContext(ctx,
		"SELECT deezer_track_id FROM liked_tracks WHERE user_id = ? AND deezer_track_id IN ("+string(placeholders)+")",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

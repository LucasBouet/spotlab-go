-- name: RecordActivationUse :execrows
-- The PRIMARY KEY on code_hash is the whole replay guard: a second attempt
-- with the same code hits a UNIQUE conflict here and updates zero rows,
-- atomically, no separate check-then-insert race.
INSERT OR IGNORE INTO activation_uses (code_hash, user_id) VALUES (?, ?);

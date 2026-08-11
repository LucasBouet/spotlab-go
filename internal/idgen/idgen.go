// Package idgen generates the opaque string ids used for every row (User,
// Session excepted — its id is the token itself, see internal/auth).
//
// The old server used Prisma's cuid(); nothing in the wire contract cares
// about the id's shape (Android treats every id as an opaque string), so a
// plain UUIDv4 is used here instead rather than reimplementing cuid.
package idgen

import "github.com/google/uuid"

func New() string {
	return uuid.NewString()
}

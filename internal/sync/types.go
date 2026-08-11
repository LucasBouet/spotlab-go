// Package sync is the in-memory playback/jam engine and its HTTP surface
// (GET /api/sync/stream, POST /api/sync/command, POST /api/jam). It is the
// direct port of the old server's src/lib/playback-sync.ts — see docs/PLAN.md
// §3 for why a single actor goroutine, not per-room actors or a bare mutex.
package sync

// AddedByDTO / QueueItemDTO / JamMemberDTO / JamStateDTO / JamInviteDTO /
// PlaybackStateDTO mirror the Android client's SyncDto.kt exactly, field
// for field — see docs/PLAN.md's Android contract notes.

type AddedByDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// QueueItemDTO is used both on the wire and, as QueueItem's near-twin, in
// the reducer — see queue.go. AddedBy is nil for solo playback and must
// serialize as an *absent* key there, matching the old server's
// `addedBy?: {...}` — encoding/json already omits a nil pointer field only
// with `omitempty`, which is set here for exactly that reason.
type QueueItemDTO struct {
	ID       int64       `json:"id"`
	Title    string      `json:"title"`
	Artist   string      `json:"artist"`
	Album    string      `json:"album"`
	Cover    string      `json:"cover"`
	Duration int         `json:"duration"`
	UID      string      `json:"uid"`
	IsManual bool        `json:"isManual"`
	AddedBy  *AddedByDTO `json:"addedBy,omitempty"`
}

type JamMemberDTO struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	IsHost bool   `json:"isHost"`
	Online bool   `json:"online"`
}

type JamStateDTO struct {
	ID      string         `json:"id"`
	HostID  string         `json:"hostId"`
	Members []JamMemberDTO `json:"members"`
}

type JamInviteDTO struct {
	JamID       string `json:"jamId"`
	HostID      string `json:"hostId"`
	HostName    string `json:"hostName"`
	MemberCount int    `json:"memberCount"`
	CreatedAt   string `json:"createdAt"`
}

// PlaybackStateDTO is the canonical playback state as broadcast over SSE
// and returned by GET's snapshot. Room is the user id in solo mode, the
// jam id in a jam; Jam is non-nil only in the latter case.
type PlaybackStateDTO struct {
	Current           *QueueItemDTO  `json:"current"`
	Queue             []QueueItemDTO `json:"queue"`
	History           []QueueItemDTO `json:"history"`
	ContextTracks     []QueueItemDTO `json:"contextTracks"`
	ActiveContextID   *string        `json:"activeContextId"`
	Shuffle           bool           `json:"shuffle"`
	IsPlaying         bool           `json:"isPlaying"`
	PositionSeconds   float64        `json:"positionSeconds"`
	PositionUpdatedAt string         `json:"positionUpdatedAt"`
	ActiveDeviceIDs   []string       `json:"activeDeviceIds"`
	OriginDeviceID    *string        `json:"originDeviceId"`
	Revision          int64          `json:"revision"`
	Room              string         `json:"room"`
	Jam               *JamStateDTO   `json:"jam"`
}

// DeviceDTO mirrors devices.DeviceDTO's wire shape. Duplicated rather than
// imported: this package is a dependency of devices' wiring in main.go
// (Hub → BroadcastFunc/OnlineFunc), so devices importing sync back would
// cycle. Small enough that keeping the two in sync by inspection is fine.
type DeviceDTO struct {
	DeviceID   string `json:"deviceId"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	Online     bool   `json:"online"`
	LastSeenAt string `json:"lastSeenAt"`
}

// SyncSnapshotDTO is the `snapshot` SSE event, sent once on connect.
type SyncSnapshotDTO struct {
	Playback   PlaybackStateDTO `json:"playback"`
	Devices    []DeviceDTO      `json:"devices"`
	JamInvites []JamInviteDTO   `json:"jamInvites"`
}

// SyncActionDTO is every action POST /api/sync/command may carry, as one
// flat struct rather than 14 Go types + an unmarshal switch — it mirrors
// the wire shape directly: `{"type": "SEEK", "positionSeconds": 12.3}`.
// Pointers distinguish "field absent" from "field present at its zero
// value" exactly where that distinction matters (ShuffleOverride nil vs
// false); the others don't need it since their zero value is never a
// meaningful input on its own (a PLAY_TRACK with an all-zero Item is
// already nonsensical).
type SyncActionDTO struct {
	Type            string         `json:"type"`
	IsPlaying       bool           `json:"isPlaying,omitempty"`
	PositionSeconds float64        `json:"positionSeconds,omitempty"`
	DeviceIDs       []string       `json:"deviceIds,omitempty"`
	Item            *QueueItemDTO  `json:"item,omitempty"`
	ContextID       string         `json:"contextId,omitempty"`
	Items           []QueueItemDTO `json:"items,omitempty"`
	StartIndex      int            `json:"startIndex,omitempty"`
	ShuffleOverride *bool          `json:"shuffleOverride,omitempty"`
	UID             string         `json:"uid,omitempty"`
	FromIndex       int            `json:"fromIndex,omitempty"`
	ToIndex         int            `json:"toIndex,omitempty"`
}

// SyncCommandDTO is POST /api/sync/command's body.
type SyncCommandDTO struct {
	DeviceID string        `json:"deviceId"`
	Action   SyncActionDTO `json:"action"`
}

// SyncCommandResultDTO exists only so a client can reconcile revisions —
// the resulting state arrives over SSE, not here.
type SyncCommandResultDTO struct {
	OK       bool  `json:"ok"`
	Revision int64 `json:"revision"`
}

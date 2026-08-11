package sync

import (
	"math"
	"time"
)

// PlaybackState is the domain counterpart of PlaybackStateDTO — same
// shape as the old server's CanonicalPlaybackState, with PositionUpdatedAt
// kept as time.Time (not a wire string) so extrapolatePosition can do real
// time arithmetic. toDTO converts to the wire shape.
type PlaybackState struct {
	QueueState
	IsPlaying         bool
	PositionSeconds   float64
	PositionUpdatedAt time.Time
	ActiveDeviceIDs   []string
	OriginDeviceID    *string
	Revision          int64
}

func newPlaybackState(now time.Time) PlaybackState {
	return PlaybackState{
		QueueState:        initialQueueState(),
		ActiveDeviceIDs:   []string{},
		PositionUpdatedAt: now,
	}
}

// extrapolatePosition is extrapolatePosition in playback-position.ts,
// ported field for field. Not playing: the stored anchor is already the
// answer. Playing: add elapsed wall-clock time, clamped to [0, duration].
// duration is +Inf only when there is no current track at all — a current
// track with a genuinely zero duration clamps position to 0, exactly like
// the original (`state.current?.duration ?? null`: null only when
// `current` itself is absent, not when its duration happens to be 0).
func extrapolatePosition(state PlaybackState, at time.Time) float64 {
	if !state.IsPlaying {
		return state.PositionSeconds
	}
	elapsed := at.Sub(state.PositionUpdatedAt).Seconds()
	duration := math.Inf(1)
	if state.Current != nil {
		duration = float64(state.Current.Duration)
	}
	pos := state.PositionSeconds + elapsed
	if pos < 0 {
		pos = 0
	}
	if pos > duration {
		pos = duration
	}
	return pos
}

// resetPositionTypes are the action types that start a genuinely new
// playback position, as opposed to reshuffling/editing the queue around an
// unchanged current track — RESET_POSITION_TYPES in the old server.
var resetPositionTypes = map[string]bool{
	"PLAY_TRACK":      true,
	"PLAY_CONTEXT":    true,
	"PLAY_FROM_QUEUE": true,
	"SKIP_NEXT":       true,
	"SKIP_PREVIOUS":   true,
}

// nextPlaybackState is nextPlaybackState in playback-sync.ts, ported
// field for field: the pure transition shared by the solo and jam command
// paths. SET_ACTIVE_DEVICES is deliberately left to the caller (solo
// replaces the whole list, a jam merges per member) and passes through
// unchanged here, exactly like the original.
func nextPlaybackState(state PlaybackState, action SyncActionDTO, now time.Time) PlaybackState {
	switch action.Type {
	case "TOGGLE_PLAY":
		out := state
		out.IsPlaying = !state.IsPlaying
		out.PositionSeconds = extrapolatePosition(state, now)
		out.PositionUpdatedAt = now
		return out

	case "SET_PLAYING":
		out := state
		out.IsPlaying = action.IsPlaying
		out.PositionSeconds = extrapolatePosition(state, now)
		out.PositionUpdatedAt = now
		return out

	case "SEEK":
		out := state
		out.PositionSeconds = math.Max(0, action.PositionSeconds)
		out.PositionUpdatedAt = now
		return out

	case "SET_ACTIVE_DEVICES":
		return state

	default:
		queueNext := queueReducer(state.QueueState, action)
		next := state
		next.QueueState = queueNext
		if resetPositionTypes[action.Type] {
			next.IsPlaying = true
			next.PositionSeconds = 0
			next.PositionUpdatedAt = now
		}
		return next
	}
}

// withAddedBy stamps the member who queued a track onto the item(s) an
// action carries, so the jam roster/queue can show "ajouté par X" —
// withAddedBy in playback-sync.ts. Non item-bearing actions pass through
// untouched. Only ever called on the jam path; solo playback never sets
// AddedBy, which is what keeps it absent (not null) on the wire.
func withAddedBy(action SyncActionDTO, by AddedByDTO) SyncActionDTO {
	switch action.Type {
	case "PLAY_TRACK", "QUEUE_PLAY_NEXT", "QUEUE_ADD_TO_END":
		if action.Item == nil {
			return action
		}
		stamped := *action.Item
		stamped.AddedBy = &by
		action.Item = &stamped
		return action

	case "PLAY_CONTEXT":
		items := make([]QueueItemDTO, len(action.Items))
		for i, item := range action.Items {
			item.AddedBy = &by
			items[i] = item
		}
		action.Items = items
		return action

	default:
		return action
	}
}

// toDTO is toPlaybackDTO in playback-sync.ts.
func toDTO(state PlaybackState, room string, jam *JamStateDTO) PlaybackStateDTO {
	return PlaybackStateDTO{
		Current:           state.Current,
		Queue:             state.Queue,
		History:           state.History,
		ContextTracks:     state.ContextTracks,
		ActiveContextID:   state.ActiveContextID,
		Shuffle:           state.Shuffle,
		IsPlaying:         state.IsPlaying,
		PositionSeconds:   state.PositionSeconds,
		PositionUpdatedAt: state.PositionUpdatedAt.UTC().Format(time.RFC3339Nano),
		ActiveDeviceIDs:   state.ActiveDeviceIDs,
		OriginDeviceID:    state.OriginDeviceID,
		Revision:          state.Revision,
		Room:              room,
		Jam:               jam,
	}
}

package sync

import "math/rand"

// QueueState is a direct port of queue-reducer.ts's QueueState — same
// field names, same shape. Queue items are QueueItemDTO directly (no
// separate domain type): the JS original doesn't distinguish "wire" from
// "internal" for a queue item either, and inventing the distinction here
// would only add a translation layer with nothing to translate.
type QueueState struct {
	Current         *QueueItemDTO
	Queue           []QueueItemDTO
	History         []QueueItemDTO
	ContextTracks   []QueueItemDTO
	ActiveContextID *string
	Shuffle         bool
}

func initialQueueState() QueueState {
	return QueueState{
		Queue:         []QueueItemDTO{},
		History:       []QueueItemDTO{},
		ContextTracks: []QueueItemDTO{},
	}
}

// arrayMove is queue-reducer.ts's arrayMove, verified against it line by
// line: remove at fromIndex *first*, then insert into the now-shorter
// slice at toIndex — exactly Array.prototype.splice semantics. toIndex
// therefore addresses the list already missing the moved element; reading
// it any other way is off by one the moment a row moves down. Locked by
// TestArrayMove's table (queue_test.go).
//
// Out-of-range indices return items unchanged rather than panicking:
// JS's splice silently no-ops/clamps on an out-of-range index instead of
// throwing, and this is the actor's single command-processing goroutine —
// a panic here doesn't just fail one request, a naive top-level recover()
// still leaves the caller blocked forever on a reply that never gets
// sent (verified live: this exact gap deadlocked a test). Bounds-checking
// here, at the source, is the real fix; HTTP layer never claimed
// fromIndex/toIndex were validated against the current queue length
// anyway (see PlaybackRepository.kt's `moved()` on the Android side,
// which guards the identical case for the identical reason).
func arrayMove(items []QueueItemDTO, fromIndex, toIndex int) []QueueItemDTO {
	if fromIndex < 0 || fromIndex >= len(items) || toIndex < 0 || toIndex >= len(items) {
		return items
	}
	result := make([]QueueItemDTO, len(items))
	copy(result, items)

	moved := result[fromIndex]
	result = append(result[:fromIndex], result[fromIndex+1:]...)

	result = append(result[:toIndex], append([]QueueItemDTO{moved}, result[toIndex:]...)...)
	return result
}

// shuffleArray is a Fisher-Yates shuffle, ported from shuffleArray in
// queue-reducer.ts — same algorithm, so a shuffled queue's statistical
// properties match the web client's (not that anything currently compares
// them, but there's no reason to invent a different one).
func shuffleArray(items []QueueItemDTO) []QueueItemDTO {
	result := make([]QueueItemDTO, len(items))
	copy(result, items)
	for i := len(result) - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// queueReducer is a direct, field-for-field port of queueReducer in
// queue-reducer.ts. Every branch returns freshly built slices — never a
// mutated alias of the input — because Go struct assignment copies slice
// headers, not their backing arrays; an in-place mutation here would
// silently corrupt whatever else still held a reference to the old state
// (a broadcast frame already being written, `pendingQueue`-style call
// sites elsewhere).
func queueReducer(state QueueState, action SyncActionDTO) QueueState {
	switch action.Type {
	case "PLAY_TRACK":
		return QueueState{
			Current:       action.Item,
			Queue:         []QueueItemDTO{},
			History:       []QueueItemDTO{},
			ContextTracks: []QueueItemDTO{},
			Shuffle:       state.Shuffle,
		}

	case "PLAY_CONTEXT":
		items := action.Items
		startIndex := action.StartIndex
		if startIndex < 0 || startIndex >= len(items) {
			// A malformed/malicious startIndex must not crash the actor
			// (see arrayMove's doc comment for why bounds-checking at the
			// source beats relying on panic recovery). No sensible
			// "which track is current" answer exists for an out-of-range
			// index, so the command is treated as a no-op.
			return state
		}
		before := append([]QueueItemDTO{}, items[:startIndex]...)
		after := append([]QueueItemDTO{}, items[startIndex+1:]...)
		nextShuffle := state.Shuffle
		if action.ShuffleOverride != nil {
			nextShuffle = *action.ShuffleOverride
		}
		if nextShuffle {
			after = shuffleArray(after)
		}
		current := items[startIndex]
		contextID := action.ContextID
		return QueueState{
			Current:         &current,
			Queue:           after,
			History:         before,
			ContextTracks:   append([]QueueItemDTO{}, items...),
			ActiveContextID: &contextID,
			Shuffle:         nextShuffle,
		}

	case "SKIP_NEXT":
		if len(state.Queue) == 0 {
			return state
		}
		next := state.Queue[0]
		rest := append([]QueueItemDTO{}, state.Queue[1:]...)
		history := state.History
		if state.Current != nil {
			history = append(append([]QueueItemDTO{}, state.History...), *state.Current)
		}
		out := state
		out.Current = &next
		out.Queue = rest
		out.History = history
		return out

	case "SKIP_PREVIOUS":
		if len(state.History) == 0 {
			return state
		}
		prev := state.History[len(state.History)-1]
		out := state
		out.Current = &prev
		out.History = append([]QueueItemDTO{}, state.History[:len(state.History)-1]...)
		if state.Current != nil {
			out.Queue = append([]QueueItemDTO{*state.Current}, state.Queue...)
		} else {
			out.Queue = state.Queue
		}
		return out

	case "TOGGLE_SHUFFLE":
		nextShuffle := !state.Shuffle
		var manual []QueueItemDTO
		for _, item := range state.Queue {
			if item.IsManual {
				manual = append(manual, item)
			}
		}
		out := state
		out.Shuffle = nextShuffle
		if nextShuffle {
			var rest []QueueItemDTO
			for _, item := range state.Queue {
				if !item.IsManual {
					rest = append(rest, item)
				}
			}
			out.Queue = append(append([]QueueItemDTO{}, manual...), shuffleArray(rest)...)
			return out
		}
		played := map[string]bool{}
		for _, item := range state.History {
			played[item.UID] = true
		}
		if state.Current != nil {
			played[state.Current.UID] = true
		}
		var remaining []QueueItemDTO
		for _, item := range state.ContextTracks {
			if !played[item.UID] {
				remaining = append(remaining, item)
			}
		}
		out.Queue = append(append([]QueueItemDTO{}, manual...), remaining...)
		return out

	case "PLAY_FROM_QUEUE":
		index := -1
		for i, item := range state.Queue {
			if item.UID == action.UID {
				index = i
				break
			}
		}
		if index == -1 {
			return state
		}
		target := state.Queue[index]
		skipped := append([]QueueItemDTO{}, state.Queue[:index]...)
		rest := append([]QueueItemDTO{}, state.Queue[index+1:]...)
		var played []QueueItemDTO
		if state.Current != nil {
			played = append(append(append([]QueueItemDTO{}, state.History...), *state.Current), skipped...)
		} else {
			played = append(append([]QueueItemDTO{}, state.History...), skipped...)
		}
		out := state
		out.Current = &target
		out.Queue = rest
		out.History = played
		return out

	case "QUEUE_PLAY_NEXT":
		if action.Item == nil {
			// A malformed action claiming to queue a track while carrying
			// none — nothing sensible to do but treat it as a no-op,
			// never dereference a nil pointer.
			return state
		}
		out := state
		out.Queue = append([]QueueItemDTO{*action.Item}, state.Queue...)
		return out

	case "QUEUE_ADD_TO_END":
		if action.Item == nil {
			return state
		}
		out := state
		out.Queue = append(append([]QueueItemDTO{}, state.Queue...), *action.Item)
		return out

	case "REMOVE_FROM_QUEUE":
		var kept []QueueItemDTO
		for _, item := range state.Queue {
			if item.UID != action.UID {
				kept = append(kept, item)
			}
		}
		out := state
		out.Queue = kept
		return out

	case "REORDER_QUEUE":
		out := state
		out.Queue = arrayMove(state.Queue, action.FromIndex, action.ToIndex)
		return out

	default:
		return state
	}
}

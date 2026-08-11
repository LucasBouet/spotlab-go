package sync

import "time"

// heartbeatLocked is the 5s setInterval body in playback-sync.ts, ported
// field for field. Called directly from Run's ticker branch — already
// inside the actor goroutine at that point, so no command wrapper is
// needed the way every externally-triggered operation needs one.
//
// Two unrelated jobs share one tick because the original does: pure drift
// correction for anyone actively playing solo (re-broadcasts the
// extrapolated position without ever touching the stored anchor —
// positionSeconds/positionUpdatedAt only change on a real transport
// command), and reaping jams every one of whose members has been offline
// for longer than jamReapDuration.
func (h *Hub) heartbeatLocked(now time.Time) {
	for userID, state := range h.solo {
		if _, inJam := h.userJamID[userID]; inJam {
			// Frozen while in a jam — must never be pushed over the jam
			// they're actually hearing.
			continue
		}
		if !state.IsPlaying {
			continue
		}
		if len(h.conns[userID]) == 0 {
			continue
		}
		h.broadcastPlaybackLocked(userID, now)
	}

	var toReap []*jam
	for _, j := range h.jams {
		online := 0
		for _, memberID := range j.MemberOrder {
			if h.isUserOnlineLocked(memberID) {
				online++
			}
		}
		if online == 0 {
			if j.EmptySince == nil {
				t := now
				j.EmptySince = &t
			} else if now.Sub(*j.EmptySince) > jamReapDuration {
				toReap = append(toReap, j)
			}
			continue
		}
		j.EmptySince = nil
		if j.State.IsPlaying {
			h.broadcastJamLocked(j)
		}
	}
	for _, j := range toReap {
		h.endJamLocked(j)
	}
}

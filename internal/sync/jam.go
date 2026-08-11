package sync

import "time"

// jamMember / invitedUser / jam mirror the old server's JamMember/Jam types.
// All Hub-internal — never touched outside the actor goroutine.
type jamMember struct {
	Name      string
	DeviceIDs []string
}

type invitedUser struct {
	Name string
}

// jam mirrors the old server's Jam type, with one addition: MemberOrder.
// The original's `members` is a JS Map, which iterates in insertion
// order — and two behaviors depend on that order: jamStateDTO's `members`
// array, and leaveJam picking "the next member" as host
// (`jam.members.keys().next().value`) after the current host leaves. A Go
// map has no iteration order guarantee at all, so without tracking order
// explicitly, both would become nondeterministic — the member list would
// visibly reshuffle on every poll, and host transfer would hand the role
// to a random remaining member instead of the next-longest-present one.
type jam struct {
	ID          string
	HostID      string
	Members     map[string]*jamMember
	MemberOrder []string
	Invited     map[string]invitedUser
	State       PlaybackState
	CreatedAt   time.Time
	// EmptySince is the moment the jam last had zero online members, or
	// nil while at least one member holds a live connection. The
	// heartbeat reaps a jam once this has stood for jamReapDuration —
	// long enough to survive a brief mobile reconnect, short enough not
	// to leak abandoned rooms forever.
	EmptySince *time.Time
}

// addMember registers a member (or re-registers one already present,
// leaving its position in MemberOrder unchanged — only genuinely new
// members are appended).
func (j *jam) addMember(userID string, m *jamMember) {
	if _, exists := j.Members[userID]; !exists {
		j.MemberOrder = append(j.MemberOrder, userID)
	}
	j.Members[userID] = m
}

func (j *jam) removeMember(userID string) {
	delete(j.Members, userID)
	for i, id := range j.MemberOrder {
		if id == userID {
			j.MemberOrder = append(j.MemberOrder[:i], j.MemberOrder[i+1:]...)
			break
		}
	}
}

// jamStateDTO is jamStateDTO in playback-sync.ts. onlineFn reports
// whether a given user holds a live connection — passed in rather than
// read from Hub state directly so this stays a pure function callable
// from anywhere in the package without reaching into Hub internals.
func jamStateDTO(j *jam, onlineFn func(userID string) bool) JamStateDTO {
	members := make([]JamMemberDTO, 0, len(j.MemberOrder))
	for _, userID := range j.MemberOrder {
		member := j.Members[userID]
		members = append(members, JamMemberDTO{
			UserID: userID,
			Name:   member.Name,
			IsHost: userID == j.HostID,
			Online: onlineFn(userID),
		})
	}
	return JamStateDTO{ID: j.ID, HostID: j.HostID, Members: members}
}

// syncJamActiveDevices recomputes activeDeviceIds as the union of every
// member's chosen output devices — never mutated piecemeal, so it can
// never drift from the roster (e.g. after a member leaves). Matches
// syncJamActiveDevices exactly, including member iteration order (host
// first, then joins in order), though nothing currently depends on the
// resulting order beyond determinism itself.
func syncJamActiveDevices(j *jam) {
	var ids []string
	for _, userID := range j.MemberOrder {
		ids = append(ids, j.Members[userID].DeviceIDs...)
	}
	if ids == nil {
		ids = []string{}
	}
	j.State.ActiveDeviceIDs = ids
}

// cloneStateForJam is cloneStateForJam in playback-sync.ts: seeds a fresh
// jam room from a solo state, resetting the fields that are meaningless to
// carry over (outputs, origin, and — the detail that matters most, see
// docs/PLAN.md §3.2 — the revision counter, which starts a jam at 0
// regardless of how far the solo state's own counter had climbed).
func cloneStateForJam(source PlaybackState) PlaybackState {
	return PlaybackState{
		QueueState: QueueState{
			Current:         source.Current,
			Queue:           append([]QueueItemDTO{}, source.Queue...),
			History:         append([]QueueItemDTO{}, source.History...),
			ContextTracks:   append([]QueueItemDTO{}, source.ContextTracks...),
			ActiveContextID: source.ActiveContextID,
			Shuffle:         source.Shuffle,
		},
		IsPlaying:         source.IsPlaying,
		PositionSeconds:   source.PositionSeconds,
		PositionUpdatedAt: source.PositionUpdatedAt,
		ActiveDeviceIDs:   []string{},
		OriginDeviceID:    nil,
		Revision:          0,
	}
}

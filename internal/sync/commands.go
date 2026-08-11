package sync

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ------------------------------------------------------- playback commands

// applyCommandLocked is applyCommand in playback-sync.ts: dispatches to the
// jam or solo path depending on the user's current room.
func (h *Hub) applyCommandLocked(userID, deviceID string, action SyncActionDTO, now time.Time) PlaybackStateDTO {
	if j := h.userJamLocked(userID); j != nil {
		return h.applyJamCommandLocked(j, userID, deviceID, action, now)
	}
	return h.applySoloCommandLocked(userID, deviceID, action, now)
}

func (h *Hub) applySoloCommandLocked(userID, deviceID string, action SyncActionDTO, now time.Time) PlaybackStateDTO {
	state := h.getOrCreateStateLocked(userID, now)
	wasIdle := state.Current == nil

	var next PlaybackState
	if action.Type == "SET_ACTIVE_DEVICES" {
		next = *state
		next.ActiveDeviceIDs = action.DeviceIDs
	} else {
		next = nextPlaybackState(*state, action, now)
	}
	// A session starting from idle defaults to the originating device as
	// the sole output — preserves the single-device UX unless the user
	// opens Appareils and changes it.
	if wasIdle && next.Current != nil {
		next.ActiveDeviceIDs = []string{deviceID}
	}
	next.Revision = state.Revision + 1
	originDevice := deviceID
	next.OriginDeviceID = &originDevice

	h.solo[userID] = &next
	h.broadcastPlaybackLocked(userID, now)
	return toDTO(next, userID, nil)
}

func (h *Hub) applyJamCommandLocked(j *jam, userID, deviceID string, action SyncActionDTO, now time.Time) PlaybackStateDTO {
	member := j.Members[userID]

	if action.Type == "SET_ACTIVE_DEVICES" {
		// A member only ever sets their own output devices (ownership is
		// validated in the HTTP handler before this command is even
		// sent); the jam's activeDeviceIds is the union of all members'
		// choices, so the others' selections are preserved.
		if member != nil {
			member.DeviceIDs = action.DeviceIDs
		}
		syncJamActiveDevices(j)
		j.State.Revision++
		originDevice := deviceID
		j.State.OriginDeviceID = &originDevice
	} else {
		stamped := action
		if member != nil {
			stamped = withAddedBy(action, AddedByDTO{ID: userID, Name: member.Name})
		}
		next := nextPlaybackState(j.State, stamped, now)
		next.Revision = j.State.Revision + 1
		originDevice := deviceID
		next.OriginDeviceID = &originDevice
		j.State = next
		syncJamActiveDevices(j)
	}

	h.broadcastJamLocked(j)
	dto := jamStateDTO(j, h.isUserOnlineLocked)
	return toDTO(j.State, j.ID, &dto)
}

type applyCommandCmd struct {
	userID, deviceID string
	action           SyncActionDTO
	now              time.Time
	reply            chan PlaybackStateDTO
}

func (c *applyCommandCmd) apply(h *Hub) {
	c.reply <- h.applyCommandLocked(c.userID, c.deviceID, c.action, c.now)
}

func (c *applyCommandCmd) failReply() { c.reply <- PlaybackStateDTO{} }

// ApplyCommand is the server-side counterpart of the client's optimistic
// dispatch — POST /api/sync/command's handler calls this after validating
// SET_ACTIVE_DEVICES ownership against the DB (the one check this package
// cannot do itself, since the actor never touches the database).
func (h *Hub) ApplyCommand(ctx context.Context, userID, deviceID string, action SyncActionDTO) (PlaybackStateDTO, error) {
	reply := make(chan PlaybackStateDTO, 1)
	if err := h.send(ctx, &applyCommandCmd{userID, deviceID, action, time.Now(), reply}); err != nil {
		return PlaybackStateDTO{}, err
	}
	select {
	case dto := <-reply:
		return dto, nil
	case <-ctx.Done():
		return PlaybackStateDTO{}, ctx.Err()
	}
}

// --------------------------------------------------------- SSE connections

type subscribeCmd struct {
	userID, deviceID string
	devices          []DeviceDTO // pre-fetched from the DB by the caller; Online is overwritten here
	now              time.Time
	reply            chan subscribeResult
}

type subscribeResult struct {
	connID string
	out    chan []byte
	err    error
}

// apply registers the connection and computes+pushes the snapshot in the
// same atomic step, so no command processed afterward (guaranteed by
// channel ordering) can race ahead of the connecting client's first view
// of the state — see docs/PLAN.md §3.3.
func (c *subscribeCmd) apply(h *Hub) {
	connID := uuid.NewString()
	ch := make(chan []byte, 16)
	if h.conns[c.userID] == nil {
		h.conns[c.userID] = map[string]*conn{}
	}
	h.conns[c.userID][connID] = &conn{deviceID: c.deviceID, out: ch}

	devices := make([]DeviceDTO, len(c.devices))
	for i, d := range c.devices {
		d.Online = h.isOnlineLocked(c.userID, d.DeviceID)
		devices[i] = d
	}
	snapshot := SyncSnapshotDTO{
		Playback:   h.playbackDTOForLocked(c.userID, c.now),
		Devices:    devices,
		JamInvites: h.pendingInvitesForLocked(c.userID),
	}
	ch <- encodeSSE("snapshot", snapshot) // never blocks, buffer just opened

	c.reply <- subscribeResult{connID: connID, out: ch}
}

func (c *subscribeCmd) failReply() {
	c.reply <- subscribeResult{err: errors.New("erreur interne lors de la connexion")}
}

// Subscribe registers one SSE connection and returns its id (pass back to
// Unsubscribe on disconnect) and the channel of pre-encoded frames to
// write to the response as they arrive. devices is this account's device
// list, fetched by the caller (the actor never touches the DB) — Online
// is filled in here from this connection's own view of presence.
func (h *Hub) Subscribe(ctx context.Context, userID, deviceID string, devices []DeviceDTO) (connID string, out <-chan []byte, err error) {
	reply := make(chan subscribeResult, 1)
	if err := h.send(ctx, &subscribeCmd{userID, deviceID, devices, time.Now(), reply}); err != nil {
		return "", nil, err
	}
	select {
	case result := <-reply:
		if result.err != nil {
			return "", nil, result.err
		}
		return result.connID, result.out, nil
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
}

type unsubscribeCmd struct {
	userID, connID string
	done           chan struct{}
}

func (c *unsubscribeCmd) apply(h *Hub) {
	userConns := h.conns[c.userID]
	if userConns != nil {
		delete(userConns, c.connID)
		if len(userConns) == 0 {
			delete(h.conns, c.userID)
		}
	}
	close(c.done)
}

func (c *unsubscribeCmd) failReply() { close(c.done) }

// Unsubscribe removes one connection. Safe to call more than once or with
// an id that was already dropped (e.g. by sendEventLocked pruning a full
// buffer) — it's a no-op either way.
func (h *Hub) Unsubscribe(ctx context.Context, userID, connID string) {
	done := make(chan struct{})
	if err := h.send(ctx, &unsubscribeCmd{userID, connID, done}); err != nil {
		return
	}
	<-done
}

// ------------------------------------------------------------- presence

type isOnlineCmd struct {
	userID, deviceID string
	reply            chan bool
}

func (c *isOnlineCmd) apply(h *Hub) { c.reply <- h.isOnlineLocked(c.userID, c.deviceID) }

func (c *isOnlineCmd) failReply() { c.reply <- false }

// IsOnline reports whether a specific device holds a live connection —
// isOnline in the old server, used by the devices list to fill in each
// row's `online` flag.
func (h *Hub) IsOnline(ctx context.Context, userID, deviceID string) bool {
	reply := make(chan bool, 1)
	if err := h.send(ctx, &isOnlineCmd{userID, deviceID, reply}); err != nil {
		return false
	}
	select {
	case online := <-reply:
		return online
	case <-ctx.Done():
		return false
	}
}

// NowPlaying / TrackInfo are the Hub's presence answer for one user —
// activityFor in the old server, split into a Hub-only shape so this
// package never has to import the social package for one DTO (see
// types.go's DeviceDTO doc comment for the same reasoning). main.go's
// wiring converts this into social.FriendActivityDTO.
type TrackInfo struct{ Title, Artist, Cover string }
type NowPlaying struct {
	Online    bool
	IsPlaying bool
	Track     *TrackInfo
}

type activityCmd struct {
	userID string
	now    time.Time
	reply  chan NowPlaying
}

func (c *activityCmd) apply(h *Hub) {
	online := h.isUserOnlineLocked(c.userID)
	result := NowPlaying{Online: online}
	if !online {
		c.reply <- result
		return
	}
	// A user in a jam is playing the jam's track, not their frozen solo
	// state — matches getNowPlaying exactly.
	var state *PlaybackState
	if j := h.userJamLocked(c.userID); j != nil {
		state = &j.State
	} else {
		state = h.solo[c.userID]
	}
	if state != nil && state.Current != nil {
		result.IsPlaying = state.IsPlaying
		result.Track = &TrackInfo{Title: state.Current.Title, Artist: state.Current.Artist, Cover: state.Current.Cover}
	}
	c.reply <- result
}

func (c *activityCmd) failReply() { c.reply <- NowPlaying{} }

// Activity computes a user's live presence and now-playing state, for the
// social/friends view — the same in-memory state device sync uses, no
// extra bookkeeping and no DB.
func (h *Hub) Activity(ctx context.Context, userID string) NowPlaying {
	reply := make(chan NowPlaying, 1)
	if err := h.send(ctx, &activityCmd{userID, time.Now(), reply}); err != nil {
		return NowPlaying{}
	}
	select {
	case result := <-reply:
		return result
	case <-ctx.Done():
		return NowPlaying{}
	}
}

// ------------------------------------------------------------- devices

type broadcastDevicesCmd struct {
	userID  string
	devices []DeviceDTO
	done    chan struct{}
}

func (c *broadcastDevicesCmd) apply(h *Hub) {
	devices := make([]DeviceDTO, len(c.devices))
	for i, d := range c.devices {
		d.Online = h.isOnlineLocked(c.userID, d.DeviceID)
		devices[i] = d
	}
	h.sendEventLocked(c.userID, "devices", devices)
	close(c.done)
}

func (c *broadcastDevicesCmd) failReply() { close(c.done) }

// BroadcastDevices pushes the updated device roster to a user's connected
// devices — broadcastDevices in the old server. devices is the
// already-DB-fetched list (Online is filled in here); called after
// register/rename/forget, and on every SSE connect/disconnect.
func (h *Hub) BroadcastDevices(ctx context.Context, userID string, devices []DeviceDTO) {
	done := make(chan struct{})
	if err := h.send(ctx, &broadcastDevicesCmd{userID, devices, done}); err != nil {
		return
	}
	<-done
}

// ----------------------------------------------------------- jam lifecycle

var (
	ErrInviteNotFound = errors.New("Invitation introuvable.")
	ErrNoActiveJam    = errors.New("Aucune jam en cours.")
	ErrNotHost        = errors.New("Seul l'hôte peut arrêter la jam.")
)

func (h *Hub) createJamLocked(hostID, hostName, deviceID string, now time.Time) *jam {
	// Seeds from the host's current solo playback, so inviting friends
	// while already listening means "come hear what I'm playing" rather
	// than a blank room.
	j := &jam{
		ID:        uuid.NewString(),
		HostID:    hostID,
		Members:   map[string]*jamMember{},
		Invited:   map[string]invitedUser{},
		State:     cloneStateForJam(*h.getOrCreateStateLocked(hostID, now)),
		CreatedAt: now,
	}
	j.addMember(hostID, &jamMember{Name: hostName, DeviceIDs: []string{deviceID}})
	syncJamActiveDevices(j)
	h.jams[j.ID] = j
	h.userJamID[hostID] = j.ID
	return j
}

// endJamLocked tears a jam down: unlinks every member, drops it from the
// registry, and clears any dangling invites (notifying those users). Does
// NOT revert members' clients to solo — callers that end a jam for people
// still present do that themselves.
func (h *Hub) endJamLocked(j *jam) {
	for memberID := range j.Members {
		if h.userJamID[memberID] == j.ID {
			delete(h.userJamID, memberID)
		}
	}
	invitedIDs := make([]string, 0, len(j.Invited))
	for id := range j.Invited {
		invitedIDs = append(invitedIDs, id)
	}
	delete(h.jams, j.ID)
	for _, id := range invitedIDs {
		h.broadcastInvitesLocked(id)
	}
}

type inviteToJamResult struct {
	jamID string
	err   error
}

type inviteToJamCmd struct {
	inviterID, inviterName, deviceID string
	friendID, friendName             string
	now                              time.Time
	reply                            chan inviteToJamResult
}

func (c *inviteToJamCmd) apply(h *Hub) {
	j := h.userJamLocked(c.inviterID)
	if j == nil {
		j = h.createJamLocked(c.inviterID, c.inviterName, c.deviceID, c.now)
		// The inviter's own clients switch into the jam room now.
		h.broadcastJamLocked(j)
	}
	_, isMember := j.Members[c.friendID]
	_, isInvited := j.Invited[c.friendID]
	if !isMember && !isInvited {
		j.Invited[c.friendID] = invitedUser{Name: c.friendName}
		h.broadcastInvitesLocked(c.friendID)
	}
	c.reply <- inviteToJamResult{jamID: j.ID}
}

func (c *inviteToJamCmd) failReply() {
	c.reply <- inviteToJamResult{err: errors.New("erreur interne")}
}

// InviteToJam invites a friend into the inviter's jam, creating one
// (seeded from the inviter's current playback) if they aren't hosting yet.
// Any member may invite. Friendship is verified by the HTTP handler
// (needs the DB) before this is ever called.
func (h *Hub) InviteToJam(ctx context.Context, inviterID, inviterName, deviceID, friendID, friendName string) (string, error) {
	reply := make(chan inviteToJamResult, 1)
	if err := h.send(ctx, &inviteToJamCmd{inviterID, inviterName, deviceID, friendID, friendName, time.Now(), reply}); err != nil {
		return "", err
	}
	select {
	case result := <-reply:
		return result.jamID, result.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// leaveJamLocked is leaveJam in the old server: a member (including the
// host) removes themselves. When the host leaves, the role transfers to
// the longest-present remaining member (MemberOrder[0] — see jam.go's doc
// comment on why this has to be tracked explicitly in Go); when the last
// member leaves, the jam ends.
func (h *Hub) leaveJamLocked(userID string, now time.Time) bool {
	j := h.userJamLocked(userID)
	if j == nil {
		return false
	}

	j.removeMember(userID)
	delete(h.userJamID, userID)
	// Reverts the leaver's own connections to their preserved solo state.
	h.broadcastPlaybackLocked(userID, now)

	if len(j.Members) == 0 {
		h.endJamLocked(j)
		return true
	}
	if j.HostID == userID {
		j.HostID = j.MemberOrder[0]
	}
	syncJamActiveDevices(j)
	// Bumped so remaining members apply the smaller roster / new host
	// rather than dropping this as a same-revision duplicate.
	j.State.Revision++
	h.broadcastJamLocked(j)
	return true
}

type acceptJamInviteCmd struct {
	jamID, userID, userName, deviceID string
	now                               time.Time
	reply                             chan error
}

func (c *acceptJamInviteCmd) apply(h *Hub) {
	j, ok := h.jams[c.jamID]
	if !ok {
		c.reply <- ErrInviteNotFound
		return
	}
	if _, invited := j.Invited[c.userID]; !invited {
		c.reply <- ErrInviteNotFound
		return
	}

	// Accepting an invite while already in another jam leaves that one first.
	if existing := h.userJamLocked(c.userID); existing != nil && existing.ID != c.jamID {
		h.leaveJamLocked(c.userID, c.now)
	}

	delete(j.Invited, c.userID)
	j.addMember(c.userID, &jamMember{Name: c.userName, DeviceIDs: []string{c.deviceID}})
	h.userJamID[c.userID] = j.ID
	j.EmptySince = nil
	syncJamActiveDevices(j)
	// Bumped so existing members (whose room is unchanged) actually apply
	// this broadcast and see the new member + updated devices, instead of
	// dropping it as a same-revision duplicate.
	j.State.Revision++
	// Pushes the room switch + updated roster to everyone, including the
	// joiner (their client resets its revision on the new room and starts
	// playing in sync from the shared position).
	h.broadcastJamLocked(j)
	h.broadcastInvitesLocked(c.userID)
	c.reply <- nil
}

func (c *acceptJamInviteCmd) failReply() { c.reply <- errors.New("erreur interne") }

func (h *Hub) AcceptJamInvite(ctx context.Context, jamID, userID, userName, deviceID string) error {
	reply := make(chan error, 1)
	if err := h.send(ctx, &acceptJamInviteCmd{jamID, userID, userName, deviceID, time.Now(), reply}); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type declineJamInviteCmd struct {
	jamID, userID string
	done          chan struct{}
}

func (c *declineJamInviteCmd) apply(h *Hub) {
	if j, ok := h.jams[c.jamID]; ok {
		if _, invited := j.Invited[c.userID]; invited {
			delete(j.Invited, c.userID)
			h.broadcastInvitesLocked(c.userID)
		}
	}
	close(c.done)
}

func (c *declineJamInviteCmd) failReply() { close(c.done) }

func (h *Hub) DeclineJamInvite(ctx context.Context, jamID, userID string) {
	done := make(chan struct{})
	if err := h.send(ctx, &declineJamInviteCmd{jamID, userID, done}); err != nil {
		return
	}
	<-done
}

type leaveJamCmd struct {
	userID string
	now    time.Time
	done   chan struct{}
}

func (c *leaveJamCmd) apply(h *Hub) {
	h.leaveJamLocked(c.userID, c.now)
	close(c.done)
}

func (c *leaveJamCmd) failReply() { close(c.done) }

// LeaveJam always "succeeds" even if the caller wasn't in a jam at all —
// matches the old server exactly, which is why it's unusable as a "was I
// in a jam" probe (see docs/PLAN.md's known pitfalls).
func (h *Hub) LeaveJam(ctx context.Context, userID string) {
	done := make(chan struct{})
	if err := h.send(ctx, &leaveJamCmd{userID, time.Now(), done}); err != nil {
		return
	}
	<-done
}

type stopJamCmd struct {
	userID string
	now    time.Time
	reply  chan error
}

func (c *stopJamCmd) apply(h *Hub) {
	j := h.userJamLocked(c.userID)
	if j == nil {
		c.reply <- ErrNoActiveJam
		return
	}
	if j.HostID != c.userID {
		c.reply <- ErrNotHost
		return
	}
	memberIDs := append([]string{}, j.MemberOrder...)
	h.endJamLocked(j)
	for _, memberID := range memberIDs {
		h.broadcastPlaybackLocked(memberID, c.now)
	}
	c.reply <- nil
}

func (c *stopJamCmd) failReply() { c.reply <- errors.New("erreur interne") }

// StopJam ends the jam for everyone; each member reverts to their solo
// state. Host only.
func (h *Hub) StopJam(ctx context.Context, userID string) error {
	reply := make(chan error, 1)
	if err := h.send(ctx, &stopJamCmd{userID, time.Now(), reply}); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

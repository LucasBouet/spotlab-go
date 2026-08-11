package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// jamReapDuration mirrors JAM_REAP_MS.
const jamReapDuration = 120 * time.Second

// heartbeatInterval mirrors the 5s setInterval in playback-sync.ts.
const heartbeatInterval = 5 * time.Second

// conn is one open SSE connection. Keyed by a per-connection id (not by
// deviceId) for the same reason as the old server: a device's second live
// connection (a reconnect whose old socket hasn't been torn down yet) must
// not be able to overwrite and later delete the *active* one. A device
// counts as online while any of its connections lives.
type conn struct {
	deviceID string
	out      chan []byte
}

// command is the single serialization point every state mutation goes
// through — see docs/PLAN.md §3.1 for why one actor rather than per-room
// actors or a bare mutex.
type command interface{ apply(h *Hub) }

// failer is implemented by every command whose apply() sends a reply the
// caller blocks on. If apply() panics before reaching that send — bounds
// checks in queue.go catch the known cases, but this is the guarantee for
// every case nobody has thought of yet — failReply is what unblocks the
// caller instead of leaving it waiting forever on a value that will never
// arrive. Caught live: a first version of this Hub recovered the actor
// from a panic and logged it, but the calling goroutine (and the test
// calling it) hung permanently, because recovering the actor and
// unblocking its caller are two different guarantees and only the first
// one was implemented.
type failer interface{ failReply() }

// Hub owns every piece of in-memory state playback-sync.ts held at module
// level: solo playback per user, jams, and live SSE connections. Nothing
// outside the goroutine running Run ever touches these maps directly —
// every read and write is a command processed one at a time, in arrival
// order, which is what makes this safe without a mutex (and provably so
// under `go test -race`, which the original's single-threaded-JS argument
// could never offer).
type Hub struct {
	cmds   chan command
	logger *slog.Logger

	solo      map[string]*PlaybackState
	jams      map[string]*jam
	userJamID map[string]string
	conns     map[string]map[string]*conn
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		cmds:      make(chan command, 64),
		logger:    logger,
		solo:      make(map[string]*PlaybackState),
		jams:      make(map[string]*jam),
		userJamID: make(map[string]string),
		conns:     make(map[string]map[string]*conn),
	}
}

// Run processes commands until ctx is cancelled. Must be started exactly
// once, in its own goroutine, before any command is sent.
func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case cmd := <-h.cmds:
			h.apply(cmd)
		case now := <-ticker.C:
			h.heartbeatLocked(now)
		case <-ctx.Done():
			return
		}
	}
}

// apply runs one command with a panic guard: a malformed action (e.g. an
// out-of-range PLAY_CONTEXT startIndex) must degrade to "this one command
// had no effect", never take down the actor goroutine and, with it, sync
// for every user on the server.
func (h *Hub) apply(cmd command) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("commande sync récupérée après panique", "error", fmt.Sprint(r))
			if f, ok := cmd.(failer); ok {
				f.failReply()
			}
		}
	}()
	cmd.apply(h)
}

// send delivers cmd to the actor and blocks until ctx is done or the
// channel accepts it. The channel is buffered (64) so a burst of commands
// from several devices never has to synchronously wait on the actor
// draining one at a time before the HTTP handler can even hand off.
func (h *Hub) send(ctx context.Context, cmd command) error {
	select {
	case h.cmds <- cmd:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --------------------------------------------------------------- lookups

func (h *Hub) getOrCreateStateLocked(userID string, now time.Time) *PlaybackState {
	state, ok := h.solo[userID]
	if !ok {
		fresh := newPlaybackState(now)
		state = &fresh
		h.solo[userID] = state
	}
	return state
}

// userJamLocked self-heals a dangling index entry (jam already reaped) —
// getUserJam in the original.
func (h *Hub) userJamLocked(userID string) *jam {
	jamID, ok := h.userJamID[userID]
	if !ok {
		return nil
	}
	j, ok := h.jams[jamID]
	if !ok {
		delete(h.userJamID, userID)
		return nil
	}
	return j
}

func (h *Hub) isOnlineLocked(userID, deviceID string) bool {
	for _, c := range h.conns[userID] {
		if c.deviceID == deviceID {
			return true
		}
	}
	return false
}

func (h *Hub) isUserOnlineLocked(userID string) bool {
	return len(h.conns[userID]) > 0
}

// pendingInvitesForLocked is pendingInvitesFor in playback-sync.ts.
func (h *Hub) pendingInvitesForLocked(userID string) []JamInviteDTO {
	invites := []JamInviteDTO{}
	for _, j := range h.jams {
		if _, ok := j.Invited[userID]; !ok {
			continue
		}
		hostName := "Un ami"
		if host, ok := j.Members[j.HostID]; ok {
			hostName = host.Name
		}
		invites = append(invites, JamInviteDTO{
			JamID:       j.ID,
			HostID:      j.HostID,
			HostName:    hostName,
			MemberCount: len(j.Members),
			CreatedAt:   j.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return invites
}

// playbackDTOForLocked is playbackDTOFor in playback-sync.ts: a jam's
// shared state (with roster) if the user is in one, otherwise their own
// solo state.
func (h *Hub) playbackDTOForLocked(userID string, now time.Time) PlaybackStateDTO {
	if j := h.userJamLocked(userID); j != nil {
		dto := jamStateDTO(j, h.isUserOnlineLocked)
		return toDTO(j.State, j.ID, &dto)
	}
	return toDTO(*h.getOrCreateStateLocked(userID, now), userID, nil)
}

// ------------------------------------------------------------ broadcasts

func encodeSSE(event string, data any) []byte {
	body, err := json.Marshal(data)
	if err != nil {
		// A DTO that fails to marshal is a programmer error (an
		// unsupported field type), not a runtime condition to recover
		// from silently — but the actor must never panic on it, so this
		// frame is simply dropped rather than sent malformed.
		body = []byte("null")
	}
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event, body)
}

// sendEventLocked fans an event out to every live connection for a user.
// A connection whose buffered channel is full (a slow/dead client) is
// dropped immediately rather than blocking the actor — the non-blocking
// equivalent of the original's try/catch-then-prune, see docs/PLAN.md §3.3.
func (h *Hub) sendEventLocked(userID, event string, data any) {
	userConns := h.conns[userID]
	if len(userConns) == 0 {
		return
	}
	frame := encodeSSE(event, data)
	for id, c := range userConns {
		select {
		case c.out <- frame:
		default:
			delete(userConns, id)
			close(c.out)
		}
	}
}

func (h *Hub) broadcastJamLocked(j *jam) {
	dto := jamStateDTO(j, h.isUserOnlineLocked)
	payload := toDTO(j.State, j.ID, &dto)
	for memberID := range j.Members {
		h.sendEventLocked(memberID, "playback", payload)
	}
}

// broadcastPlaybackLocked is broadcastPlayback in playback-sync.ts.
func (h *Hub) broadcastPlaybackLocked(userID string, now time.Time) {
	if j := h.userJamLocked(userID); j != nil {
		h.broadcastJamLocked(j)
		return
	}
	state := h.getOrCreateStateLocked(userID, now)
	h.sendEventLocked(userID, "playback", toDTO(*state, userID, nil))
}

func (h *Hub) broadcastInvitesLocked(userID string) {
	h.sendEventLocked(userID, "jam-invites", h.pendingInvitesForLocked(userID))
}

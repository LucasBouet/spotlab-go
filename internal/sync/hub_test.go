package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func newTestHub(t *testing.T) (*Hub, context.Context) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := NewHub(logger)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	return hub, ctx
}

// TestApplyCommandConcurrentIsRace-free is the test docs/PLAN.md §7
// specifically asks for: fire commands from many goroutines at once,
// assert the final revision matches the count sent, and run it under
// `go test -race`. A per-room actor or a bare mutex could both pass this
// by luck; what actually proves the design is no goroutine ever seeing a
// torn/interleaved write, which -race checks structurally rather than by
// assertion.
func TestApplyCommandConcurrentIsRaceFree(t *testing.T) {
	hub, ctx := newTestHub(t)
	const n = 200

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := hub.ApplyCommand(ctx, "user1", "dev1", SyncActionDTO{Type: "TOGGLE_PLAY"})
			if err != nil {
				t.Errorf("ApplyCommand: %v", err)
			}
		}()
	}
	wg.Wait()

	final, err := hub.ApplyCommand(ctx, "user1", "dev1", SyncActionDTO{Type: "SEEK", PositionSeconds: 1})
	if err != nil {
		t.Fatalf("ApplyCommand final: %v", err)
	}
	if final.Revision != n+1 {
		t.Errorf("Revision = %d, attendu %d — chaque commande doit incrémenter exactement une fois, sans en perdre ni en dupliquer sous concurrence", final.Revision, n+1)
	}
}

func TestApplyCommandStartsFromIdleSetsSoleOutput(t *testing.T) {
	hub, ctx := newTestHub(t)
	state, err := hub.ApplyCommand(ctx, "u1", "dev1", SyncActionDTO{
		Type: "PLAY_TRACK", Item: &QueueItemDTO{UID: "x", Duration: 200},
	})
	if err != nil {
		t.Fatalf("ApplyCommand: %v", err)
	}
	if len(state.ActiveDeviceIDs) != 1 || state.ActiveDeviceIDs[0] != "dev1" {
		t.Errorf("ActiveDeviceIDs = %v, attendu [dev1] — démarrer depuis l'inactivité doit désigner l'appareil d'origine", state.ActiveDeviceIDs)
	}
}

func TestApplyCommandResumingFromExistingQueueDoesNotStealOutput(t *testing.T) {
	hub, ctx := newTestHub(t)
	if _, err := hub.ApplyCommand(ctx, "u1", "dev1", SyncActionDTO{
		Type: "PLAY_TRACK", Item: &QueueItemDTO{UID: "x", Duration: 200},
	}); err != nil {
		t.Fatalf("PLAY_TRACK: %v", err)
	}
	// A second device sends a command while something is already playing —
	// must NOT silently reassign the output to itself.
	state, err := hub.ApplyCommand(ctx, "u1", "dev2", SyncActionDTO{Type: "TOGGLE_PLAY"})
	if err != nil {
		t.Fatalf("TOGGLE_PLAY: %v", err)
	}
	if len(state.ActiveDeviceIDs) != 1 || state.ActiveDeviceIDs[0] != "dev1" {
		t.Errorf("ActiveDeviceIDs = %v, attendu [dev1] inchangé — wasIdle est faux ici", state.ActiveDeviceIDs)
	}
}

func TestSubscribeDeliversSnapshotFirst(t *testing.T) {
	hub, ctx := newTestHub(t)
	_, out, err := hub.Subscribe(ctx, "u1", "dev1", nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	select {
	case frame := <-out:
		if !containsEvent(frame, "snapshot") {
			t.Errorf("premier cadre = %s, attendu un événement snapshot", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout en attendant le snapshot")
	}
}

func TestSubscribeSnapshotDeviceOnlineReflectsThisConnection(t *testing.T) {
	hub, ctx := newTestHub(t)
	devices := []DeviceDTO{{DeviceID: "dev1", Name: "Phone"}}
	_, out, err := hub.Subscribe(ctx, "u1", "dev1", devices)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	frame := <-out
	var snap struct {
		Devices []DeviceDTO `json:"devices"`
	}
	decodeFrame(t, frame, &snap)
	if len(snap.Devices) != 1 || !snap.Devices[0].Online {
		t.Errorf("Devices = %+v, l'appareil qui vient de se connecter doit apparaître en ligne", snap.Devices)
	}
}

func TestUnsubscribeStopsFurtherDelivery(t *testing.T) {
	hub, ctx := newTestHub(t)
	connID, out, err := hub.Subscribe(ctx, "u1", "dev1", nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	<-out // drain the snapshot

	hub.Unsubscribe(ctx, "u1", connID)
	if hub.IsOnline(ctx, "u1", "dev1") {
		t.Error("après Unsubscribe, IsOnline doit être false")
	}

	if _, err := hub.ApplyCommand(ctx, "u1", "dev1", SyncActionDTO{Type: "TOGGLE_PLAY"}); err != nil {
		t.Fatalf("ApplyCommand: %v", err)
	}
	select {
	case frame, ok := <-out:
		if ok {
			t.Errorf("une connexion désabonnée ne doit plus rien recevoir, a reçu %s", frame)
		}
	case <-time.After(100 * time.Millisecond):
		// no frame arrived — correct
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	hub, ctx := newTestHub(t)
	connID, _, err := hub.Subscribe(ctx, "u1", "dev1", nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	hub.Unsubscribe(ctx, "u1", connID)
	hub.Unsubscribe(ctx, "u1", connID) // must not panic or hang
	hub.Unsubscribe(ctx, "u1", "never-existed")
}

func TestBroadcastDropsSlowConnectionRatherThanBlocking(t *testing.T) {
	hub, ctx := newTestHub(t)
	_, out, err := hub.Subscribe(ctx, "u1", "dev1", nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	<-out // drain snapshot, buffer now empty (cap 16)

	// Flood past the connection's buffer without ever reading it — the
	// actor must keep processing other commands instead of blocking on a
	// full channel.
	for i := 0; i < 32; i++ {
		if _, err := hub.ApplyCommand(ctx, "u1", "dev1", SyncActionDTO{Type: "TOGGLE_PLAY"}); err != nil {
			t.Fatalf("ApplyCommand #%d: %v", i, err)
		}
	}

	// The Hub must still be responsive to a brand new, unrelated user.
	done := make(chan struct{})
	go func() {
		hub.ApplyCommand(ctx, "u2", "dev2", SyncActionDTO{Type: "TOGGLE_PLAY"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("l'acteur semble bloqué par une connexion lente/pleine")
	}
}

func TestPanicInOneCommandDoesNotTakeDownTheActor(t *testing.T) {
	hub, ctx := newTestHub(t)
	// PLAY_CONTEXT with a startIndex out of range panics inside the pure
	// reducer (a slice out-of-bounds) — the actor must survive it and keep
	// serving every other command afterward.
	_, _ = hub.ApplyCommand(ctx, "u1", "dev1", SyncActionDTO{
		Type: "PLAY_CONTEXT", Items: []QueueItemDTO{{UID: "a"}}, StartIndex: 99,
	})

	state, err := hub.ApplyCommand(ctx, "u1", "dev1", SyncActionDTO{Type: "TOGGLE_PLAY"})
	if err != nil {
		t.Fatalf("l'acteur n'a pas survécu à la panique précédente: %v", err)
	}
	if state.Revision == 0 {
		t.Error("une commande normale après la panique devrait tout de même s'appliquer")
	}
}

// --- test helpers ---

func containsEvent(frame []byte, event string) bool {
	return len(frame) > 7 && string(frame[7:7+len(event)]) == event
}

func decodeFrame(t *testing.T, frame []byte, v any) {
	t.Helper()
	// frame is "event: X\ndata: {...}\n\n" — the JSON payload follows "data: ".
	const marker = "data: "
	i := bytes.Index(frame, []byte(marker))
	if i == -1 {
		t.Fatalf("cadre SSE sans section data: %s", frame)
	}
	if err := json.Unmarshal(frame[i+len(marker):], v); err != nil {
		t.Fatalf("décodage du cadre: %v (%s)", err, frame)
	}
}

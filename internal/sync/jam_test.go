package sync

import (
	"errors"
	"testing"
	"time"
)

func TestInviteToJamCreatesAJamSeededFromInviterPlayback(t *testing.T) {
	hub, ctx := newTestHub(t)
	if _, err := hub.ApplyCommand(ctx, "host", "hostDev", SyncActionDTO{
		Type: "PLAY_TRACK", Item: &QueueItemDTO{UID: "x", Title: "Now Playing", Duration: 200},
	}); err != nil {
		t.Fatalf("PLAY_TRACK: %v", err)
	}

	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "friend", "Friend")
	if err != nil {
		t.Fatalf("InviteToJam: %v", err)
	}
	if jamID == "" {
		t.Fatal("jamID vide")
	}

	// The host's own room switched to the jam, seeded from what they were
	// playing solo.
	state, err := hub.ApplyCommand(ctx, "host", "hostDev", SyncActionDTO{Type: "TOGGLE_PLAY"})
	if err != nil {
		t.Fatalf("ApplyCommand: %v", err)
	}
	if state.Room != jamID {
		t.Errorf("Room = %q, attendu %q", state.Room, jamID)
	}
	if state.Current == nil || state.Current.Title != "Now Playing" {
		t.Errorf("Current = %v, la jam doit être scellée depuis la lecture solo de l'hôte", state.Current)
	}
}

// TestJoiningAndLeavingAJamBothArriveAtRevisionZero is the load-bearing
// assertion in this whole package — see docs/PLAN.md §3.2. Verified live
// against the reference server before this port existed; this test locks
// that measurement in as a regression guard.
func TestJoiningAndLeavingAJamBothArriveAtRevisionZero(t *testing.T) {
	hub, ctx := newTestHub(t)

	// Climb the host's solo revision well above 0 before ever jamming.
	for i := 0; i < 5; i++ {
		if _, err := hub.ApplyCommand(ctx, "host", "hostDev", SyncActionDTO{Type: "TOGGLE_PLAY"}); err != nil {
			t.Fatalf("warm-up #%d: %v", i, err)
		}
	}

	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "friend", "Friend")
	if err != nil {
		t.Fatalf("InviteToJam: %v", err)
	}
	if err := hub.AcceptJamInvite(ctx, jamID, "friend", "Friend", "friendDev"); err != nil {
		t.Fatalf("AcceptJamInvite: %v", err)
	}

	// A fresh jam clone starts at 0; AcceptJamInvite bumps it once (so
	// existing members apply the broadcast rather than dropping it as a
	// same-revision duplicate), landing at 1. The TOGGLE_PLAY just below
	// bumps it once more, to 2 — the point under test is that this is
	// nowhere near the host's pre-jam revision (5), which a naive "just
	// carry the counter over" port would have produced instead.
	joined, err := hub.ApplyCommand(ctx, "friend", "friendDev", SyncActionDTO{Type: "TOGGLE_PLAY"})
	if err != nil {
		t.Fatalf("ApplyCommand in jam: %v", err)
	}
	if joined.Revision != 2 {
		t.Errorf("Revision = %d, attendu 2 (0 au clonage, +1 à l'acceptation, +1 à cette commande)", joined.Revision)
	}

	hub.LeaveJam(ctx, "friend")
	solo, err := hub.ApplyCommand(ctx, "friend", "friendDev", SyncActionDTO{Type: "TOGGLE_PLAY"})
	if err != nil {
		t.Fatalf("ApplyCommand after leave: %v", err)
	}
	if solo.Room == jamID {
		t.Fatal("après avoir quitté, la salle ne doit plus être celle de la jam")
	}
	if solo.Revision >= 5 {
		t.Errorf("Revision après retour au solo = %d, un nouveau compte 'friend' n'avait jamais atteint 5", solo.Revision)
	}
}

func TestAcceptJamInviteRejectsUnknownJam(t *testing.T) {
	hub, ctx := newTestHub(t)
	err := hub.AcceptJamInvite(ctx, "no-such-jam", "u1", "U1", "dev1")
	if !errors.Is(err, ErrInviteNotFound) {
		t.Errorf("err = %v, attendu ErrInviteNotFound", err)
	}
}

func TestAcceptJamInviteRejectsWithoutAPendingInvite(t *testing.T) {
	hub, ctx := newTestHub(t)
	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "friend", "Friend")
	if err != nil {
		t.Fatalf("InviteToJam: %v", err)
	}
	// "other" was never invited.
	err = hub.AcceptJamInvite(ctx, jamID, "other", "Other", "otherDev")
	if !errors.Is(err, ErrInviteNotFound) {
		t.Errorf("err = %v, attendu ErrInviteNotFound", err)
	}
}

func TestAcceptingAnInviteLeavesTheCurrentJamFirst(t *testing.T) {
	hub, ctx := newTestHub(t)
	jamA, err := hub.InviteToJam(ctx, "hostA", "HostA", "devA", "roamer", "Roamer")
	if err != nil {
		t.Fatalf("invite A: %v", err)
	}
	jamB, err := hub.InviteToJam(ctx, "hostB", "HostB", "devB", "roamer", "Roamer")
	if err != nil {
		t.Fatalf("invite B: %v", err)
	}

	if err := hub.AcceptJamInvite(ctx, jamA, "roamer", "Roamer", "roamerDev"); err != nil {
		t.Fatalf("accept A: %v", err)
	}
	if err := hub.AcceptJamInvite(ctx, jamB, "roamer", "Roamer", "roamerDev"); err != nil {
		t.Fatalf("accept B: %v", err)
	}

	state, err := hub.ApplyCommand(ctx, "roamer", "roamerDev", SyncActionDTO{Type: "TOGGLE_PLAY"})
	if err != nil {
		t.Fatalf("ApplyCommand: %v", err)
	}
	if state.Room != jamB {
		t.Errorf("Room = %q, attendu %q — accepter une deuxième invitation doit quitter la première jam", state.Room, jamB)
	}
}

func TestDeclineJamInviteRemovesTheInviteWithoutJoining(t *testing.T) {
	hub, ctx := newTestHub(t)
	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "friend", "Friend")
	if err != nil {
		t.Fatalf("InviteToJam: %v", err)
	}
	hub.DeclineJamInvite(ctx, jamID, "friend")

	// Now that the invite is gone, accepting it must fail.
	err = hub.AcceptJamInvite(ctx, jamID, "friend", "Friend", "friendDev")
	if !errors.Is(err, ErrInviteNotFound) {
		t.Errorf("err = %v, attendu ErrInviteNotFound après refus", err)
	}
}

func TestDeclineJamInviteOnUnknownJamIsANoOp(t *testing.T) {
	hub, ctx := newTestHub(t)
	hub.DeclineJamInvite(ctx, "no-such-jam", "u1") // must not panic or hang
}

func TestLeaveJamTransfersHostToLongestPresentMember(t *testing.T) {
	hub, ctx := newTestHub(t)
	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "second", "Second")
	if err != nil {
		t.Fatalf("invite second: %v", err)
	}
	if err := hub.AcceptJamInvite(ctx, jamID, "second", "Second", "secondDev"); err != nil {
		t.Fatalf("accept second: %v", err)
	}
	if _, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "third", "Third"); err != nil {
		t.Fatalf("invite third: %v", err)
	}
	if err := hub.AcceptJamInvite(ctx, jamID, "third", "Third", "thirdDev"); err != nil {
		t.Fatalf("accept third: %v", err)
	}

	hub.LeaveJam(ctx, "host")

	state, err := hub.ApplyCommand(ctx, "second", "secondDev", SyncActionDTO{Type: "TOGGLE_PLAY"})
	if err != nil {
		t.Fatalf("ApplyCommand: %v", err)
	}
	if state.Jam == nil {
		t.Fatal("Jam ne devrait pas être nil, second et third sont encore membres")
	}
	if state.Jam.HostID != "second" {
		t.Errorf("HostID = %q, attendu second (le plus ancien membre restant, pas third)", state.Jam.HostID)
	}
}

func TestLeaveJamEndsItWhenTheLastMemberLeaves(t *testing.T) {
	hub, ctx := newTestHub(t)
	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "friend", "Friend")
	if err != nil {
		t.Fatalf("InviteToJam: %v", err)
	}

	hub.LeaveJam(ctx, "host")

	// The jam should no longer exist — a fresh invite must be able to
	// reuse a friend relationship without colliding on jamID reuse (a
	// weak check, but AcceptJamInvite on the old id must now fail).
	err = hub.AcceptJamInvite(ctx, jamID, "friend", "Friend", "friendDev")
	if !errors.Is(err, ErrInviteNotFound) {
		t.Errorf("err = %v, attendu ErrInviteNotFound — la jam doit avoir disparu avec son unique membre", err)
	}
}

func TestLeaveJamOnANonMemberIsANoOp(t *testing.T) {
	hub, ctx := newTestHub(t)
	hub.LeaveJam(ctx, "never-in-a-jam") // must not panic or hang
}

func TestStopJamRevertsEveryMemberToSolo(t *testing.T) {
	hub, ctx := newTestHub(t)
	if _, err := hub.ApplyCommand(ctx, "friend", "friendDev", SyncActionDTO{
		Type: "PLAY_TRACK", Item: &QueueItemDTO{UID: "solo-track", Title: "Friend's own song"},
	}); err != nil {
		t.Fatalf("friend PLAY_TRACK: %v", err)
	}

	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "friend", "Friend")
	if err != nil {
		t.Fatalf("InviteToJam: %v", err)
	}
	if err := hub.AcceptJamInvite(ctx, jamID, "friend", "Friend", "friendDev"); err != nil {
		t.Fatalf("AcceptJamInvite: %v", err)
	}

	if err := hub.StopJam(ctx, "host"); err != nil {
		t.Fatalf("StopJam: %v", err)
	}

	friendState, err := hub.ApplyCommand(ctx, "friend", "friendDev", SyncActionDTO{Type: "TOGGLE_PLAY"})
	if err != nil {
		t.Fatalf("ApplyCommand: %v", err)
	}
	if friendState.Room == jamID {
		t.Error("friend devrait être revenu à sa propre salle solo")
	}
	if friendState.Current == nil || friendState.Current.Title != "Friend's own song" {
		t.Errorf("Current = %v, l'état solo gelé de friend doit être restauré tel quel", friendState.Current)
	}
}

func TestStopJamRejectsNonHost(t *testing.T) {
	hub, ctx := newTestHub(t)
	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "friend", "Friend")
	if err != nil {
		t.Fatalf("InviteToJam: %v", err)
	}
	if err := hub.AcceptJamInvite(ctx, jamID, "friend", "Friend", "friendDev"); err != nil {
		t.Fatalf("AcceptJamInvite: %v", err)
	}

	err = hub.StopJam(ctx, "friend")
	if !errors.Is(err, ErrNotHost) {
		t.Errorf("err = %v, attendu ErrNotHost", err)
	}
}

func TestStopJamRejectsWhenNotInAnyJam(t *testing.T) {
	hub, ctx := newTestHub(t)
	err := hub.StopJam(ctx, "nobody")
	if !errors.Is(err, ErrNoActiveJam) {
		t.Errorf("err = %v, attendu ErrNoActiveJam", err)
	}
}

func TestJamActiveDeviceIDsIsTheUnionOfMembersOutputs(t *testing.T) {
	hub, ctx := newTestHub(t)
	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "friend", "Friend")
	if err != nil {
		t.Fatalf("InviteToJam: %v", err)
	}
	if err := hub.AcceptJamInvite(ctx, jamID, "friend", "Friend", "friendDev2"); err != nil {
		t.Fatalf("AcceptJamInvite: %v", err)
	}

	state, err := hub.ApplyCommand(ctx, "friend", "friendDev2", SyncActionDTO{
		Type: "SET_ACTIVE_DEVICES", DeviceIDs: []string{"friendDev2", "friendDev3"},
	})
	if err != nil {
		t.Fatalf("SET_ACTIVE_DEVICES: %v", err)
	}
	want := map[string]bool{"hostDev": true, "friendDev2": true, "friendDev3": true}
	if len(state.ActiveDeviceIDs) != len(want) {
		t.Fatalf("ActiveDeviceIDs = %v", state.ActiveDeviceIDs)
	}
	for _, id := range state.ActiveDeviceIDs {
		if !want[id] {
			t.Errorf("appareil inattendu dans l'union: %s", id)
		}
	}
}

func TestAddedByIsStampedOnlyInsideAJam(t *testing.T) {
	hub, ctx := newTestHub(t)
	solo, err := hub.ApplyCommand(ctx, "solo-user", "dev1", SyncActionDTO{
		Type: "PLAY_TRACK", Item: &QueueItemDTO{UID: "x"},
	})
	if err != nil {
		t.Fatalf("solo PLAY_TRACK: %v", err)
	}
	if solo.Current.AddedBy != nil {
		t.Error("la lecture solo ne doit jamais tamponner AddedBy")
	}

	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "friend", "Friend")
	if err != nil {
		t.Fatalf("InviteToJam: %v", err)
	}
	if err := hub.AcceptJamInvite(ctx, jamID, "friend", "Friend", "friendDev"); err != nil {
		t.Fatalf("AcceptJamInvite: %v", err)
	}
	jamState, err := hub.ApplyCommand(ctx, "friend", "friendDev", SyncActionDTO{
		Type: "QUEUE_ADD_TO_END", Item: &QueueItemDTO{UID: "y"},
	})
	if err != nil {
		t.Fatalf("jam QUEUE_ADD_TO_END: %v", err)
	}
	if len(jamState.Queue) != 1 || jamState.Queue[0].AddedBy == nil || jamState.Queue[0].AddedBy.Name != "Friend" {
		t.Errorf("Queue = %+v, attendu un titre tamponné par Friend", jamState.Queue)
	}
}

func TestHeartbeatReapsAJamAfterEveryoneHasBeenOfflineLongEnough(t *testing.T) {
	hub, ctx := newTestHub(t)
	jamID, err := hub.InviteToJam(ctx, "host", "Host", "hostDev", "friend", "Friend")
	if err != nil {
		t.Fatalf("InviteToJam: %v", err)
	}
	if err := hub.AcceptJamInvite(ctx, jamID, "friend", "Friend", "friendDev"); err != nil {
		t.Fatalf("AcceptJamInvite: %v", err)
	}
	// No SSE connections were ever opened for host/friend in this test, so
	// both already read as offline — simulate the heartbeat running past
	// the reap window directly, without waiting jamReapDuration for real.
	done := make(chan struct{})
	hub.send(ctx, testFuncCmd(func(h *Hub) {
		if j, ok := h.jams[jamID]; ok {
			past := time.Now().Add(-jamReapDuration - time.Second)
			j.EmptySince = &past
		}
		close(done)
	}))
	<-done
	hub.send(ctx, testFuncCmd(func(h *Hub) { h.heartbeatLocked(time.Now()) }))

	err = hub.AcceptJamInvite(ctx, jamID, "friend", "Friend", "friendDev")
	if !errors.Is(err, ErrInviteNotFound) {
		t.Errorf("err = %v, attendu ErrInviteNotFound — la jam aurait dû être ramassée", err)
	}
}

// testFuncCmd lets a test inject an arbitrary closure onto the actor's own
// goroutine — used only to set up otherwise-untestable internal state
// (EmptySince in the past) and to invoke the heartbeat synchronously
// instead of waiting out the real 5s ticker.
type testFuncCmd func(h *Hub)

func (f testFuncCmd) apply(h *Hub) { f(h) }

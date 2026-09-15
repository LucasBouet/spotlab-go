package sync

import (
	"reflect"
	"testing"
)

func item(uid string) QueueItemDTO { return QueueItemDTO{UID: uid, Title: uid} }

func uids(items []QueueItemDTO) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.UID
	}
	return out
}

// TestArrayMove locks the one piece of arithmetic in this whole port most
// likely to be silently wrong: REORDER_QUEUE has to mean exactly what
// Array.prototype.splice means — toIndex addresses the list *already
// missing* the moved element. Any other reading is off by one the moment
// a row moves down.
func TestArrayMove(t *testing.T) {
	cases := []struct {
		name     string
		from, to int
		want     []string
	}{
		{"move down", 0, 2, []string{"b", "c", "a", "d"}},
		{"move up", 2, 0, []string{"c", "a", "b", "d"}},
		{"no-op same index", 1, 1, []string{"a", "b", "c", "d"}},
		{"move to the very end", 0, 3, []string{"b", "c", "d", "a"}},
		{"move last to front", 3, 0, []string{"d", "a", "b", "c"}},
		{"adjacent swap forward", 1, 2, []string{"a", "c", "b", "d"}},
		{"adjacent swap backward", 2, 1, []string{"a", "c", "b", "d"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items := []QueueItemDTO{item("a"), item("b"), item("c"), item("d")}
			got := uids(arrayMove(items, c.from, c.to))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("arrayMove(%d, %d) = %v, attendu %v", c.from, c.to, got, c.want)
			}
		})
	}
}

func TestArrayMoveDoesNotMutateInput(t *testing.T) {
	original := []QueueItemDTO{item("a"), item("b"), item("c")}
	snapshot := append([]QueueItemDTO{}, original...)
	arrayMove(original, 0, 2)
	if !reflect.DeepEqual(uids(original), uids(snapshot)) {
		t.Error("arrayMove a muté sa slice d'entrée — toute autre référence à l'ancien état est maintenant corrompue")
	}
}

func TestQueueReducerPlayTrackClearsQueueAndKeepsShuffle(t *testing.T) {
	state := QueueState{
		Queue:   []QueueItemDTO{item("x")},
		History: []QueueItemDTO{item("y")},
		Shuffle: true,
	}
	next := queueReducer(state, SyncActionDTO{Type: "PLAY_TRACK", Item: &QueueItemDTO{UID: "new"}})
	if next.Current == nil || next.Current.UID != "new" {
		t.Fatalf("Current = %v", next.Current)
	}
	if len(next.Queue) != 0 || len(next.History) != 0 {
		t.Errorf("queue/history non vidées: %+v", next)
	}
	if !next.Shuffle {
		t.Error("shuffle doit survivre à PLAY_TRACK (repris via ...state en JS)")
	}
}

func TestQueueReducerPlayContextShufflesEntireRestNotJustAfterIndex(t *testing.T) {
	// Regression test: starting a shuffled play near the end of a playlist
	// (startIndex close to len(items)) used to dump everything before
	// startIndex into History as "already played" and only shuffle the
	// handful of items after it — leaving almost the whole playlist
	// unreachable going forward. startIndex=3 of 5 items (the "avant-
	// dernière piste" case) makes that failure obvious: only "e" would
	// have ended up in Queue.
	items := []QueueItemDTO{item("a"), item("b"), item("c"), item("d"), item("e")}
	shuffle := true
	next := queueReducer(QueueState{}, SyncActionDTO{
		Type: "PLAY_CONTEXT", Items: items, StartIndex: 3, ContextID: "album:1", ShuffleOverride: &shuffle,
	})
	if next.Current.UID != "d" {
		t.Fatalf("Current = %v, attendu d (items[startIndex])", next.Current)
	}
	if len(next.History) != 0 {
		t.Errorf("History = %v, attendu vide (rien n'a encore été joué)", uids(next.History))
	}
	if len(next.Queue) != 4 {
		t.Fatalf("len(Queue) = %d, attendu 4 (a,b,c,e mélangés — tout sauf la piste courante)", len(next.Queue))
	}
	seen := map[string]bool{}
	for _, it := range next.Queue {
		seen[it.UID] = true
	}
	for _, uid := range []string{"a", "b", "c", "e"} {
		if !seen[uid] {
			t.Errorf("Queue = %v, il manque %q", uids(next.Queue), uid)
		}
	}
	if len(next.ContextTracks) != 5 {
		t.Errorf("ContextTracks doit contenir tous les items d'origine, non mélangés")
	}
	if *next.ActiveContextID != "album:1" {
		t.Errorf("ActiveContextID = %q", *next.ActiveContextID)
	}
}

func TestQueueReducerPlayContextNonShuffleStillSplitsBeforeAfter(t *testing.T) {
	// Non-shuffle behavior is unchanged: linear play from startIndex keeps
	// earlier tracks in History (reachable via Previous) and only queues
	// what comes after.
	items := []QueueItemDTO{item("a"), item("b"), item("c"), item("d"), item("e")}
	next := queueReducer(QueueState{}, SyncActionDTO{
		Type: "PLAY_CONTEXT", Items: items, StartIndex: 3, ContextID: "album:1",
	})
	if !reflect.DeepEqual(uids(next.History), []string{"a", "b", "c"}) {
		t.Errorf("History = %v, attendu [a b c]", uids(next.History))
	}
	if !reflect.DeepEqual(uids(next.Queue), []string{"e"}) {
		t.Errorf("Queue = %v, attendu [e]", uids(next.Queue))
	}
}

func TestQueueReducerPlayContextWithoutShuffleOverrideKeepsStateShuffle(t *testing.T) {
	items := []QueueItemDTO{item("a"), item("b")}
	next := queueReducer(QueueState{Shuffle: true}, SyncActionDTO{Type: "PLAY_CONTEXT", Items: items, StartIndex: 0})
	if !next.Shuffle {
		t.Error("ShuffleOverride absent (nil) doit retomber sur state.Shuffle, pas sur false")
	}
}

func TestQueueReducerSkipNextMovesQueueHeadToCurrent(t *testing.T) {
	state := QueueState{
		Current: &QueueItemDTO{UID: "now"},
		Queue:   []QueueItemDTO{item("next1"), item("next2")},
		History: []QueueItemDTO{item("prev")},
	}
	next := queueReducer(state, SyncActionDTO{Type: "SKIP_NEXT"})
	if next.Current.UID != "next1" {
		t.Fatalf("Current = %v", next.Current)
	}
	if !reflect.DeepEqual(uids(next.Queue), []string{"next2"}) {
		t.Errorf("Queue = %v", uids(next.Queue))
	}
	if !reflect.DeepEqual(uids(next.History), []string{"prev", "now"}) {
		t.Errorf("History = %v, attendu [prev now]", uids(next.History))
	}
}

func TestQueueReducerSkipNextOnEmptyQueueIsANoOpWithoutContext(t *testing.T) {
	state := QueueState{Current: &QueueItemDTO{UID: "now"}}
	next := queueReducer(state, SyncActionDTO{Type: "SKIP_NEXT"})
	if next.Current.UID != "now" {
		t.Errorf("SKIP_NEXT sur une file vide sans contexte ne doit rien changer, Current = %v", next.Current)
	}
}

func TestQueueReducerSkipNextOnEmptyQueueLoopsContext(t *testing.T) {
	state := QueueState{
		Current:       &QueueItemDTO{UID: "d"},
		Queue:         []QueueItemDTO{},
		History:       []QueueItemDTO{item("a"), item("b"), item("c")},
		ContextTracks: []QueueItemDTO{item("a"), item("b"), item("c"), item("d")},
	}
	next := queueReducer(state, SyncActionDTO{Type: "SKIP_NEXT"})
	if next.Current == nil || next.Current.UID != "a" {
		t.Fatalf("Current = %v, attendu a (début d'un nouveau tour, non mélangé)", next.Current)
	}
	if !reflect.DeepEqual(uids(next.Queue), []string{"b", "c", "d"}) {
		t.Errorf("Queue = %v, attendu [b c d]", uids(next.Queue))
	}
	if !reflect.DeepEqual(uids(next.History), []string{"a", "b", "c", "d"}) {
		t.Errorf("History = %v, attendu [a b c d]", uids(next.History))
	}
}

func TestQueueReducerSkipNextOnEmptyQueueLoopsShuffledContext(t *testing.T) {
	state := QueueState{
		Current:       &QueueItemDTO{UID: "d"},
		Queue:         []QueueItemDTO{},
		ContextTracks: []QueueItemDTO{item("a"), item("b"), item("c"), item("d")},
		Shuffle:       true,
	}
	next := queueReducer(state, SyncActionDTO{Type: "SKIP_NEXT"})
	if next.Current == nil {
		t.Fatal("Current = nil, attendu une piste du contexte")
	}
	if len(next.Queue) != 3 {
		t.Fatalf("len(Queue) = %d, attendu 3", len(next.Queue))
	}
	all := append([]string{next.Current.UID}, uids(next.Queue)...)
	seen := map[string]bool{}
	for _, uid := range all {
		seen[uid] = true
	}
	for _, uid := range []string{"a", "b", "c", "d"} {
		if !seen[uid] {
			t.Errorf("le nouveau tour a perdu %q: current=%v queue=%v", uid, next.Current.UID, uids(next.Queue))
		}
	}
}

func TestQueueReducerSkipPreviousMovesHistoryTailToCurrent(t *testing.T) {
	state := QueueState{
		Current: &QueueItemDTO{UID: "now"},
		Queue:   []QueueItemDTO{item("next")},
		History: []QueueItemDTO{item("older"), item("prev")},
	}
	next := queueReducer(state, SyncActionDTO{Type: "SKIP_PREVIOUS"})
	if next.Current.UID != "prev" {
		t.Fatalf("Current = %v", next.Current)
	}
	if !reflect.DeepEqual(uids(next.History), []string{"older"}) {
		t.Errorf("History = %v, attendu [older]", uids(next.History))
	}
	if !reflect.DeepEqual(uids(next.Queue), []string{"now", "next"}) {
		t.Errorf("Queue = %v, attendu [now next]", uids(next.Queue))
	}
}

func TestQueueReducerToggleShuffleOnKeepsManualFirst(t *testing.T) {
	manual := QueueItemDTO{UID: "m1", IsManual: true}
	rest := []QueueItemDTO{item("r1"), item("r2"), item("r3")}
	state := QueueState{Queue: append([]QueueItemDTO{manual}, rest...)}
	next := queueReducer(state, SyncActionDTO{Type: "TOGGLE_SHUFFLE"})
	if !next.Shuffle {
		t.Fatal("Shuffle devrait être activé")
	}
	if len(next.Queue) != 4 || next.Queue[0].UID != "m1" {
		t.Errorf("Queue = %v, le titre manuel doit rester en tête", uids(next.Queue))
	}
}

func TestQueueReducerToggleShuffleOffRebuildsFromUnplayedContext(t *testing.T) {
	manual := QueueItemDTO{UID: "m1", IsManual: true}
	state := QueueState{
		Current:       &QueueItemDTO{UID: "current"},
		History:       []QueueItemDTO{item("played1")},
		Queue:         []QueueItemDTO{manual, item("shuffled-remnant")},
		ContextTracks: []QueueItemDTO{item("played1"), {UID: "current"}, item("unplayed1"), item("unplayed2")},
		Shuffle:       true,
	}
	next := queueReducer(state, SyncActionDTO{Type: "TOGGLE_SHUFFLE"})
	if next.Shuffle {
		t.Fatal("Shuffle devrait être désactivé")
	}
	// manual first, then only the context tracks not yet played (current + history excluded).
	if !reflect.DeepEqual(uids(next.Queue), []string{"m1", "unplayed1", "unplayed2"}) {
		t.Errorf("Queue = %v, attendu [m1 unplayed1 unplayed2]", uids(next.Queue))
	}
}

func TestQueueReducerPlayFromQueueMovesSkippedTracksToHistory(t *testing.T) {
	state := QueueState{
		Current: &QueueItemDTO{UID: "now"},
		Queue:   []QueueItemDTO{item("skip1"), item("skip2"), item("target"), item("after")},
		History: []QueueItemDTO{item("old")},
	}
	next := queueReducer(state, SyncActionDTO{Type: "PLAY_FROM_QUEUE", UID: "target"})
	if next.Current.UID != "target" {
		t.Fatalf("Current = %v", next.Current)
	}
	if !reflect.DeepEqual(uids(next.Queue), []string{"after"}) {
		t.Errorf("Queue = %v, attendu [after]", uids(next.Queue))
	}
	if !reflect.DeepEqual(uids(next.History), []string{"old", "now", "skip1", "skip2"}) {
		t.Errorf("History = %v, attendu [old now skip1 skip2]", uids(next.History))
	}
}

func TestQueueReducerPlayFromQueueUnknownUIDIsANoOp(t *testing.T) {
	state := QueueState{Current: &QueueItemDTO{UID: "now"}, Queue: []QueueItemDTO{item("a")}}
	next := queueReducer(state, SyncActionDTO{Type: "PLAY_FROM_QUEUE", UID: "does-not-exist"})
	if next.Current.UID != "now" {
		t.Error("un uid introuvable ne doit rien changer")
	}
}

func TestQueueReducerQueuePlayNextPrepends(t *testing.T) {
	state := QueueState{Queue: []QueueItemDTO{item("a")}}
	next := queueReducer(state, SyncActionDTO{Type: "QUEUE_PLAY_NEXT", Item: &QueueItemDTO{UID: "new"}})
	if !reflect.DeepEqual(uids(next.Queue), []string{"new", "a"}) {
		t.Errorf("Queue = %v, attendu [new a]", uids(next.Queue))
	}
}

func TestQueueReducerQueueAddToEndAppends(t *testing.T) {
	state := QueueState{Queue: []QueueItemDTO{item("a")}}
	next := queueReducer(state, SyncActionDTO{Type: "QUEUE_ADD_TO_END", Item: &QueueItemDTO{UID: "new"}})
	if !reflect.DeepEqual(uids(next.Queue), []string{"a", "new"}) {
		t.Errorf("Queue = %v, attendu [a new]", uids(next.Queue))
	}
}

func TestQueueReducerRemoveFromQueue(t *testing.T) {
	state := QueueState{Queue: []QueueItemDTO{item("a"), item("b"), item("c")}}
	next := queueReducer(state, SyncActionDTO{Type: "REMOVE_FROM_QUEUE", UID: "b"})
	if !reflect.DeepEqual(uids(next.Queue), []string{"a", "c"}) {
		t.Errorf("Queue = %v, attendu [a c]", uids(next.Queue))
	}
}

func TestQueueReducerReorderQueue(t *testing.T) {
	state := QueueState{Queue: []QueueItemDTO{item("a"), item("b"), item("c")}}
	next := queueReducer(state, SyncActionDTO{Type: "REORDER_QUEUE", FromIndex: 0, ToIndex: 2})
	if !reflect.DeepEqual(uids(next.Queue), []string{"b", "c", "a"}) {
		t.Errorf("Queue = %v, attendu [b c a]", uids(next.Queue))
	}
}

func TestQueueReducerUnknownActionIsANoOp(t *testing.T) {
	state := QueueState{Queue: []QueueItemDTO{item("a")}}
	next := queueReducer(state, SyncActionDTO{Type: "SOMETHING_UNKNOWN"})
	if !reflect.DeepEqual(uids(next.Queue), []string{"a"}) {
		t.Error("une action inconnue doit laisser l'état intact")
	}
}

package sync

import (
	"math"
	"testing"
	"time"
)

func TestExtrapolatePositionWhenPaused(t *testing.T) {
	state := PlaybackState{IsPlaying: false, PositionSeconds: 42}
	got := extrapolatePosition(state, time.Now().Add(time.Hour))
	if got != 42 {
		t.Errorf("got %v, à l'arrêt la position stockée est déjà la réponse", got)
	}
}

func TestExtrapolatePositionAddsElapsedTimeWhilePlaying(t *testing.T) {
	anchor := time.Now()
	state := PlaybackState{
		IsPlaying: true, PositionSeconds: 10, PositionUpdatedAt: anchor,
		QueueState: QueueState{Current: &QueueItemDTO{Duration: 300}},
	}
	got := extrapolatePosition(state, anchor.Add(5*time.Second))
	if math.Abs(got-15) > 0.01 {
		t.Errorf("got %v, attendu ~15", got)
	}
}

func TestExtrapolatePositionClampsToDuration(t *testing.T) {
	anchor := time.Now()
	state := PlaybackState{
		IsPlaying: true, PositionSeconds: 295, PositionUpdatedAt: anchor,
		QueueState: QueueState{Current: &QueueItemDTO{Duration: 300}},
	}
	got := extrapolatePosition(state, anchor.Add(time.Hour))
	if got != 300 {
		t.Errorf("got %v, attendu 300 (plafonné à la durée)", got)
	}
}

func TestExtrapolatePositionClampsToZeroDurationWhenCurrentExistsButHasNone(t *testing.T) {
	// The subtle case: duration is +Inf only when Current itself is nil.
	// A Current with Duration=0 (a real, if odd, possibility) clamps to 0
	// immediately — this is the old server's own behavior
	// (`state.current?.duration ?? null`), not a Go-specific quirk.
	anchor := time.Now()
	state := PlaybackState{
		IsPlaying: true, PositionSeconds: 0, PositionUpdatedAt: anchor,
		QueueState: QueueState{Current: &QueueItemDTO{Duration: 0}},
	}
	got := extrapolatePosition(state, anchor.Add(5*time.Second))
	if got != 0 {
		t.Errorf("got %v, attendu 0 (durée nulle du titre courant)", got)
	}
}

func TestExtrapolatePositionIsUnboundedWithNoCurrentTrack(t *testing.T) {
	anchor := time.Now()
	state := PlaybackState{IsPlaying: true, PositionSeconds: 0, PositionUpdatedAt: anchor}
	got := extrapolatePosition(state, anchor.Add(10000*time.Hour))
	if math.IsInf(got, 0) {
		t.Error("la position ne doit jamais être elle-même infinie, seulement non plafonnée")
	}
	if got < 1000 {
		t.Errorf("got %v, attendu une valeur non plafonnée en l'absence de titre courant", got)
	}
}

func TestNextPlaybackStateTogglePlayFlipsAndAnchorsPosition(t *testing.T) {
	anchor := time.Now()
	state := PlaybackState{
		IsPlaying: false, PositionSeconds: 10, PositionUpdatedAt: anchor,
	}
	now := anchor.Add(3 * time.Second)
	next := nextPlaybackState(state, SyncActionDTO{Type: "TOGGLE_PLAY"}, now)
	if !next.IsPlaying {
		t.Error("IsPlaying devrait s'inverser à true")
	}
	if next.PositionSeconds != 10 {
		t.Errorf("PositionSeconds = %v, attendu 10 (à l'arrêt, extrapolatePosition renvoie tel quel)", next.PositionSeconds)
	}
	if !next.PositionUpdatedAt.Equal(now) {
		t.Error("PositionUpdatedAt doit être réancré à `now`")
	}
}

func TestNextPlaybackStateSeekClampsNegative(t *testing.T) {
	next := nextPlaybackState(PlaybackState{}, SyncActionDTO{Type: "SEEK", PositionSeconds: -50}, time.Now())
	if next.PositionSeconds != 0 {
		t.Errorf("PositionSeconds = %v, attendu 0 (jamais négatif)", next.PositionSeconds)
	}
}

func TestNextPlaybackStateSetActiveDevicesPassesThroughUnchanged(t *testing.T) {
	state := PlaybackState{IsPlaying: true, PositionSeconds: 99}
	next := nextPlaybackState(state, SyncActionDTO{Type: "SET_ACTIVE_DEVICES", DeviceIDs: []string{"x"}}, time.Now())
	if next.PositionSeconds != 99 || !next.IsPlaying {
		t.Error("SET_ACTIVE_DEVICES doit être un no-op ici — la logique d'affectation vit chez l'appelant (solo remplace, jam fusionne)")
	}
}

func TestNextPlaybackStateResetsPositionOnlyForResetTypes(t *testing.T) {
	anchor := time.Now()
	base := PlaybackState{
		IsPlaying: false, PositionSeconds: 50, PositionUpdatedAt: anchor,
		QueueState: QueueState{Queue: []QueueItemDTO{{UID: "next", Duration: 200}}},
	}
	now := anchor.Add(time.Minute)

	skipNext := nextPlaybackState(base, SyncActionDTO{Type: "SKIP_NEXT"}, now)
	if !skipNext.IsPlaying || skipNext.PositionSeconds != 0 {
		t.Errorf("SKIP_NEXT doit réinitialiser la position: IsPlaying=%v Position=%v", skipNext.IsPlaying, skipNext.PositionSeconds)
	}

	reordered := nextPlaybackState(base, SyncActionDTO{Type: "REORDER_QUEUE", FromIndex: 0, ToIndex: 0}, now)
	if reordered.IsPlaying || reordered.PositionSeconds != 50 {
		t.Errorf("REORDER_QUEUE ne touche pas la lecture: IsPlaying=%v Position=%v", reordered.IsPlaying, reordered.PositionSeconds)
	}
}

func TestWithAddedByStampsPlayTrack(t *testing.T) {
	action := SyncActionDTO{Type: "PLAY_TRACK", Item: &QueueItemDTO{UID: "x"}}
	by := AddedByDTO{ID: "u1", Name: "Marie"}
	stamped := withAddedBy(action, by)
	if stamped.Item.AddedBy == nil || stamped.Item.AddedBy.Name != "Marie" {
		t.Errorf("AddedBy = %v", stamped.Item.AddedBy)
	}
	if action.Item.AddedBy != nil {
		t.Error("l'action d'origine ne doit pas être mutée — withAddedBy doit renvoyer une copie")
	}
}

func TestWithAddedByStampsEveryItemInPlayContext(t *testing.T) {
	action := SyncActionDTO{Type: "PLAY_CONTEXT", Items: []QueueItemDTO{{UID: "a"}, {UID: "b"}}}
	stamped := withAddedBy(action, AddedByDTO{ID: "u1", Name: "Marie"})
	for _, it := range stamped.Items {
		if it.AddedBy == nil || it.AddedBy.Name != "Marie" {
			t.Errorf("item %s: AddedBy = %v", it.UID, it.AddedBy)
		}
	}
}

func TestWithAddedByLeavesOtherActionsUntouched(t *testing.T) {
	action := SyncActionDTO{Type: "TOGGLE_PLAY"}
	stamped := withAddedBy(action, AddedByDTO{ID: "u1", Name: "Marie"})
	if stamped.Type != "TOGGLE_PLAY" || stamped.Item != nil {
		t.Errorf("une action sans item ne doit pas être modifiée: %+v", stamped)
	}
}

func TestToDTOFormatsTimeAsRFC3339(t *testing.T) {
	when := time.Date(2026, 8, 11, 19, 6, 4, 0, time.UTC)
	dto := toDTO(PlaybackState{PositionUpdatedAt: when}, "room1", nil)
	if dto.PositionUpdatedAt != "2026-08-11T19:06:04Z" {
		t.Errorf("PositionUpdatedAt = %q", dto.PositionUpdatedAt)
	}
	if dto.Room != "room1" {
		t.Errorf("Room = %q", dto.Room)
	}
	if dto.Jam != nil {
		t.Error("Jam doit rester nil hors jam")
	}
}

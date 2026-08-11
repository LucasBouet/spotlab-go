package stats

import (
	"reflect"
	"testing"
	"time"
)

func TestInterleaveRoundRobinsAcrossGroups(t *testing.T) {
	groups := [][]int{{1, 2, 3}, {4, 5}, {6}}
	got := interleave(groups, 10)
	want := []int{1, 4, 6, 2, 5, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("interleave = %v, attendu %v", got, want)
	}
}

func TestInterleaveStopsAtMax(t *testing.T) {
	groups := [][]int{{1, 2, 3}, {4, 5, 6}}
	got := interleave(groups, 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, attendu 3", len(got))
	}
	want := []int{1, 4, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("interleave = %v, attendu %v", got, want)
	}
}

func TestInterleaveHandlesEmptyGroups(t *testing.T) {
	if got := interleave[int](nil, 5); len(got) != 0 {
		t.Errorf("interleave(nil) = %v, attendu vide", got)
	}
	if got := interleave([][]int{{}, {}}, 5); len(got) != 0 {
		t.Errorf("interleave([[],[]]) = %v, attendu vide", got)
	}
}

func TestMapPoolPreservesOrderAndAppliesFnToEveryItem(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	got := mapPool(items, 3, func(i int) int { return i * i })
	want := []int{1, 4, 9, 16, 25, 36, 49, 64, 81, 100}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mapPool = %v, attendu %v", got, want)
	}
}

func TestMapPoolHandlesEmptyInput(t *testing.T) {
	got := mapPool[int, int](nil, 4, func(i int) int { return i })
	if len(got) != 0 {
		t.Errorf("mapPool(nil) = %v, attendu vide", got)
	}
}

func TestMapPoolHandlesLimitLargerThanInput(t *testing.T) {
	items := []int{1, 2}
	got := mapPool(items, 10, func(i int) int { return i + 1 })
	want := []int{2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mapPool = %v, attendu %v", got, want)
	}
}

func TestAlbumKeyIsCaseInsensitive(t *testing.T) {
	a := albumKey("Master Of Puppets", "Metallica")
	b := albumKey("master of puppets", "METALLICA")
	if a != b {
		t.Errorf("albumKey diverge selon la casse: %q vs %q", a, b)
	}
}

func TestParseWindowAcceptsOnlyKnownValues(t *testing.T) {
	for _, v := range []string{"day", "week", "all"} {
		if _, ok := parseWindow(v); !ok {
			t.Errorf("parseWindow(%q) = false, attendu true", v)
		}
	}
	for _, v := range []string{"", "month", "DAY", "all "} {
		if _, ok := parseWindow(v); ok {
			t.Errorf("parseWindow(%q) = true, attendu false", v)
		}
	}
}

func TestWindowStartNilForAllWindow(t *testing.T) {
	if got := windowStart(windowAll); got != nil {
		t.Errorf("windowStart(all) = %v, attendu nil", got)
	}
}

func TestWindowStartBoundsForDayAndWeek(t *testing.T) {
	now := time.Now()

	day := windowStart(windowDay)
	if day == nil {
		t.Fatal("windowStart(day) = nil")
	}
	if diff := now.Sub(*day); diff < 23*time.Hour || diff > 25*time.Hour {
		t.Errorf("windowStart(day) est à %v du présent, attendu ~24h", diff)
	}

	week := windowStart(windowWeek)
	if week == nil {
		t.Fatal("windowStart(week) = nil")
	}
	if diff := now.Sub(*week); diff < 6*24*time.Hour || diff > 8*24*time.Hour {
		t.Errorf("windowStart(week) est à %v du présent, attendu ~7j", diff)
	}
}

func TestPickRelatedExcludesSeedsAndDuplicates(t *testing.T) {
	seedIDs := map[int64]bool{1: true}
	lists := [][]artistRef{
		{{ID: 1, Name: "Seed"}, {ID: 2, Name: "A"}, {ID: 3, Name: "B"}},
		{{ID: 2, Name: "A dup"}, {ID: 4, Name: "C"}},
	}
	got := pickRelated(seedIDs, lists)

	seen := map[int64]bool{}
	for _, a := range got {
		if a.ID == 1 {
			t.Error("pickRelated a inclus l'artiste seed lui-même")
		}
		if seen[a.ID] {
			t.Errorf("pickRelated a inclus %d en double", a.ID)
		}
		seen[a.ID] = true
	}
	if len(got) != 3 {
		t.Errorf("len = %d, attendu 3 (2, 4, 3)", len(got))
	}
}

func TestPickRelatedCapsAtMaxRelatedArtists(t *testing.T) {
	seedIDs := map[int64]bool{}
	var list []artistRef
	for i := int64(1); i <= maxRelatedArtists+5; i++ {
		list = append(list, artistRef{ID: i, Name: "A"})
	}
	got := pickRelated(seedIDs, [][]artistRef{list})
	if len(got) != maxRelatedArtists {
		t.Errorf("len = %d, attendu %d", len(got), maxRelatedArtists)
	}
}

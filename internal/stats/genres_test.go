package stats

import "testing"

func TestMatchGenreTagRecognizesExactAndNormalizedForms(t *testing.T) {
	cases := []struct {
		tag  string
		want string
	}{
		{"Metalcore", "Metalcore"},
		{"metalcore", "Metalcore"},
		{"  Post-Hardcore  ", "Post-Hardcore"},
		{"drum_n_bass", "Drum & Bass"}, // underscores normalize to spaces, same as the "drum n bass" alias
		{"seen live", ""},
		{"favorites", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := matchGenreTag(c.tag); got != c.want {
			t.Errorf("matchGenreTag(%q) = %q, attendu %q", c.tag, got, c.want)
		}
	}
}

func TestMatchGenreTagResolvesAliases(t *testing.T) {
	cases := map[string]string{
		"rap":              "Hip-Hop",
		"Hip Hop":          "Hip-Hop",
		"RnB":              "R&B",
		"drum n bass":      "Drum & Bass",
		"drum and bass":    "Drum & Bass",
		"lofi":             "Lo-fi Hip-Hop",
		"melodic hardcore": "Post-Hardcore",
		"nu-metal":         "Nu Metal",
	}
	for tag, want := range cases {
		if got := matchGenreTag(tag); got != want {
			t.Errorf("matchGenreTag(%q) = %q, attendu %q", tag, got, want)
		}
	}
}

func TestPickGenrePrefersSpecificOverBroadWithinTopTags(t *testing.T) {
	// "Metal" (broad) appears before "Deathcore" (specific) in popularity —
	// the specific genre must still win, matching pickGenre's "first
	// recognized specific genre wins" rule over "first tag wins".
	got := pickGenre([]string{"Metal", "seen live", "Deathcore"})
	if got != "Deathcore" {
		t.Errorf("pickGenre = %q, attendu Deathcore (spécifique doit gagner sur large)", got)
	}
}

func TestPickGenreFallsBackToBroadWhenNoSpecificTagMatches(t *testing.T) {
	got := pickGenre([]string{"favorites", "Rock", "american"})
	if got != "Rock" {
		t.Errorf("pickGenre = %q, attendu Rock (repli sur le genre large)", got)
	}
}

func TestPickGenreReturnsEmptyWhenNothingMatches(t *testing.T) {
	if got := pickGenre([]string{"seen live", "favorites", "00s"}); got != "" {
		t.Errorf("pickGenre = %q, attendu vide", got)
	}
}

func TestPickGenreOnlyConsidersTopTags(t *testing.T) {
	// Ten junk tags fill the whole top-10 window; the only recognizable
	// genres come after it and must not surface.
	tags := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "Rock", "Deathcore"}
	got := pickGenre(tags)
	if got != "" {
		t.Errorf("pickGenre = %q, attendu vide — Rock/Deathcore sont hors des 10 premiers tags", got)
	}
}

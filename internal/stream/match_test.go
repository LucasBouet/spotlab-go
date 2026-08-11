package stream

import "testing"

func TestNormalizeStripsDiacriticsCaseAndPunctuation(t *testing.T) {
	cases := map[string]string{
		"Café del Mar":       "cafe del mar",
		"MÉTALLICA":          "metallica",
		"Naïve (Remastered)": "naive remastered",
		"  spaced   out  ":   "spaced out",
		"":                   "",
		"Björk":              "bjork",
	}
	for input, want := range cases {
		if got := normalize(input); got != want {
			t.Errorf("normalize(%q) = %q, attendu %q", input, got, want)
		}
	}
}

func TestScoreCandidateExactMatchScoresHighest(t *testing.T) {
	query := MatchQuery{Title: "Master of Puppets", Artist: "Metallica", DurationSeconds: 515}
	exact := ytDlpSearchResult{Title: "Master of Puppets", Uploader: "Metallica", Duration: 515}
	partial := ytDlpSearchResult{Title: "Master of Puppets (Live)", Uploader: "Metallica Fan Channel", Duration: 700}
	unrelated := ytDlpSearchResult{Title: "Some Other Song", Uploader: "Some Other Artist", Duration: 200}

	exactScore := scoreCandidate(exact, query)
	partialScore := scoreCandidate(partial, query)
	unrelatedScore := scoreCandidate(unrelated, query)

	if !(exactScore > partialScore && partialScore > unrelatedScore) {
		t.Errorf("scores = exact:%v partial:%v unrelated:%v, attendu un ordre strictement décroissant", exactScore, partialScore, unrelatedScore)
	}
}

func TestScoreCandidatePenalizesDurationOutsideTolerance(t *testing.T) {
	query := MatchQuery{Title: "Song", Artist: "Artist", DurationSeconds: 200}
	closeEnough := ytDlpSearchResult{Title: "Song", Uploader: "Artist", Duration: 205}
	farOff := ytDlpSearchResult{Title: "Song", Uploader: "Artist", Duration: 3000}

	if scoreCandidate(closeEnough, query) <= scoreCandidate(farOff, query) {
		t.Error("un écart de durée hors tolérance doit être pénalisé par rapport à un écart proche")
	}
}

func TestScoreCandidateHandlesMissingDuration(t *testing.T) {
	query := MatchQuery{Title: "Song", Artist: "Artist", DurationSeconds: 200}
	noDuration := ytDlpSearchResult{Title: "Song", Uploader: "Artist", Duration: 0}
	// Exact title+artist match alone must still score positively even with
	// no usable duration signal (Duration <= 0 skips that scoring branch
	// entirely, mirroring `if (song.duration != null)` in ytmusic.ts).
	if got := scoreCandidate(noDuration, query); got != 6 {
		t.Errorf("score = %v, attendu 6 (3 titre + 3 artiste, aucun bonus/malus de durée)", got)
	}
}

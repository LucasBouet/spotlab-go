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

func TestScoreCandidatePrefersTopicChannelOverOfficialVideo(t *testing.T) {
	// The regression this guards: an official music video and a "- Topic"
	// audio upload can both land within duration tolerance of the real
	// track, but only the Topic upload is guaranteed to be just the studio
	// audio — the video's own intro/outro is what desyncs playback and
	// lyrics (see the comment on preferredUploaderSuffix in match.go).
	// Title+artist+duration alone score these two identically (partial title
	// match + exact artist for one, exact title + partial artist for the
	// other, same duration) — deliberately, so only the uploader/title
	// heuristics can be what breaks the tie. Without them this is exactly a
	// wash, which is the regression: duration/title/artist scoring alone
	// cannot tell these two apart, and a wash resolves arbitrarily.
	query := MatchQuery{Title: "Rockstar", Artist: "Post Malone", DurationSeconds: 218}
	topicUpload := ytDlpSearchResult{Title: "Rockstar", Uploader: "Post Malone - Topic", Duration: 218}
	officialVideo := ytDlpSearchResult{Title: "Rockstar (Official Video)", Uploader: "Post Malone", Duration: 218}

	if scoreCandidate(topicUpload, query) <= scoreCandidate(officialVideo, query) {
		t.Error("un upload \"- Topic\" doit scorer au-dessus d'une vidéo officielle de même durée/titre approchants")
	}
}

func TestHasDemotedTitleSignal(t *testing.T) {
	cases := map[string]bool{
		"rockstar official video":       true,
		"song title live":               true,
		"official lyric video":          true,
		"artist name behind the scenes": true,
		"rockstar":                      false,
		"olive garden":                  false, // "olive" must not match the word "live"
		"clipper city anthem":           false, // "clipper" must not match the word "clip"
	}
	for input, want := range cases {
		if got := hasDemotedTitleSignal(input); got != want {
			t.Errorf("hasDemotedTitleSignal(%q) = %v, attendu %v", input, got, want)
		}
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

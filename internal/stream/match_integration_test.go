package stream

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newFakeExecutable writes script as an executable shell script and
// returns its path — used to stand in for the real yt-dlp binary so tests
// exercise FindBestMatch/runDownload's actual process-spawning and
// stdout-parsing code without hitting the real network or requiring
// yt-dlp to be installed.
func newFakeExecutable(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("le faux exécutable yt-dlp de test est un script shell")
	}
	path := filepath.Join(t.TempDir(), "fake-yt-dlp")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("écriture du faux yt-dlp: %v", err)
	}
	return path
}

func TestFindBestMatchPicksHighestScoringCandidate(t *testing.T) {
	fake := newFakeExecutable(t, `cat <<'EOF'
{"id":"vid-exact","title":"Master of Puppets","uploader":"Metallica","duration":515}
{"id":"vid-live","title":"Master of Puppets (Live in Seattle)","uploader":"Metallica Bootlegs","duration":700}
{"id":"vid-unrelated","title":"Some Other Song","uploader":"Nobody","duration":180}
EOF
`)

	videoID, err := FindBestMatch(context.Background(), fake, MatchQuery{
		Title: "Master of Puppets", Artist: "Metallica", DurationSeconds: 515,
	})
	if err != nil {
		t.Fatalf("FindBestMatch: %v", err)
	}
	if videoID != "vid-exact" {
		t.Errorf("videoID = %q, attendu vid-exact", videoID)
	}
}

// TestFindBestMatchPrefersCleanPartialMatchOverHigherScoringLiveVersion
// guards the regression reported live: some tracks resolved to a live
// recording "for no reason" even though a studio upload was also among the
// candidates. hasDemotedTitleSignal's -2.5 penalty is soft, so an
// exact-everything live candidate can still out-score a merely-partial
// match on the real studio upload (e.g. a slightly different uploader
// name) — this asserts the studio upload wins regardless, as long as it
// genuinely matches title and artist.
func TestFindBestMatchPrefersCleanPartialMatchOverHigherScoringLiveVersion(t *testing.T) {
	fake := newFakeExecutable(t, `cat <<'EOF'
{"id":"vid-live","title":"Rockstar (Live at Wembley)","uploader":"Post Malone","duration":218}
{"id":"vid-studio","title":"Rockstar","uploader":"Post Malone Music","duration":218}
EOF
`)
	videoID, err := FindBestMatch(context.Background(), fake, MatchQuery{
		Title: "Rockstar", Artist: "Post Malone", DurationSeconds: 218,
	})
	if err != nil {
		t.Fatalf("FindBestMatch: %v", err)
	}
	if videoID != "vid-studio" {
		t.Errorf("videoID = %q, attendu vid-studio (une version live ne doit jamais gagner face à un candidat propre correctement matché)", videoID)
	}
}

// TestFindBestMatchFallsBackToLiveVersionWhenNothingElseMatches ensures the
// override doesn't turn into a hard exclusion: if every candidate looks
// live/lyric/etc, the best-scoring one among them should still be returned
// rather than failing the whole resolution.
func TestFindBestMatchFallsBackToLiveVersionWhenNothingElseMatches(t *testing.T) {
	fake := newFakeExecutable(t, `cat <<'EOF'
{"id":"vid-live-close","title":"Master of Puppets (Live)","uploader":"Metallica","duration":515}
{"id":"vid-live-far","title":"Master of Puppets (Live in Seattle)","uploader":"Metallica Bootlegs","duration":900}
EOF
`)
	videoID, err := FindBestMatch(context.Background(), fake, MatchQuery{
		Title: "Master of Puppets", Artist: "Metallica", DurationSeconds: 515,
	})
	if err != nil {
		t.Fatalf("FindBestMatch: %v", err)
	}
	if videoID != "vid-live-close" {
		t.Errorf("videoID = %q, attendu vid-live-close (repli sur le meilleur score quand rien n'est propre)", videoID)
	}
}

func TestFindBestMatchSkipsMalformedLinesAndBlankLines(t *testing.T) {
	fake := newFakeExecutable(t, `cat <<'EOF'
not json at all

{"id":"vid-ok","title":"Song","uploader":"Artist","duration":200}
EOF
`)
	videoID, err := FindBestMatch(context.Background(), fake, MatchQuery{
		Title: "Song", Artist: "Artist", DurationSeconds: 200,
	})
	if err != nil {
		t.Fatalf("FindBestMatch: %v", err)
	}
	if videoID != "vid-ok" {
		t.Errorf("videoID = %q, attendu vid-ok", videoID)
	}
}

func TestFindBestMatchReturnsErrorWhenNoCandidates(t *testing.T) {
	fake := newFakeExecutable(t, `true`) // exits 0, prints nothing
	_, err := FindBestMatch(context.Background(), fake, MatchQuery{Title: "X", Artist: "Y", DurationSeconds: 100})
	if err == nil {
		t.Fatal("attendu une erreur quand yt-dlp ne renvoie aucun candidat")
	}
}

func TestFindBestMatchSurfacesStderrOnFailure(t *testing.T) {
	fake := newFakeExecutable(t, `echo "ERROR: video unavailable" >&2; exit 1`)
	_, err := FindBestMatch(context.Background(), fake, MatchQuery{Title: "X", Artist: "Y", DurationSeconds: 100})
	if err == nil {
		t.Fatal("attendu une erreur quand yt-dlp échoue")
	}
	if !strings.Contains(err.Error(), "video unavailable") {
		t.Errorf("err = %q, attendu qu'il contienne le stderr de yt-dlp", err.Error())
	}
}

// TestFindBestMatchTimesOutOnHangingSearch guards the regression this fix
// closes: a yt-dlp search that never exits (YouTube-side stall, throttling)
// used to hang the caller — and therefore GET /api/stream/{id} for any
// uncached track — forever, since only the download step had a timeout.
// searchTimeout must actually bound the search half too.
func TestFindBestMatchTimesOutOnHangingSearch(t *testing.T) {
	previous := searchTimeout
	searchTimeout = 200 * time.Millisecond
	defer func() { searchTimeout = previous }()

	// exec, not a plain `sleep 30`: a plain command would fork sleep as a
	// child of this script's shell, and killing only the shell (what
	// exec.CommandContext does on timeout) leaves that orphaned child
	// holding the stdout pipe open — the very os/exec pipe-inheritance
	// gotcha this test would otherwise fall into, and unrelated to the
	// behavior under test. exec replaces the shell with sleep in place (same
	// pid), so the timeout's kill actually stops it.
	fake := newFakeExecutable(t, `exec sleep 30`)
	start := time.Now()
	_, err := FindBestMatch(context.Background(), fake, MatchQuery{Title: "X", Artist: "Y", DurationSeconds: 100})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("attendu une erreur quand yt-dlp ne répond jamais")
	}
	if elapsed >= 5*time.Second {
		t.Errorf("FindBestMatch a attendu %v, attendu un abandon peu après searchTimeout (200ms)", elapsed)
	}
}

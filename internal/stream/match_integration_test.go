package stream

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

package stream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// searchResultCount and durationToleranceSeconds mirror ytsearch5 and
// DURATION_TOLERANCE_SECONDS in ytmusic.ts. Search itself moved from
// ytmusic-api (no Go equivalent) to yt-dlp's own `ytsearchN:` — see
// docs/PLAN.md §0/§4.2 — but the scoring stayed a direct port.
const (
	searchResultCount        = 5
	durationToleranceSeconds = 12
)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

var stripMarks = transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// normalize mirrors normalize() in ytmusic.ts: lowercase, strip diacritics
// (décompose NFKD, drop combining marks, recompose), collapse everything
// that isn't a-z0-9 into single spaces, trim.
func normalize(value string) string {
	decomposed, _, err := transform.String(stripMarks, strings.ToLower(value))
	if err != nil {
		decomposed = strings.ToLower(value)
	}
	return strings.TrimSpace(nonAlphanumeric.ReplaceAllString(decomposed, " "))
}

// MatchQuery is the Deezer-side information used to score a YouTube
// candidate — TrackMatchQuery in ytmusic.ts.
type MatchQuery struct {
	Title           string
	Artist          string
	DurationSeconds int
}

// ytDlpSearchResult is the subset of `yt-dlp ytsearchN:... --dump-json
// --flat-playlist`'s one-line-per-video output this package reads —
// verified against a real invocation before writing this (see the field
// names: id/title/uploader/duration, not name/artist.name/duration like
// ytmusic-api's SongDetailed).
type ytDlpSearchResult struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Uploader string  `json:"uploader"`
	Duration float64 `json:"duration"`
}

func scoreCandidate(c ytDlpSearchResult, query MatchQuery) float64 {
	candidateTitle := normalize(c.Title)
	candidateArtist := normalize(c.Uploader)
	queryTitle := normalize(query.Title)
	queryArtist := normalize(query.Artist)

	var score float64
	switch {
	case candidateTitle == queryTitle:
		score += 3
	case strings.Contains(candidateTitle, queryTitle) || strings.Contains(queryTitle, candidateTitle):
		score += 1.5
	}
	switch {
	case candidateArtist == queryArtist:
		score += 3
	case strings.Contains(candidateArtist, queryArtist) || strings.Contains(queryArtist, candidateArtist):
		score += 1.5
	}
	if c.Duration > 0 {
		diff := math.Abs(c.Duration - float64(query.DurationSeconds))
		if diff <= durationToleranceSeconds {
			score += 2 - diff/durationToleranceSeconds
		} else {
			score -= 1
		}
	}
	return score
}

// FindBestMatch runs a yt-dlp native search for query and returns the
// video id of the closest-scoring candidate, or an error if yt-dlp fails
// or nothing usable comes back. Mirrors findBestMatch in ytmusic.ts.
func FindBestMatch(ctx context.Context, ytdlpPath string, query MatchQuery) (string, error) {
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}
	searchTerm := fmt.Sprintf("ytsearch%d:%s %s", searchResultCount, query.Artist, query.Title)
	cmd := exec.CommandContext(ctx, ytdlpPath, searchTerm,
		"--dump-json", "--no-warnings", "--quiet", "--flat-playlist")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("recherche yt-dlp : %s", detail)
	}

	var best string
	bestScore := math.Inf(-1)
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var candidate ytDlpSearchResult
		if err := json.Unmarshal(line, &candidate); err != nil {
			continue
		}
		if candidate.ID == "" {
			continue
		}
		if score := scoreCandidate(candidate, query); score > bestScore {
			bestScore = score
			best = candidate.ID
		}
	}

	if best == "" {
		return "", errors.New("aucune correspondance trouvée sur YouTube")
	}
	return best, nil
}

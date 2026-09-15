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
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// searchResultCount and durationToleranceSeconds mirror ytsearch5 (bumped
// to 8, see below) and DURATION_TOLERANCE_SECONDS in ytmusic.ts. Search
// itself moved from
// ytmusic-api (no Go equivalent) to yt-dlp's own `ytsearchN:` — see
// docs/PLAN.md §0/§4.2 — but the scoring stayed a direct port.
//
// That move quietly dropped a filter the old search had for free:
// ytmusic-api's searchSongs() only ever returned YouTube Music's curated
// "Songs" catalog — clean studio audio, essentially never a music video, a
// live cut, or anything with a spoken intro. A plain `ytsearchN:` returns
// whatever ranks on YouTube, official "clip" uploads included, and those
// can land well within duration/title/artist tolerance while still not
// being the actual track (extra content at the start/middle/end desyncs
// playback position and lyrics). preferredUploaderSuffix and the
// demotedTitle* lists are a substitute for that lost filter.
const (
	// Widened from 5: with the "prefer a non-live/non-lyric candidate"
	// override below, a bigger candidate pool matters more than it used
	// to — the fix can only pick a clean studio upload if one actually
	// showed up in the search results in the first place.
	searchResultCount        = 8
	durationToleranceSeconds = 12

	// YouTube auto-generates "<artist> - Topic" channels from official
	// releases (content ID, not a human upload) — audio only, always
	// matching the real release exactly. The single most reliable signal
	// available without ytmusic-api's catalog restriction.
	preferredUploaderSuffix = "topic"
)

// searchTimeout bounds one yt-dlp search invocation — mirrors downloadTimeout
// in pipeline.go, which only ever guarded the download half of a fetch. The
// search half (this file) had no bound at all: a yt-dlp search that stalls
// (YouTube throttling/anti-bot changes, network stall) used to hang GET
// /api/stream/{id} for any uncached track forever, since manager.go's
// server-lifetime ctx has no deadline of its own. That reads to the app as
// "never loads" until some unrelated network hop eventually kills the idle
// connection — fixed by giving this step the same kind of bound the download
// step already had. A var, not a const, so tests can shrink it instead of
// actually waiting out a multi-second timeout.
var searchTimeout = 30 * time.Second

// youtubeExtractorArgs forces yt-dlp to use YouTube's "android" client for
// both search and download. As of writing, YouTube's default (web) client
// requires a PO token to fetch actual media bytes that yt-dlp doesn't
// supply, so a plain fetch answers 403 Forbidden on every real download
// even though search (a different endpoint) still works fine — verified
// directly against the real API: web 403s, android succeeds. The tradeoff:
// android only ever offers one muxed video+audio format, never a separate
// audio-only stream, so a cached file carries a small unused video track
// (a few extra MB) instead of being pure audio.
var youtubeExtractorArgs = []string{"--extractor-args", "youtube:player_client=android"}

// demotedTitlePhrases and demotedTitleWords flag candidates that are
// probably not the plain track: official music videos, live performances,
// lyric videos, and similar uploads that share the track's title/artist
// and often a close-enough duration, but contain content (intros, crowd
// noise, spoken bits) the studio track doesn't.
var demotedTitlePhrases = []string{
	"official video",
	"music video",
	"behind the scenes",
	"lyric video",
}

var demotedTitleWords = map[string]bool{
	"live":      true,
	"clip":      true,
	"lyrics":    true,
	"trailer":   true,
	"teaser":    true,
	"interview": true,
	"reaction":  true,
}

// hasDemotedTitleSignal expects an already-[normalize]d title: matching
// whole words (via Fields) rather than raw substrings avoids flagging
// something like "olive" for containing "live".
func hasDemotedTitleSignal(normalizedTitle string) bool {
	for _, phrase := range demotedTitlePhrases {
		if strings.Contains(normalizedTitle, phrase) {
			return true
		}
	}
	for _, word := range strings.Fields(normalizedTitle) {
		if demotedTitleWords[word] {
			return true
		}
	}
	return false
}

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

// hasTextualMatch reports whether a candidate's title *and* uploader both
// resemble the query — used to gate the "prefer a non-live candidate"
// override below at a genuine match, not just "some video with a similar
// duration". Requiring both fields (not just one, like scoreCandidate's
// softer partial-credit scoring) keeps a generic-titled unrelated video
// from ever outranking a properly-matched live/lyric upload.
func hasTextualMatch(c ytDlpSearchResult, query MatchQuery) bool {
	candidateTitle := normalize(c.Title)
	candidateArtist := normalize(c.Uploader)
	queryTitle := normalize(query.Title)
	queryArtist := normalize(query.Artist)
	titleMatches := candidateTitle == queryTitle ||
		strings.Contains(candidateTitle, queryTitle) || strings.Contains(queryTitle, candidateTitle)
	artistMatches := candidateArtist == queryArtist ||
		strings.Contains(candidateArtist, queryArtist) || strings.Contains(queryArtist, candidateArtist)
	return titleMatches && artistMatches
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
	if strings.HasSuffix(candidateArtist, preferredUploaderSuffix) {
		score += 2
	}
	if hasDemotedTitleSignal(candidateTitle) {
		score -= 2.5
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
	searchCtx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	searchTerm := fmt.Sprintf("ytsearch%d:%s %s", searchResultCount, query.Artist, query.Title)
	args := append([]string{searchTerm, "--dump-json", "--no-warnings", "--quiet", "--flat-playlist"}, youtubeExtractorArgs...)
	cmd := exec.CommandContext(searchCtx, ytdlpPath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if searchCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("la recherche YouTube a mis trop de temps à répondre")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("recherche yt-dlp : %s", detail)
	}

	var best, bestClean string
	bestScore := math.Inf(-1)
	bestCleanScore := math.Inf(-1)
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
		score := scoreCandidate(candidate, query)
		if score > bestScore {
			bestScore = score
			best = candidate.ID
		}
		// hasDemotedTitleSignal's -2.5 penalty is a soft nudge: an exact
		// title+artist+duration match on a live/lyric upload can still
		// out-score a merely-partial match elsewhere, which is how a live
		// version could win despite the penalty. So on top of scoring,
		// track the best candidate that both matches genuinely (title AND
		// artist, not just duration luck) and carries no demoted signal
		// at all, and prefer it outright over the raw top score — falling
		// back to bestScore only when every candidate looks demoted or
		// unmatched.
		if !hasDemotedTitleSignal(normalize(candidate.Title)) && hasTextualMatch(candidate, query) && score > bestCleanScore {
			bestCleanScore = score
			bestClean = candidate.ID
		}
	}

	if bestClean != "" {
		return bestClean, nil
	}
	if best == "" {
		return "", errors.New("aucune correspondance trouvée sur YouTube")
	}
	return best, nil
}

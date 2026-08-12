package stream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/lucasbouet/spotlab-go/internal/catalog"
)

// Manager owns the on-disk cache and the table of in-flight downloads —
// the Go equivalent of the module-level `inFlight` Map plus the free
// functions in stream.ts, bundled into one value so it can be constructed
// once in main() and handed to the HTTP layer.
type Manager struct {
	cacheDir   string
	ytdlpPath  string
	ffmpegPath string
	deezer     *catalog.DeezerClient

	// ctx is the server's lifetime context — deliberately NOT any single
	// request's context. A download must outlive the request that started
	// it: a second device's request for the same track, or the request
	// that started it disconnecting early, must not kill an in-progress
	// download other readers may still be tailing (docs/PLAN.md §4.1).
	ctx context.Context

	mu       sync.Mutex
	inFlight map[string]*Download
}

func NewManager(ctx context.Context, cacheDir, ytdlpPath, ffmpegPath string, deezer *catalog.DeezerClient) *Manager {
	return &Manager{
		ctx: ctx, cacheDir: cacheDir, ytdlpPath: ytdlpPath, ffmpegPath: ffmpegPath, deezer: deezer,
		inFlight: make(map[string]*Download),
	}
}

// inFlightCount is a test hook: it lets tests observe when a download has
// left the dedup table (i.e. finished) without racing the map read against
// getOrStartDownload's own locked access to it.
func (m *Manager) inFlightCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inFlight)
}

// getOrStartDownload mirrors getOrStartDownload in stream.ts: a second
// caller for a track already downloading joins the same Download rather
// than starting a redundant one. The map entry is reserved under m.mu
// before any work happens, so two concurrent callers can never both start
// a download for the same id — same guarantee the JS original gets from
// setting the map entry synchronously before its first await.
func (m *Manager) getOrStartDownload(trackID string) *Download {
	m.mu.Lock()
	if d, ok := m.inFlight[trackID]; ok {
		m.mu.Unlock()
		return d
	}
	download := NewDownload()
	m.inFlight[trackID] = download
	m.mu.Unlock()

	go func() {
		m.runTrackDownload(trackID, download)
		m.mu.Lock()
		delete(m.inFlight, trackID)
		m.mu.Unlock()
	}()

	return download
}

type deezerTrackForMatch struct {
	Title    string `json:"title"`
	Duration int    `json:"duration"`
	Artist   struct {
		Name string `json:"name"`
	} `json:"artist"`
}

// runTrackDownload mirrors startTrackDownload in stream.ts: re-check the
// cache first (another request may have finished caching this exact track
// while this one waited to be scheduled), then resolve Deezer metadata,
// find the closest YouTube match, and download it.
func (m *Manager) runTrackDownload(trackID string, download *Download) {
	if filePath, ok := findExistingCacheFile(m.cacheDir, trackID); ok {
		completeFromExistingFile(download, filePath)
		return
	}

	trackRaw, ok := m.deezer.FetchTrack(m.ctx, trackID)
	if !ok {
		download.fail(errors.New("ce titre est introuvable"))
		return
	}
	var track deezerTrackForMatch
	if err := json.Unmarshal(trackRaw, &track); err != nil {
		download.fail(errors.New("réponse Deezer invalide"))
		return
	}

	videoID, err := FindBestMatch(m.ctx, m.ytdlpPath, MatchQuery{
		Title: track.Title, Artist: track.Artist.Name, DurationSeconds: track.Duration,
	})
	if err != nil {
		download.fail(err)
		return
	}

	runDownload(m.ctx, m.ytdlpPath, videoID, m.cacheDir, trackID, download)
}

func completeFromExistingFile(download *Download, filePath string) {
	info, err := os.Stat(filePath)
	if err != nil {
		download.fail(err)
		return
	}
	download.setExt(filePath, contentTypeForPath(filePath))
	download.addBytes(info.Size())
	download.complete()
}

// ResolveFile blocks until trackID is fully cached, returning the final
// file path — backs POST /api/prefetch/{id}, and mirrors resolveTrackFile
// in stream.ts.
func (m *Manager) ResolveFile(trackID string) (string, error) {
	if filePath, ok := findExistingCacheFile(m.cacheDir, trackID); ok {
		return filePath, nil
	}
	return m.getOrStartDownload(trackID).WaitForDone()
}

// StreamResult is what OpenStream hands the HTTP handler: either an
// already-complete file (served with Range support) or a live reader
// tailing an in-progress download (not seekable yet).
type StreamResult struct {
	Cached      bool
	FilePath    string
	ContentType string
	Reader      io.ReadCloser
}

// OpenStream mirrors openTrackStream in stream.ts: a cached track answers
// immediately with its file path; an uncached one waits only for yt-dlp to
// pick a format, then hands back a live-tailing reader so playback can
// start before the whole file exists on disk.
func (m *Manager) OpenStream(trackID string) (StreamResult, error) {
	if filePath, ok := findExistingCacheFile(m.cacheDir, trackID); ok {
		return StreamResult{Cached: true, FilePath: filePath}, nil
	}

	download := m.getOrStartDownload(trackID)

	if _, _, err := download.WaitForExt(); err != nil {
		return StreamResult{}, err
	}
	if filePath, finished := download.snapshot(); finished {
		// Finished between WaitForExt returning and here — the cached,
		// Range-capable path is strictly better than tailing a reader
		// that's about to hit EOF anyway.
		return StreamResult{Cached: true, FilePath: filePath}, nil
	}

	reader, err := download.Reader()
	if err != nil {
		return StreamResult{}, err
	}
	_, contentType, _ := download.WaitForExt()
	return StreamResult{Cached: false, ContentType: contentType, Reader: reader}, nil
}

package stream

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/catalog"
)

func newTestManagerWithCachedTrack(t *testing.T, trackID, content string) *Manager {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, dir+"/"+trackID+".opus", content)
	deezer := newTestDeezerTrackServer(t)
	return NewManager(t.Context(), dir, "", "", deezer)
}

func newDownloadRouter(manager *Manager) chi.Router {
	r := chi.NewRouter()
	r.Get("/api/download/{id}", handleDownload(manager))
	return r
}

func newStreamRouter(manager *Manager) chi.Router {
	r := chi.NewRouter()
	r.Get("/api/stream/{id}", handleStream(manager))
	return r
}

func TestHandleStreamServesLiveBytesOnSuccessfulFetch(t *testing.T) {
	fake := newFakeExecutable(t, `
case "$1" in
  ytsearch*)
    echo '{"id":"fake-video-id","title":"Master of Puppets","uploader":"Metallica","duration":515}'
    ;;
  *)
`+printToFileSidecarScript+`
    printf 'opus' > "$sidecar"
    printf 'hello '
    sleep 0.3
    printf 'world'
    ;;
esac
`)
	manager := NewManager(t.Context(), t.TempDir(), fake, "", newTestDeezerTrackServer(t))
	r := newStreamRouter(manager)

	req := httptest.NewRequest(http.MethodGet, "/api/stream/123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, attendu 200, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Accept-Ranges"); got != "none" {
		t.Errorf("Accept-Ranges = %q, attendu none (flux en direct, pas encore seekable — la pause de 300ms avant le second morceau doit largement dépasser extPollInterval, pour que la requête attrape le fichier encore en cours)", got)
	}
	if got := w.Body.String(); got != "hello world" {
		t.Errorf("body = %q, attendu %q", got, "hello world")
	}
}

// TestHandleStreamReturns502WhenFetchProducesNoBytes guards the regression
// this fix closes: a fetch that fails right after picking a format (real
// example: YouTube 403ing the actual download while the format list still
// resolved) used to still answer 200 with an empty body — indistinguishable
// from a genuinely playable but silent track. It must answer a real error
// instead, so the client can retry/skip rather than "playing" nothing.
func TestHandleStreamReturns502WhenFetchProducesNoBytes(t *testing.T) {
	fake := newFakeExecutable(t, `
case "$1" in
  ytsearch*)
    echo '{"id":"fake-video-id","title":"Master of Puppets","uploader":"Metallica","duration":515}'
    ;;
  *)
`+printToFileSidecarScript+`
    printf 'opus' > "$sidecar"
    sleep 0.3
    echo "ERROR: unable to download video data: HTTP Error 403: Forbidden" >&2
    exit 1
    ;;
esac
`)
	manager := NewManager(t.Context(), t.TempDir(), fake, "", newTestDeezerTrackServer(t))
	r := newStreamRouter(manager)

	req := httptest.NewRequest(http.MethodGet, "/api/stream/123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, attendu 502 (jamais un 200 vide)", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("body vide — attendu un message d'erreur exploitable par le client")
	}
}

func TestHandleDownloadRejectsInvalidTrackID(t *testing.T) {
	manager := newTestManagerWithCachedTrack(t, "123", "audio")
	r := newDownloadRouter(manager)

	req := httptest.NewRequest(http.MethodGet, "/api/download/not-a-number", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, attendu 400", w.Code)
	}
}

func TestHandleDownloadTranscodesCachedTrack(t *testing.T) {
	manager := newTestManagerWithCachedTrack(t, "123", "fake cached audio bytes")
	manager.ffmpegPath = newFakeExecutable(t, `
# Echo back a fixed payload regardless of the real input — proves the
# handler wires stdin/stdout/args correctly without needing real ffmpeg
# or a real audio file.
printf 'fake mp3 bytes'
`)
	r := newDownloadRouter(manager)

	req := httptest.NewRequest(http.MethodGet, "/api/download/123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, attendu 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("Content-Type = %q, attendu audio/mpeg", got)
	}
	if got := w.Body.String(); got != "fake mp3 bytes" {
		t.Errorf("body = %q, attendu %q", got, "fake mp3 bytes")
	}
}

func TestHandleDownloadFailsWhenTrackCannotBeResolved(t *testing.T) {
	// No cached file and a Deezer stub that answers 404 for every track —
	// ResolveFile can't even find metadata to start a download from.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	deezer := catalog.NewDeezerClientForTesting(server.Client(), server.URL)
	manager := NewManager(t.Context(), t.TempDir(), "", "", deezer)
	r := newDownloadRouter(manager)

	req := httptest.NewRequest(http.MethodGet, "/api/download/999999999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, attendu 502", w.Code)
	}
}

func TestHandleDownloadReturns502WhenFfmpegBinaryMissing(t *testing.T) {
	manager := newTestManagerWithCachedTrack(t, "123", "audio")
	manager.ffmpegPath = t.TempDir() + "/does-not-exist"
	r := newDownloadRouter(manager)

	req := httptest.NewRequest(http.MethodGet, "/api/download/123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, attendu 502", w.Code)
	}
}

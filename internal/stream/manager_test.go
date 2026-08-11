package stream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucasbouet/spotlab-go/internal/catalog"
)

// newFakeSearchAndDownloadYtDlp handles both invocation shapes Manager
// makes through the same configured binary: a `ytsearchN:...` search
// (FindBestMatch) and a real download (`<url> -o - ... --print-to-file`).
func newFakeSearchAndDownloadYtDlp(t *testing.T) string {
	t.Helper()
	return newFakeExecutable(t, `
case "$1" in
  ytsearch*)
    echo '{"id":"fake-video-id","title":"Master of Puppets","uploader":"Metallica","duration":515}'
    ;;
  *)
`+printToFileSidecarScript+`
    printf 'opus' > "$sidecar"
    sleep 0.03
    printf 'hello world'
    ;;
esac
`)
}

func newTestDeezerTrackServer(t *testing.T) *catalog.DeezerClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":123,"title":"Master of Puppets","duration":515,"artist":{"name":"Metallica"}}`))
	}))
	t.Cleanup(server.Close)
	return catalog.NewDeezerClientForTesting(server.Client(), server.URL)
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	fake := newFakeSearchAndDownloadYtDlp(t)
	deezer := newTestDeezerTrackServer(t)
	return NewManager(context.Background(), t.TempDir(), fake, deezer)
}

// newFakeBlockingDownloadYtDlp writes "hello " immediately after the
// sidecar, then blocks until markerPath exists before writing "world" and
// exiting. This lets a test deterministically control exactly when the
// fake download finishes, instead of racing a fixed sleep duration against
// however long the test goroutine takes to run its assertions (which
// flaked under load: see the mid-flight Cached=false assertion below).
func newFakeBlockingDownloadYtDlp(t *testing.T, markerPath string) string {
	t.Helper()
	return newFakeExecutable(t, fmt.Sprintf(`
case "$1" in
  ytsearch*)
    echo '{"id":"fake-video-id","title":"Master of Puppets","uploader":"Metallica","duration":515}'
    ;;
  *)
%s
    printf 'opus' > "$sidecar"
    printf 'hello '
    i=0
    while [ ! -f %q ] && [ $i -lt 100 ]; do
      sleep 0.05
      i=$((i+1))
    done
    printf 'world'
    ;;
esac
`, printToFileSidecarScript, markerPath))
}

func TestOpenStreamOnColdCacheStreamsThenSecondCallServesFromCache(t *testing.T) {
	destDir := t.TempDir()
	marker := filepath.Join(destDir, "go-signal")
	fake := newFakeBlockingDownloadYtDlp(t, marker)
	deezer := newTestDeezerTrackServer(t)
	m := NewManager(context.Background(), destDir, fake, deezer)

	result, err := m.OpenStream("123")
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if result.Cached {
		t.Fatal("premier appel: attendu Cached=false (rien en cache au départ)")
	}
	if result.ContentType != "audio/opus" {
		t.Errorf("ContentType = %q, attendu audio/opus", result.ContentType)
	}

	// The fake process is blocked before writing "world" — proves bytes
	// are readable from a genuinely in-progress download, not just after
	// the fact from a finished file.
	firstChunk := make([]byte, len("hello "))
	if _, err := io.ReadFull(result.Reader, firstChunk); err != nil {
		t.Fatalf("lecture du premier segment: %v", err)
	}
	if string(firstChunk) != "hello " {
		t.Errorf("premier segment = %q, attendu %q", firstChunk, "hello ")
	}

	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatalf("écriture du marqueur: %v", err)
	}
	rest := readAllAndClose(t, result.Reader)
	if string(rest) != "world" {
		t.Errorf("reste = %q, attendu %q", rest, "world")
	}

	// The download is dedup-tracked until it finishes; wait for it to
	// leave the in-flight table before checking the cache.
	waitUntil(t, func() bool { return m.inFlightCount() == 0 })

	second, err := m.OpenStream("123")
	if err != nil {
		t.Fatalf("deuxième OpenStream: %v", err)
	}
	if !second.Cached {
		t.Error("deuxième appel: attendu Cached=true, le fichier est maintenant en cache")
	}
}

func TestResolveFileBlocksUntilCachedAndReturnsFinalPath(t *testing.T) {
	m := newTestManager(t)

	filePath, err := m.ResolveFile("123")
	if err != nil {
		t.Fatalf("ResolveFile: %v", err)
	}
	body := mustReadFile(t, filePath)
	if string(body) != "hello world" {
		t.Errorf("body = %q", body)
	}

	// A second call must now take the already-cached path with no error.
	second, err := m.ResolveFile("123")
	if err != nil || second != filePath {
		t.Errorf("deuxième ResolveFile = %q/%v, attendu %q/nil", second, err, filePath)
	}
}

func TestConcurrentRequestsForSameTrackShareOneDownload(t *testing.T) {
	fake := newFakeExecutable(t, `
case "$1" in
  ytsearch*)
    echo '{"id":"fake-video-id","title":"Master of Puppets","uploader":"Metallica","duration":515}'
    ;;
  *)
`+printToFileSidecarScript+`
    printf 'opus' > "$sidecar"
    sleep 0.1
    printf 'hello world'
    ;;
esac
`)
	var deezerHits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&deezerHits, 1)
		w.Write([]byte(`{"id":123,"title":"Master of Puppets","duration":515,"artist":{"name":"Metallica"}}`))
	}))
	defer server.Close()
	deezer := catalog.NewDeezerClientForTesting(server.Client(), server.URL)
	m := NewManager(context.Background(), t.TempDir(), fake, deezer)

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]StreamResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = m.OpenStream("123")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("OpenStream[%d]: %v", i, err)
		}
		readAllAndClose(t, results[i].Reader)
	}

	// Every concurrent caller resolved metadata through the same Deezer
	// call/download rather than each kicking off its own — the whole
	// point of the in-flight dedup table.
	if got := atomic.LoadInt64(&deezerHits); got != 1 {
		t.Errorf("appels Deezer = %d, attendu 1 (dédupliqués)", got)
	}
}

func readAllAndClose(t *testing.T, r interface {
	Read(p []byte) (int, error)
	Close() error
}) []byte {
	t.Helper()
	defer r.Close()
	buf := make([]byte, 0, 64)
	tmp := make([]byte, 32)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("lecture de %s: %v", path, err)
	}
	return body
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout en attendant la condition")
}

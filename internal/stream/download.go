// Package stream is the audio pipeline: matching a Deezer track to a
// YouTube video (match.go), spawning yt-dlp and caching its output
// (pipeline.go), and serving it over HTTP either from the finished cache
// file (Range-seekable) or by tailing an in-progress download (not yet
// seekable) — GET /api/stream/{id} and POST /api/prefetch/{id}
// (docs/PLAN.md §4, Phase 8).
package stream

import (
	"io"
	"os"
	"sync"
)

// Download represents one in-flight (or already-completed) attempt to
// cache a track: bytes accumulate in a file on disk while contentType and
// bytesWritten track progress, and a single sync.Cond lets any number of
// readers (a live HTTP response tailing the file, ResolveFile blocking
// until it's done, a second request racing the first) wake up whenever
// there's more to read.
//
// This collapses the old server's three separate EventEmitter-based
// waiters (waitForExt/waitForDone/waitForProgress in youtube-audio.ts)
// into one condition variable, and turns the live-tail read loop into a
// plain io.Reader consumable by io.Copy instead of a hand-rolled async
// generator.
type Download struct {
	mu           sync.Mutex
	cond         *sync.Cond
	filePath     string
	contentType  string
	bytesWritten int64
	finished     bool
	err          error
}

func NewDownload() *Download {
	d := &Download{}
	d.cond = sync.NewCond(&d.mu)
	return d
}

// completedDownload wraps a file that's already fully cached in the same
// Download shape, so callers don't need to special-case "someone else
// just finished caching this" versus "still downloading" — mirrors
// completedDownload in youtube-audio.ts.
func completedDownload(filePath, contentType string, size int64) *Download {
	d := NewDownload()
	d.filePath = filePath
	d.contentType = contentType
	d.bytesWritten = size
	d.finished = true
	return d
}

func (d *Download) setExt(filePath, contentType string) {
	d.mu.Lock()
	d.filePath, d.contentType = filePath, contentType
	d.cond.Broadcast()
	d.mu.Unlock()
}

func (d *Download) addBytes(n int64) {
	if n == 0 {
		return
	}
	d.mu.Lock()
	d.bytesWritten += n
	d.cond.Broadcast()
	d.mu.Unlock()
}

// fail records the download's terminal error. Only the first call sticks —
// once yt-dlp's exit is attributed to (say) a read failure, a later Wait()
// call reporting "process exited" shouldn't overwrite that with a less
// specific message.
func (d *Download) fail(err error) {
	d.mu.Lock()
	if d.err == nil {
		d.err = err
	}
	d.finished = true
	d.cond.Broadcast()
	d.mu.Unlock()
}

func (d *Download) renamePath(newPath string) {
	d.mu.Lock()
	d.filePath = newPath
	d.mu.Unlock()
}

func (d *Download) complete() {
	d.mu.Lock()
	d.finished = true
	d.cond.Broadcast()
	d.mu.Unlock()
}

// snapshot reads filePath/finished together, consistently.
func (d *Download) snapshot() (filePath string, finished bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.filePath, d.finished
}

// WaitForExt blocks until the target file/content-type is known (yt-dlp
// has picked a format and the file has been created) or the download has
// already failed.
func (d *Download) WaitForExt() (filePath, contentType string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for d.filePath == "" && d.err == nil {
		d.cond.Wait()
	}
	return d.filePath, d.contentType, d.err
}

// WaitForDone blocks until the download finishes, successfully or not,
// and returns the final file path.
func (d *Download) WaitForDone() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for !d.finished {
		d.cond.Wait()
	}
	if d.err != nil {
		return "", d.err
	}
	return d.filePath, nil
}

// Reader opens a live-tailing io.ReadCloser starting from byte 0, following
// along as new bytes are written to disk and stopping once the download
// finishes — tailTrackDownload in youtube-audio.ts, as a plain io.Reader
// instead of an async generator.
func (d *Download) Reader() (io.ReadCloser, error) {
	filePath, _, err := d.WaitForExt()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	return &tailReader{d: d, f: f}, nil
}

type tailReader struct {
	d        *Download
	f        *os.File
	position int64
}

func (t *tailReader) Read(p []byte) (int, error) {
	t.d.mu.Lock()
	for t.position >= t.d.bytesWritten && t.d.err == nil && !t.d.finished {
		t.d.cond.Wait()
	}
	bytesWritten, finished, err := t.d.bytesWritten, t.d.finished, t.d.err
	t.d.mu.Unlock()

	if t.position < bytesWritten {
		n, readErr := t.f.ReadAt(p, t.position)
		if n > 0 {
			t.position += int64(n)
			return n, nil
		}
		if readErr != nil && readErr != io.EOF {
			return 0, readErr
		}
	}
	if err != nil {
		return 0, err
	}
	if finished && t.position >= bytesWritten {
		return 0, io.EOF
	}
	// A spurious empty read: bytesWritten/finished/err changed between the
	// wakeup and the read above (e.g. a concurrent addBytes landed after
	// this ReadAt observed a stale bytesWritten). Loop back on the next
	// call rather than blocking here — the caller (io.Copy et al.) will
	// call Read again immediately.
	return 0, nil
}

func (t *tailReader) Close() error {
	return t.f.Close()
}

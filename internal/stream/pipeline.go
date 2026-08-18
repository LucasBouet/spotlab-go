package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// downloadTimeout bounds one yt-dlp invocation — a genuinely stuck process
// (network stall, YouTube throttling) must not leak forever. Not present
// in the old server at all (see docs/PLAN.md §4.1); a Go-side addition.
const downloadTimeout = 5 * time.Minute

// extPollInterval mirrors the 75ms setInterval in youtube-audio.ts that
// polls for the ext sidecar file yt-dlp writes right after format
// selection, before any audio bytes reach stdout.
const extPollInterval = 75 * time.Millisecond

// readChunkSize is how much of yt-dlp's stdout is read per Read() call —
// arbitrary but generous enough that polling/locking overhead is
// negligible next to actual I/O.
const readChunkSize = 256 * 1024

// writeTarget buffers stdout chunks until the ext sidecar reveals the
// target file, then flushes them and writes straight through — the Go
// equivalent of pendingChunks/writeStream in startDownload
// (youtube-audio.ts), just made safe for two goroutines (the stdout
// reader and the sidecar poller) touching it concurrently.
type writeTarget struct {
	mu       sync.Mutex
	file     *os.File
	partPath string
	pending  [][]byte
}

// tryOpen attempts to read the ext sidecar and, if it's there, creates the
// on-disk .part file and flushes any buffered chunks into it. Returns true
// once the file is open (on this call or a previous one).
func (w *writeTarget) tryOpen(download *Download, extSidecar, destDir, trackID string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return true, nil
	}

	raw, err := os.ReadFile(extSidecar)
	if err != nil {
		return false, nil // sidecar not written yet — normal, keep polling
	}
	ext := strings.TrimSpace(string(raw))
	if ext == "" {
		return false, nil
	}
	_ = os.Remove(extSidecar)

	partPath := filepath.Join(destDir, trackID+"."+ext+".part")
	f, err := os.Create(partPath)
	if err != nil {
		return false, err
	}
	w.file = f
	w.partPath = partPath

	contentType := contentTypeForExt(ext)

	var written int64
	for _, chunk := range w.pending {
		n, writeErr := f.Write(chunk)
		written += int64(n)
		if writeErr != nil {
			w.pending = nil
			download.setExt(partPath, contentType)
			download.addBytes(written)
			return true, writeErr
		}
	}
	w.pending = nil
	download.setExt(partPath, contentType)
	download.addBytes(written)
	return true, nil
}

func (w *writeTarget) write(chunk []byte, download *Download) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		n, err := w.file.Write(chunk)
		download.addBytes(int64(n))
		return err
	}
	buf := make([]byte, len(chunk))
	copy(buf, chunk)
	w.pending = append(w.pending, buf)
	return nil
}

// snapshot returns the open file (if any) and its part path, for the
// caller to close/rename once yt-dlp has exited.
func (w *writeTarget) snapshot() (*os.File, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file, w.partPath
}

// runDownload spawns yt-dlp for videoID and streams its stdout straight to
// <destDir>/<trackID>.<ext>.part, renaming to the final name on a clean
// exit — a direct port of startDownload in youtube-audio.ts, run
// synchronously (the caller is expected to already be on its own
// goroutine). ctx should be a long-lived, server-scoped context — NOT one
// tied to any single HTTP request, since a second reader may still be
// tailing this exact Download after the first caller's request ends (see
// manager.go's dedup table).
func runDownload(ctx context.Context, ytdlpPath, videoID, destDir, trackID string, download *Download) {
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}
	extSidecar := filepath.Join(destDir, trackID+".ext.tmp")
	_ = os.Remove(extSidecar) // a stale sidecar from a crashed previous attempt must not be misread

	downloadCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	args := append([]string{
		"https://www.youtube.com/watch?v=" + videoID,
		"-o", "-",
		"--format", "bestaudio/best",
		"--no-playlist",
		"--no-warnings",
		"--quiet",
		"--print-to-file", "%(ext)s", extSidecar,
	}, youtubeExtractorArgs...)
	cmd := exec.CommandContext(downloadCtx, ytdlpPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		download.fail(fmt.Errorf("le téléchargement du titre a échoué : %w", err))
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		download.fail(fmt.Errorf("le téléchargement du titre a échoué : %w", err))
		return
	}

	target := &writeTarget{}
	pollDone := make(chan struct{})
	stopPolling := make(chan struct{})
	var pollErr error
	go func() {
		defer close(pollDone)
		ticker := time.NewTicker(extPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-downloadCtx.Done():
				return
			case <-stopPolling:
				return
			case <-ticker.C:
				opened, err := target.tryOpen(download, extSidecar, destDir, trackID)
				if err != nil {
					pollErr = err
					return
				}
				if opened {
					return
				}
			}
		}
	}()

	buf := make([]byte, readChunkSize)
	var readErr error
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			if writeErr := target.write(buf[:n], download); writeErr != nil && readErr == nil {
				readErr = writeErr
			}
		}
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}
	// yt-dlp's stdout has EOF'd — it has already exited or is about to.
	// Stop polling immediately rather than potentially waiting up to
	// downloadTimeout: a process that exits fast without ever picking a
	// format (e.g. an invalid video id) would otherwise stall here for the
	// full timeout instead of surfacing cmd.Wait()'s real error below.
	close(stopPolling)
	<-pollDone
	// One last synchronous attempt: the process may have finished (and
	// written its sidecar) in the gap between the poller's last tick and
	// stopPolling taking effect.
	if opened, err := target.tryOpen(download, extSidecar, destDir, trackID); err == nil && opened {
		// nothing to do — already flushed
	} else if err != nil && pollErr == nil {
		pollErr = err
	}

	waitErr := cmd.Wait()
	file, partPath := target.snapshot()

	if readErr != nil {
		if file != nil {
			file.Close()
		}
		download.fail(fmt.Errorf("le téléchargement du titre a échoué : %w", readErr))
		return
	}
	if pollErr != nil {
		if file != nil {
			file.Close()
		}
		download.fail(fmt.Errorf("le téléchargement du titre a échoué : %w", pollErr))
		return
	}
	if waitErr != nil {
		if file != nil {
			file.Close()
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = waitErr.Error()
		}
		download.fail(fmt.Errorf("le téléchargement du titre a échoué : %s", detail))
		return
	}
	if file == nil || partPath == "" {
		download.fail(errors.New("yt-dlp n'a produit aucun fichier audio"))
		return
	}
	if err := file.Close(); err != nil {
		download.fail(fmt.Errorf("le téléchargement du titre a échoué : %w", err))
		return
	}

	finalPath := strings.TrimSuffix(partPath, ".part")
	if err := os.Rename(partPath, finalPath); err != nil {
		download.fail(fmt.Errorf("le téléchargement du titre a échoué : %w", err))
		return
	}
	download.renamePath(finalPath)
	download.complete()
}

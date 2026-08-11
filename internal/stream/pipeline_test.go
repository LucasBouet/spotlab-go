package stream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// printToFileSidecarScript is the shell snippet every fake yt-dlp download
// script below shares: parse `--print-to-file %(ext)s <path>` out of the
// argument list and write the fake extension to it, exactly like real
// yt-dlp does right after picking a format.
const printToFileSidecarScript = `
state=0
sidecar=""
for arg in "$@"; do
  case "$state" in
    0) if [ "$arg" = "--print-to-file" ]; then state=1; fi ;;
    1) state=2 ;;
    2) sidecar="$arg"; state=3 ;;
  esac
done
`

func TestRunDownloadWritesFileAndCompletesOnCleanExit(t *testing.T) {
	fake := newFakeExecutable(t, printToFileSidecarScript+`
printf 'opus' > "$sidecar"
sleep 0.05
printf 'hello '
sleep 0.05
printf 'world'
`)

	destDir := t.TempDir()
	download := NewDownload()
	runDownload(context.Background(), fake, "fake-video-id", destDir, "123", download)

	finalPath, err := download.WaitForDone()
	if err != nil {
		t.Fatalf("WaitForDone: %v", err)
	}
	if filepath.Base(finalPath) != "123.opus" {
		t.Errorf("finalPath = %q, attendu 123.opus", finalPath)
	}
	body, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "hello world" {
		t.Errorf("body = %q", body)
	}
	if _, err := os.Stat(finalPath + ".part"); !os.IsNotExist(err) {
		t.Error("le fichier .part aurait dû être renommé, pas laissé en place")
	}
	if _, err := os.Stat(filepath.Join(destDir, "123.ext.tmp")); !os.IsNotExist(err) {
		t.Error("le sidecar .ext.tmp aurait dû être supprimé après lecture")
	}
}

func TestRunDownloadFailsWhenProcessExitsNonZero(t *testing.T) {
	fake := newFakeExecutable(t, `echo "ERROR: unavailable" >&2; exit 1`)

	download := NewDownload()
	runDownload(context.Background(), fake, "fake-video-id", t.TempDir(), "123", download)

	_, err := download.WaitForDone()
	if err == nil {
		t.Fatal("attendu une erreur")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("err = %v, attendu qu'il contienne le stderr de yt-dlp", err)
	}
}

func TestRunDownloadFailsFastWhenNoSidecarIsEverWritten(t *testing.T) {
	fake := newFakeExecutable(t, `exit 0`) // exits clean but never picks a format

	download := NewDownload()
	start := time.Now()
	runDownload(context.Background(), fake, "fake-video-id", t.TempDir(), "123", download)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("runDownload a pris %v — la relance du sondage du sidecar après EOF de stdout ne fonctionne pas, ça a attendu downloadTimeout", elapsed)
	}
	if _, err := download.WaitForDone(); err == nil {
		t.Fatal("attendu une erreur quand aucun sidecar n'est jamais écrit")
	}
}

func TestRunDownloadIsInterruptibleViaContext(t *testing.T) {
	// `exec sleep` replaces the shell's own process image instead of
	// forking a child — sleep then runs as the exact PID exec.Cmd is
	// tracking, so killing that PID actually stops it. A plain `sleep 30`
	// would fork sleep as a grandchild that inherits the stdout pipe fd;
	// killing the shell wouldn't close that fd, and the test would hang
	// until the real 30s elapsed regardless of context cancellation.
	fake := newFakeExecutable(t, printToFileSidecarScript+`
printf 'opus' > "$sidecar"
exec sleep 30
`)

	ctx, cancel := context.WithCancel(context.Background())
	download := NewDownload()

	done := make(chan struct{})
	go func() {
		runDownload(ctx, fake, "fake-video-id", t.TempDir(), "123", download)
		close(done)
	}()

	if _, _, err := download.WaitForExt(); err != nil {
		t.Fatalf("WaitForExt: %v", err)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: annuler le contexte n'a pas arrêté le téléchargement")
	}
	if _, err := download.WaitForDone(); err == nil {
		t.Error("attendu une erreur après annulation du contexte")
	}
}

package stream

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForExtBlocksUntilSetThenReturnsImmediately(t *testing.T) {
	d := NewDownload()
	done := make(chan struct{})
	go func() {
		filePath, contentType, err := d.WaitForExt()
		if err != nil {
			t.Errorf("WaitForExt: %v", err)
		}
		if filePath != "/tmp/x.opus" || contentType != "audio/opus" {
			t.Errorf("got %q/%q", filePath, contentType)
		}
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("WaitForExt a retourné avant setExt")
	case <-time.After(50 * time.Millisecond):
	}

	d.setExt("/tmp/x.opus", "audio/opus")

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout: WaitForExt n'a pas débloqué après setExt")
	}

	// A second call after the fact must return immediately with the same values.
	filePath, contentType, err := d.WaitForExt()
	if err != nil || filePath != "/tmp/x.opus" || contentType != "audio/opus" {
		t.Errorf("deuxième WaitForExt = %q/%q/%v", filePath, contentType, err)
	}
}

func TestWaitForExtReturnsErrorOnFailBeforeExtKnown(t *testing.T) {
	d := NewDownload()
	boom := errors.New("boom")
	d.fail(boom)

	_, _, err := d.WaitForExt()
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, attendu %v", err, boom)
	}
}

func TestWaitForDoneBlocksUntilCompleteAndReturnsFinalPath(t *testing.T) {
	d := NewDownload()
	d.setExt("/tmp/x.opus", "audio/opus")

	done := make(chan struct{})
	var gotPath string
	var gotErr error
	go func() {
		gotPath, gotErr = d.WaitForDone()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("WaitForDone a retourné avant complete()")
	case <-time.After(50 * time.Millisecond):
	}

	d.renamePath("/tmp/x-final.opus")
	d.complete()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout: WaitForDone n'a pas débloqué après complete()")
	}
	if gotErr != nil || gotPath != "/tmp/x-final.opus" {
		t.Errorf("WaitForDone = %q/%v", gotPath, gotErr)
	}
}

func TestWaitForDoneReturnsErrorOnFail(t *testing.T) {
	d := NewDownload()
	boom := errors.New("boom")
	d.fail(boom)

	_, err := d.WaitForDone()
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, attendu %v", err, boom)
	}
}

func TestCompletedDownloadIsImmediatelyDoneAndReadable(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "123.opus")
	mustWrite(t, filePath, "hello world")

	d := completedDownload(filePath, "audio/opus", 11)
	fp, finished := d.snapshot()
	if fp != filePath || !finished {
		t.Errorf("snapshot = %q/%v, attendu déjà terminé", fp, finished)
	}

	reader, err := d.Reader()
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != "hello world" {
		t.Errorf("body = %q", body)
	}
}

// TestTailReaderFollowsLiveWritesAndStopsAtEOF is the core correctness
// property of the whole live-tail design (docs/PLAN.md §4.1): a reader
// started before the download finishes must see bytes as they're written,
// block when caught up, and cleanly hit EOF once the writer calls
// complete() — proven under -race like the sync package's Hub tests.
func TestTailReaderFollowsLiveWritesAndStopsAtEOF(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "123.opus.part")
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatalf("création du fichier: %v", err)
	}

	d := NewDownload()
	d.setExt(filePath, "audio/opus")

	reader, err := d.Reader()
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	defer reader.Close()

	readAll := make(chan []byte, 1)
	readErrCh := make(chan error, 1)
	go func() {
		body, err := io.ReadAll(reader)
		readAll <- body
		readErrCh <- err
	}()

	// Write in two bursts with a pause between, to prove the reader is
	// actually blocking on new data rather than having read everything at
	// once from a file that happened to already be complete.
	write := func(s string) {
		n, err := f.WriteString(s)
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		d.addBytes(int64(n))
	}
	write("hello ")
	time.Sleep(30 * time.Millisecond)
	write("world")
	time.Sleep(30 * time.Millisecond)

	select {
	case <-readAll:
		t.Fatal("le lecteur a terminé avant complete() — il n'attendait pas les nouvelles données")
	default:
	}

	f.Close()
	d.complete()

	select {
	case body := <-readAll:
		if string(body) != "hello world" {
			t.Errorf("body = %q, attendu %q", body, "hello world")
		}
		if err := <-readErrCh; err != nil {
			t.Errorf("erreur de lecture = %v, attendu nil (EOF propre)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: le lecteur n'a jamais atteint EOF après complete()")
	}
}

func TestTailReaderPropagatesDownloadFailure(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "123.opus.part")
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatalf("création du fichier: %v", err)
	}
	defer f.Close()

	d := NewDownload()
	d.setExt(filePath, "audio/opus")

	reader, err := d.Reader()
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	defer reader.Close()

	readErrCh := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(reader)
		readErrCh <- err
	}()

	boom := errors.New("yt-dlp a planté")
	d.fail(boom)

	select {
	case err := <-readErrCh:
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, attendu %v", err, boom)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: le lecteur n'a jamais vu l'échec")
	}
}

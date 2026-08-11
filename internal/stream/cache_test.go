package stream

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsIncompleteCacheFile(t *testing.T) {
	cases := map[string]bool{
		"123.opus.part":  true,
		"123.opus.ytdl":  true,
		"123.ext.tmp":    true,
		"123.opus":       false,
		"123.mp3":        false,
		"123.opus.part2": false, // suffix must match exactly, not just contain
	}
	for name, want := range cases {
		if got := isIncompleteCacheFile(name); got != want {
			t.Errorf("isIncompleteCacheFile(%q) = %v, attendu %v", name, got, want)
		}
	}
}

func TestFindExistingCacheFileIgnoresIncompleteAndOtherTracks(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "123.opus"), "audio bytes")
	mustWrite(t, filepath.Join(dir, "456.opus.part"), "partial")
	mustWrite(t, filepath.Join(dir, "1234.opus"), "a different track that happens to share a prefix")

	filePath, ok := findExistingCacheFile(dir, "123")
	if !ok {
		t.Fatal("attendu de trouver le fichier en cache pour 123")
	}
	if filepath.Base(filePath) != "123.opus" {
		t.Errorf("filePath = %q, attendu 123.opus", filePath)
	}

	if _, ok := findExistingCacheFile(dir, "456"); ok {
		t.Error("456 n'a qu'un .part — ne doit pas être considéré comme en cache")
	}
	if _, ok := findExistingCacheFile(dir, "789"); ok {
		t.Error("789 n'existe pas du tout")
	}
}

func TestFindExistingCacheFileOnMissingDir(t *testing.T) {
	if _, ok := findExistingCacheFile(filepath.Join(t.TempDir(), "does-not-exist"), "123"); ok {
		t.Error("un dossier de cache inexistant doit se comporter comme vide, pas paniquer")
	}
}

func TestContentTypeForPath(t *testing.T) {
	cases := map[string]string{
		"123.mp3":     "audio/mpeg",
		"123.opus":    "audio/opus",
		"123.webm":    "audio/webm",
		"123.UNKNOWN": "application/octet-stream",
		"noext":       "application/octet-stream",
	}
	for path, want := range cases {
		if got := contentTypeForPath(path); got != want {
			t.Errorf("contentTypeForPath(%q) = %q, attendu %q", path, got, want)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("écriture de %s: %v", path, err)
	}
}

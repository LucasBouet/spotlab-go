package stream

import (
	"os"
	"path/filepath"
	"strings"
)

// incompleteSuffixes are sidecar/partial files yt-dlp (or our own pipeline)
// leaves next to a track id while a download is in flight or has failed —
// never mistake one of these for a finished cache file. Mirrors
// INCOMPLETE_SUFFIXES in youtube-audio.ts.
var incompleteSuffixes = []string{".part", ".ytdl", ".ext.tmp"}

func isIncompleteCacheFile(name string) bool {
	for _, suffix := range incompleteSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// findExistingCacheFile mirrors findExistingCacheFile in stream.ts: a
// completed cache file for trackID is named "<trackID>.<ext>" — any
// sidecar/partial file for the same id must never be mistaken for one.
func findExistingCacheFile(cacheDir, trackID string) (string, bool) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return "", false
	}
	prefix := trackID + "."
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || isIncompleteCacheFile(name) {
			continue
		}
		filePath := filepath.Join(cacheDir, name)
		if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
			return filePath, true
		}
	}
	return "", false
}

// contentTypesByExt matches CONTENT_TYPES in both stream.ts and
// youtube-audio.ts — kept as one table here since Go doesn't need the two
// separate dot/no-dot forms those two files used.
var contentTypesByExt = map[string]string{
	".mp3":  "audio/mpeg",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".ogg":  "audio/ogg",
	".opus": "audio/opus",
	".wav":  "audio/wav",
	".webm": "audio/webm",
}

func contentTypeForPath(filePath string) string {
	return contentTypeForExt(filepath.Ext(filePath))
}

// contentTypeForExt looks up a content type from a bare extension (with or
// without a leading dot). Needed separately from contentTypeForPath
// because a .part file's path has two extensions (e.g. "123.opus.part") —
// filepath.Ext on that yields ".part", not ".opus". youtube-audio.ts has
// the same split for the same reason: it looks up CONTENT_TYPES[ext] using
// the ext captured straight from the sidecar, never by re-deriving it from
// the .part path.
func contentTypeForExt(ext string) string {
	ext = strings.ToLower(ext)
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if ct, ok := contentTypesByExt[ext]; ok {
		return ct
	}
	return "application/octet-stream"
}

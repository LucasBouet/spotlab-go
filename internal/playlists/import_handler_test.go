package playlists

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

func TestHandleImportStreamsProgressThenDone(t *testing.T) {
	conn, queries, userID := newTestDB(t)
	deezer := newSingleTrackPlaylistServer(t)
	user := db.User{ID: userID}

	req := httptest.NewRequest(http.MethodPost, "/api/playlists/import",
		strings.NewReader(`{"link":"1","destination":"playlist","name":"My Import"}`))
	req = req.WithContext(auth.ContextWithUserForTesting(context.Background(), user))
	w := httptest.NewRecorder()

	handleImport(conn, queries, deezer, http.DefaultClient)(w, req)

	if got := w.Header().Get("Content-Type"); got != "application/x-ndjson; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}

	var lastEvent map[string]any
	scanner := bufio.NewScanner(strings.NewReader(w.Body.String()))
	var sawProgress bool
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("ligne NDJSON invalide %q: %v", line, err)
		}
		if event["type"] == "progress" {
			sawProgress = true
		}
		lastEvent = event
	}
	if !sawProgress {
		t.Error("attendu au moins un événement progress")
	}
	if lastEvent["type"] != "done" {
		t.Fatalf("dernier événement = %+v, attendu type=done", lastEvent)
	}
	if lastEvent["destination"] != "playlist" {
		t.Errorf("destination = %v, attendu playlist", lastEvent["destination"])
	}
	if lastEvent["trackCount"].(float64) != 1 {
		t.Errorf("trackCount = %v, attendu 1", lastEvent["trackCount"])
	}
	if lastEvent["playlistId"] == nil || lastEvent["playlistId"] == "" {
		t.Error("attendu un playlistId non vide dans l'événement done")
	}
}

func TestHandleImportStreamsErrorEventOnFailure(t *testing.T) {
	conn, queries, userID := newTestDB(t)
	user := db.User{ID: userID}

	req := httptest.NewRequest(http.MethodPost, "/api/playlists/import",
		strings.NewReader(`{"link":"","destination":"playlist"}`))
	req = req.WithContext(auth.ContextWithUserForTesting(context.Background(), user))
	w := httptest.NewRecorder()

	handleImport(conn, queries, nil, http.DefaultClient)(w, req)

	var lastEvent map[string]any
	scanner := bufio.NewScanner(strings.NewReader(w.Body.String()))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &lastEvent); err != nil {
			t.Fatalf("ligne NDJSON invalide %q: %v", line, err)
		}
	}
	if lastEvent["type"] != "error" {
		t.Fatalf("événement = %+v, attendu type=error", lastEvent)
	}
}

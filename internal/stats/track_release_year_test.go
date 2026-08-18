package stats

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"
)

func TestEnsureTrackReleaseYearParsesAndCachesYearFromTrack(t *testing.T) {
	queries, _ := newTestQueries(t)
	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":3135556,"release_date":"2001-03-12","album":{"release_date":"2001-03-07"}}`))
	})

	ensureTrackReleaseYear(context.Background(), queries, deezer, 3135556)

	got, err := queries.GetTrackReleaseYear(context.Background(), 3135556)
	if err != nil {
		t.Fatalf("GetTrackReleaseYear: %v", err)
	}
	if got.Year != 2001 {
		t.Errorf("Year = %d, attendu 2001 (depuis release_date au niveau du morceau, pas de l'album)", got.Year)
	}
}

func TestEnsureTrackReleaseYearFallsBackToAlbumDate(t *testing.T) {
	queries, _ := newTestQueries(t)
	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":1,"album":{"release_date":"1999-11-01"}}`))
	})

	ensureTrackReleaseYear(context.Background(), queries, deezer, 1)

	got, err := queries.GetTrackReleaseYear(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetTrackReleaseYear: %v", err)
	}
	if got.Year != 1999 {
		t.Errorf("Year = %d, attendu 1999 (repli sur la date de l'album)", got.Year)
	}
}

func TestEnsureTrackReleaseYearLeavesUnresolvedOnImplausibleOrMissingDate(t *testing.T) {
	cases := map[string]string{
		"aucune date":       `{"id":1}`,
		"date vide":         `{"id":1,"release_date":""}`,
		"année implausible": `{"id":1,"release_date":"1850-01-01"}`,
		"date tronquée":     `{"id":1,"release_date":"20"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			queries, _ := newTestQueries(t)
			deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(body))
			})

			ensureTrackReleaseYear(context.Background(), queries, deezer, 1)

			if _, err := queries.GetTrackReleaseYear(context.Background(), 1); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("attendu aucune ligne écrite pour %q, err = %v", name, err)
			}
		})
	}
}

func TestEnsureTrackReleaseYearSkipsAlreadyResolvedTrack(t *testing.T) {
	queries, _ := newTestQueries(t)
	callCount := 0
	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write([]byte(`{"id":1,"release_date":"2010-01-01"}`))
	})

	ensureTrackReleaseYear(context.Background(), queries, deezer, 1)
	ensureTrackReleaseYear(context.Background(), queries, deezer, 1)

	if callCount != 1 {
		t.Errorf("Deezer appelé %d fois, attendu 1 (déjà résolu au deuxième appel)", callCount)
	}
}

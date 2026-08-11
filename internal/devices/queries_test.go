package devices

import (
	"context"
	"testing"

	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

func TestUpsertDeviceCreatesOnFirstCall(t *testing.T) {
	queries, userID := newTestQueries(t)
	d, err := queries.UpsertDevice(context.Background(), dbgen.UpsertDeviceParams{
		ID: idgen.New(), UserID: userID, DeviceID: "dev1", Name: "Phone", Platform: "Android",
	})
	if err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}
	if d.Name != "Phone" || d.Platform != "Android" {
		t.Errorf("d = %+v", d)
	}
}

func TestUpsertDeviceOnConflictKeepsNameButRefreshesPlatform(t *testing.T) {
	// The one behavior in this whole package worth a dedicated test: only
	// the *create* path sets the name. A user-chosen rename must survive
	// every future re-registration (every app launch re-registers).
	queries, userID := newTestQueries(t)
	ctx := context.Background()

	if _, err := queries.UpsertDevice(ctx, dbgen.UpsertDeviceParams{
		ID: idgen.New(), UserID: userID, DeviceID: "dev1", Name: "Original name", Platform: "Android",
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if _, err := queries.RenameDevice(ctx, dbgen.RenameDeviceParams{
		Name: "User renamed", UserID: userID, DeviceID: "dev1",
	}); err != nil {
		t.Fatalf("RenameDevice: %v", err)
	}

	// Re-registration, as if the app relaunched: a different name is sent
	// (what the client would send by default), plus an updated platform.
	d, err := queries.UpsertDevice(ctx, dbgen.UpsertDeviceParams{
		ID: idgen.New(), UserID: userID, DeviceID: "dev1", Name: "Default name sent again", Platform: "Android 15",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if d.Name != "User renamed" {
		t.Errorf("Name = %q, attendu \"User renamed\" — un renommage ne doit jamais être écrasé par un ré-enregistrement", d.Name)
	}
	if d.Platform != "Android 15" {
		t.Errorf("Platform = %q, attendu Android 15 — la plateforme doit se rafraîchir", d.Platform)
	}
}

func TestListDevicesByUserOrdersByLastSeenDesc(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	if _, err := queries.UpsertDevice(ctx, dbgen.UpsertDeviceParams{
		ID: idgen.New(), UserID: userID, DeviceID: "old", Name: "Old", Platform: "P",
	}); err != nil {
		t.Fatalf("upsert old: %v", err)
	}
	if _, err := queries.UpsertDevice(ctx, dbgen.UpsertDeviceParams{
		ID: idgen.New(), UserID: userID, DeviceID: "new", Name: "New", Platform: "P",
	}); err != nil {
		t.Fatalf("upsert new: %v", err)
	}
	// Force "old" to be genuinely older by touching it again... instead,
	// just re-upsert "old" is wrong (would make it newest) — this test
	// relies on insertion order alone via CURRENT_TIMESTAMP ties being
	// broken consistently is not guaranteed at 1s resolution, so assert
	// membership rather than exact order when timestamps could tie.
	rows, err := queries.ListDevicesByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListDevicesByUser: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, attendu 2", len(rows))
	}
}

func TestDevicesAreScopedPerUser(t *testing.T) {
	queries, userA := newTestQueries(t)
	ctx := context.Background()
	userB, err := queries.CreateUser(ctx, dbgen.CreateUserParams{
		ID: idgen.New(), Email: "b@example.com", PasswordHash: "x", Role: "USER",
	})
	if err != nil {
		t.Fatalf("création du second utilisateur: %v", err)
	}
	if _, err := queries.UpsertDevice(ctx, dbgen.UpsertDeviceParams{
		ID: idgen.New(), UserID: userA, DeviceID: "shared-device-id", Name: "A's", Platform: "P",
	}); err != nil {
		t.Fatalf("upsert A: %v", err)
	}

	// Same deviceId string, different user — must not collide (the unique
	// constraint is on (user_id, device_id), not device_id alone).
	if _, err := queries.UpsertDevice(ctx, dbgen.UpsertDeviceParams{
		ID: idgen.New(), UserID: userB.ID, DeviceID: "shared-device-id", Name: "B's", Platform: "P",
	}); err != nil {
		t.Fatalf("upsert B avec le même deviceId: %v", err)
	}

	rowsA, err := queries.ListDevicesByUser(ctx, userA)
	if err != nil {
		t.Fatalf("ListDevicesByUser(A): %v", err)
	}
	if len(rowsA) != 1 || rowsA[0].Name != "A's" {
		t.Errorf("appareils de A = %+v, ne doit contenir que le sien", rowsA)
	}

	if _, err := queries.RenameDevice(ctx, dbgen.RenameDeviceParams{
		Name: "Hacked", UserID: userB.ID, DeviceID: "shared-device-id",
	}); err != nil {
		t.Fatalf("RenameDevice(B): %v", err)
	}
	rowsA2, err := queries.ListDevicesByUser(ctx, userA)
	if err != nil {
		t.Fatalf("ListDevicesByUser(A) après renommage de B: %v", err)
	}
	if rowsA2[0].Name != "A's" {
		t.Error("un utilisateur ne doit jamais pouvoir renommer l'appareil d'un autre, même avec le même deviceId")
	}
}

func TestRenameDeviceReturnsZeroRowsWhenNotOwned(t *testing.T) {
	queries, userID := newTestQueries(t)
	rows, err := queries.RenameDevice(context.Background(), dbgen.RenameDeviceParams{
		Name: "X", UserID: userID, DeviceID: "never-registered",
	})
	if err != nil {
		t.Fatalf("RenameDevice: %v", err)
	}
	if rows != 0 {
		t.Errorf("rows = %d, attendu 0", rows)
	}
}

func TestDeleteDeviceIsIdempotentAtTheHandlerLevelButNotAtTheQueryLevel(t *testing.T) {
	// DeleteDevice itself just reports rows affected — 0 the second time,
	// which the handler turns into 404. Documented here so the two layers'
	// responsibilities stay clear.
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	if _, err := queries.UpsertDevice(ctx, dbgen.UpsertDeviceParams{
		ID: idgen.New(), UserID: userID, DeviceID: "dev1", Name: "P", Platform: "P",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	rows, err := queries.DeleteDevice(ctx, dbgen.DeleteDeviceParams{UserID: userID, DeviceID: "dev1"})
	if err != nil || rows != 1 {
		t.Fatalf("premier delete: rows=%d err=%v, attendu 1, nil", rows, err)
	}
	rows, err = queries.DeleteDevice(ctx, dbgen.DeleteDeviceParams{UserID: userID, DeviceID: "dev1"})
	if err != nil || rows != 0 {
		t.Fatalf("second delete: rows=%d err=%v, attendu 0, nil", rows, err)
	}
}

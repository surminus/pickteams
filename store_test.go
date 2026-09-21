package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func schemaVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestFreshDatabaseIsFullyMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	if got := schemaVersion(t, path); got != len(migrations) {
		t.Errorf("schema version is %d, want %d", got, len(migrations))
	}
}

// TestMigrationCatchesUpAnOlderDatabase sets up a database at the first
// version, with a game already in it, and checks that opening it adds the team
// name columns without losing anything.
func TestMigrationCatchesUpAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (played_on, created_at) VALUES ('2026-09-24', '2026-09-21T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO players (name, weighting, position) VALUES ('Ada', 4, 'DEF')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("opening a database from the first version: %v", err)
	}
	defer store.Close()

	if got := schemaVersion(t, path); got != len(migrations) {
		t.Errorf("schema version is %d, want %d", got, len(migrations))
	}

	sess, err := store.Session(1)
	if err != nil {
		t.Fatalf("reading the game that was already there: %v", err)
	}
	if sess.PlayedOn != "2026-09-24" {
		t.Errorf("the game is dated %q, the migration should not have touched it", sess.PlayedOn)
	}
	if sess.NameA() != DefaultTeamAName || sess.NameB() != DefaultTeamBName {
		t.Errorf("a game from before team names should fall back to the defaults, got %q and %q",
			sess.NameA(), sess.NameB())
	}

	if err := store.SetTeamNames(1, "Bibs", "Shirts"); err != nil {
		t.Fatal(err)
	}
	sess, _ = store.Session(1)
	if sess.NameA() != "Bibs" {
		t.Errorf("after renaming, the first side is %q", sess.NameA())
	}

	players, err := store.Players()
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 1 || players[0].Name != "Ada" || players[0].Weighting != 4 {
		t.Errorf("the migration disturbed the players: %+v", players)
	}
}

func TestMigratingTwiceChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSession("2026-09-24"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer store.Close()

	sessions, err := store.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Errorf("got %d games after reopening, want 1", len(sessions))
	}
	if got := schemaVersion(t, path); got != len(migrations) {
		t.Errorf("schema version is %d, want %d", got, len(migrations))
	}
}

// TestDatabaseFromANewerBuildIsRefused covers copying the file back from a
// machine running a later version: better to say so than to run queries
// against a schema we do not understand.
func TestDatabaseFromANewerBuildIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := OpenStore(path); err == nil {
		t.Fatal("expected opening a database from a newer build to fail")
	}
}

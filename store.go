package main

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database. The weighting column lives in the players
// table and is only ever read by the admin-side methods.
type Store struct {
	db *sql.DB
}

// migrations are applied in order and tracked by SQLite's own user_version,
// so a database only ever gets the steps it has not had yet. Never change one
// that has shipped, add another to the end instead.
var migrations = []string{
	// 1: the tables as they first stood.
	`
	CREATE TABLE IF NOT EXISTS players (
		id          INTEGER PRIMARY KEY,
		name        TEXT    NOT NULL UNIQUE,
		active      INTEGER NOT NULL DEFAULT 1,
		weighting   INTEGER NOT NULL DEFAULT 3,
		provisional INTEGER NOT NULL DEFAULT 1,
		position    TEXT    NOT NULL DEFAULT '',
		notes       TEXT    NOT NULL DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS sessions (
		id         INTEGER PRIMARY KEY,
		played_on  TEXT    NOT NULL,
		published  INTEGER NOT NULL DEFAULT 0,
		created_at TEXT    NOT NULL
	);

	CREATE TABLE IF NOT EXISTS attendance (
		session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		player_id  INTEGER NOT NULL REFERENCES players(id)  ON DELETE CASCADE,
		PRIMARY KEY (session_id, player_id)
	);

	CREATE TABLE IF NOT EXISTS picks (
		session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		player_id  INTEGER NOT NULL REFERENCES players(id)  ON DELETE CASCADE,
		team       TEXT    NOT NULL CHECK (team IN ('A','B')),
		PRIMARY KEY (session_id, player_id)
	);

	CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value BLOB NOT NULL
	);
	`,

	// 2: each game can be given its own team names.
	`
	ALTER TABLE sessions ADD COLUMN team_a_name TEXT NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN team_b_name TEXT NOT NULL DEFAULT '';
	`,
}

func OpenStore(path string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite takes one writer at a time and this is a handful of friends, not
	// a busy site.
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrate brings the database up to date, one step at a time. Each step runs
// in its own transaction alongside the version bump, so a failure halfway
// through leaves the database on the last version that did work.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database is at schema version %d, this build only knows %d: it was written by a newer version",
			version, len(migrations))
	}

	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %d: %w", i+1, err)
		}
		// PRAGMA will not take a placeholder, and i is a loop counter, so
		// there is nothing here to inject.
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", i+1, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// CookieKey returns the key used to sign admin session cookies, generating and
// storing one on first run so restarts do not log you out.
func (s *Store) CookieKey() ([]byte, error) {
	var key []byte
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'cookie_key'`).Scan(&key)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES ('cookie_key', ?)`, key); err != nil {
		return nil, err
	}
	return key, nil
}

// --- players (admin only) ---

// CleanPosition keeps a position only if we recognise it. Anything else, an
// empty string included, means the player will go anywhere.
func CleanPosition(pos string) string {
	pos = strings.ToUpper(strings.TrimSpace(pos))
	if slices.Contains(AllPositions, pos) {
		return pos
	}
	return ""
}

func scanPlayer(sc interface{ Scan(...any) error }) (Player, error) {
	var p Player
	var active, provisional int
	if err := sc.Scan(&p.ID, &p.Name, &active, &p.Weighting, &provisional, &p.Position, &p.Notes); err != nil {
		return Player{}, err
	}
	p.Active = active == 1
	p.Provisional = provisional == 1
	return p, nil
}

const playerCols = `id, name, active, weighting, provisional, position, notes`

func (s *Store) Players() ([]Player, error) {
	rows, err := s.db.Query(`SELECT ` + playerCols + ` FROM players ORDER BY active DESC, name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Player
	for rows.Next() {
		p, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Player(id int64) (Player, error) {
	row := s.db.QueryRow(`SELECT `+playerCols+` FROM players WHERE id = ?`, id)
	return scanPlayer(row)
}

func (s *Store) CreatePlayer(p Player) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO players (name, active, weighting, provisional, position, notes) VALUES (?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(p.Name), boolToInt(p.Active), p.Weighting, boolToInt(p.Provisional),
		CleanPosition(p.Position), p.Notes,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePlayer(p Player) error {
	_, err := s.db.Exec(
		`UPDATE players SET name = ?, active = ?, weighting = ?, provisional = ?, position = ?, notes = ? WHERE id = ?`,
		strings.TrimSpace(p.Name), boolToInt(p.Active), p.Weighting, boolToInt(p.Provisional),
		CleanPosition(p.Position), p.Notes, p.ID,
	)
	return err
}

// AdjustWeighting nudges one player up or down without opening a form, and
// clears the provisional flag because you have now had a proper look.
func (s *Store) AdjustWeighting(id int64, delta int) error {
	_, err := s.db.Exec(`
		UPDATE players
		SET weighting = MAX(?, MIN(?, weighting + ?)), provisional = 0
		WHERE id = ?`, MinWeighting, MaxWeighting, delta, id)
	return err
}

// SetWeighting sets a weighting outright and marks it as settled.
func (s *Store) SetWeighting(id int64, weighting int) error {
	_, err := s.db.Exec(
		`UPDATE players SET weighting = ?, provisional = 0 WHERE id = ?`,
		ClampWeighting(weighting), id)
	return err
}

// ProvisionalPlayers are the ones added in a hurry whose weighting you have
// not gone back to.
func (s *Store) ProvisionalPlayers() ([]Player, error) {
	rows, err := s.db.Query(`SELECT ` + playerCols + ` FROM players WHERE provisional = 1 AND active = 1 ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Player
	for rows.Next() {
		p, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeletePlayer(id int64) error {
	_, err := s.db.Exec(`DELETE FROM players WHERE id = ?`, id)
	return err
}

// --- sessions ---

type Session struct {
	ID        int64
	PlayedOn  string
	Published bool
	CreatedAt string
	Attending int
	HasPicks  bool
	// TeamAName and TeamBName are empty unless this game has been given its
	// own names. Read them through NameA and NameB.
	TeamAName string
	TeamBName string
}

// NameA is what the first side is called for this game.
func (s Session) NameA() string {
	if s.TeamAName == "" {
		return DefaultTeamAName
	}
	return s.TeamAName
}

// NameB is what the second side is called for this game.
func (s Session) NameB() string {
	if s.TeamBName == "" {
		return DefaultTeamBName
	}
	return s.TeamBName
}

func (s *Store) CreateSession(playedOn string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO sessions (played_on, created_at) VALUES (?, ?)`,
		playedOn, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const sessionSelect = `
SELECT s.id, s.played_on, s.published, s.created_at, s.team_a_name, s.team_b_name,
       (SELECT COUNT(*) FROM attendance a WHERE a.session_id = s.id),
       (SELECT COUNT(*) FROM picks p WHERE p.session_id = s.id) > 0
FROM sessions s`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var published, hasPicks int
	if err := sc.Scan(&s.ID, &s.PlayedOn, &published, &s.CreatedAt,
		&s.TeamAName, &s.TeamBName, &s.Attending, &hasPicks); err != nil {
		return Session{}, err
	}
	s.Published = published == 1
	s.HasPicks = hasPicks == 1
	return s, nil
}

func (s *Store) Sessions() ([]Session, error) {
	rows, err := s.db.Query(sessionSelect + ` ORDER BY s.played_on DESC, s.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) Session(id int64) (Session, error) {
	return scanSession(s.db.QueryRow(sessionSelect+` WHERE s.id = ?`, id))
}

func (s *Store) DeleteSession(id int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// SetTeamNames gives one game its own team names. An empty name falls back to
// the default when it is read.
func (s *Store) SetTeamNames(id int64, nameA, nameB string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET team_a_name = ?, team_b_name = ? WHERE id = ?`,
		nameA, nameB, id)
	return err
}

func (s *Store) SetPublished(id int64, published bool) error {
	_, err := s.db.Exec(`UPDATE sessions SET published = ? WHERE id = ?`, boolToInt(published), id)
	return err
}

// LatestPublished returns the most recent session that has been published and
// has teams picked. It is what the public page shows.
func (s *Store) LatestPublished() (Session, error) {
	return scanSession(s.db.QueryRow(sessionSelect + `
		WHERE s.published = 1
		  AND EXISTS (SELECT 1 FROM picks p WHERE p.session_id = s.id)
		ORDER BY s.played_on DESC, s.id DESC
		LIMIT 1`))
}

// --- attendance ---

func (s *Store) SetAttending(sessionID, playerID int64, attending bool) error {
	if attending {
		_, err := s.db.Exec(
			`INSERT INTO attendance (session_id, player_id) VALUES (?, ?)
			 ON CONFLICT DO NOTHING`, sessionID, playerID)
		return err
	}
	_, err := s.db.Exec(`DELETE FROM attendance WHERE session_id = ? AND player_id = ?`, sessionID, playerID)
	return err
}

func (s *Store) AttendingIDs(sessionID int64) (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT player_id FROM attendance WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// AttendingPlayers returns full player records, weighting included, for the
// people marked as playing. Admin side only.
func (s *Store) AttendingPlayers(sessionID int64) ([]Player, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.name, p.active, p.weighting, p.provisional, p.position, p.notes
		FROM players p
		JOIN attendance a ON a.player_id = p.id
		WHERE a.session_id = ?
		ORDER BY p.name COLLATE NOCASE`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Player
	for rows.Next() {
		p, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- picks ---

func (s *Store) SavePicks(sessionID int64, split Split) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM picks WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO picks (session_id, player_id, team) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, p := range split.A {
		if _, err := stmt.Exec(sessionID, p.ID, "A"); err != nil {
			return err
		}
	}
	for _, p := range split.B {
		if _, err := stmt.Exec(sessionID, p.ID, "B"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MovePlayer flips one player to the other side, for when you know something
// the numbers do not.
func (s *Store) MovePlayer(sessionID, playerID int64) error {
	_, err := s.db.Exec(`
		UPDATE picks SET team = CASE team WHEN 'A' THEN 'B' ELSE 'A' END
		WHERE session_id = ? AND player_id = ?`, sessionID, playerID)
	return err
}

// Lineup is the public view of a session: names and positions, nothing else.
type Lineup struct {
	Session Session
	TeamA   []PublicPlayer
	TeamB   []PublicPlayer
}

// Lineup reads the picked teams without touching the weighting column, so a
// weighting cannot reach a public page even by mistake.
func (s *Store) Lineup(sessionID int64) (Lineup, error) {
	sess, err := s.Session(sessionID)
	if err != nil {
		return Lineup{}, err
	}

	rows, err := s.db.Query(`
		SELECT k.team, p.name, p.position
		FROM picks k
		JOIN players p ON p.id = k.player_id
		WHERE k.session_id = ?
		ORDER BY p.name COLLATE NOCASE`, sessionID)
	if err != nil {
		return Lineup{}, err
	}
	defer rows.Close()

	out := Lineup{Session: sess}
	for rows.Next() {
		var team, name, position string
		if err := rows.Scan(&team, &name, &position); err != nil {
			return Lineup{}, err
		}
		pp := PublicPlayer{Name: name, Position: position}
		if team == "A" {
			out.TeamA = append(out.TeamA, pp)
		} else {
			out.TeamB = append(out.TeamB, pp)
		}
	}
	return out, rows.Err()
}

// PickedIDs maps player ID to team letter for a session.
func (s *Store) PickedIDs(sessionID int64) (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT player_id, team FROM picks WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var team string
		if err := rows.Scan(&id, &team); err != nil {
			return nil, err
		}
		out[id] = team
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Package store keeps file history in SQLite as a "linked hashmap": files maps
// a file's identity (device + inode) to the head of its event chain, and each
// event points to the previous one while holding both the old and new state.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS runs (
	id          INTEGER PRIMARY KEY,
	kind        TEXT NOT NULL,           -- scan | plan | apply | undo
	roots       TEXT NOT NULL,
	status      TEXT NOT NULL,           -- running | done | failed
	error       TEXT,
	started_at  TEXT NOT NULL,
	finished_at TEXT
);

CREATE TABLE IF NOT EXISTS files (
	id            INTEGER PRIMARY KEY,
	dev           INTEGER,
	inode         INTEGER,
	content_hash  TEXT NOT NULL,
	head_event_id INTEGER REFERENCES events(id),
	UNIQUE (dev, inode)
);
CREATE INDEX IF NOT EXISTS files_content_hash ON files(content_hash);

CREATE TABLE IF NOT EXISTS events (
	id            INTEGER PRIMARY KEY,
	file_id       INTEGER NOT NULL REFERENCES files(id),
	prev_event_id INTEGER REFERENCES events(id),
	run_id        INTEGER REFERENCES runs(id),
	op            TEXT NOT NULL,         -- seen | changed | moved | undone | cleaned | removed | restored
	old_path      TEXT,
	old_hash      TEXT,
	old_size      INTEGER,
	old_mtime     INTEGER,
	new_path      TEXT NOT NULL,
	new_hash      TEXT NOT NULL,
	new_size      INTEGER NOT NULL,
	new_mtime     INTEGER NOT NULL,
	meta          TEXT,
	created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_file ON events(file_id);

-- Folders an apply run had to create, so undo can remove them again if empty.
CREATE TABLE IF NOT EXISTS created_dirs (
	run_id INTEGER NOT NULL REFERENCES runs(id),
	path   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plan_items (
	run_id     INTEGER NOT NULL REFERENCES runs(id),
	file_id    INTEGER NOT NULL REFERENCES files(id),
	src        TEXT NOT NULL,
	dst        TEXT NOT NULL,
	category   TEXT NOT NULL,
	source     TEXT NOT NULL,            -- which decider chose the category
	reason     TEXT NOT NULL,
	confidence REAL NOT NULL,
	selected   INTEGER NOT NULL,
	PRIMARY KEY (run_id, file_id)
);
`

// State is a file's observable state at one point in time.
type State struct {
	Path  string
	Hash  string
	Size  int64
	MTime int64 // unix nanoseconds
}

// Head is the latest known state of a file.
type Head struct {
	FileID      int64
	HeadEventID int64
	State
}

// Observation is what a scan saw for one file.
type Observation struct {
	Dev, Inode uint64
	State
	Meta string // JSON
}

// Event is one node of a file's history chain.
type Event struct {
	ID        int64
	PrevID    int64 // 0 at the tail
	RunID     int64
	Op        string
	Old       *State // nil for the first observation
	New       State
	CreatedAt time.Time
}

// PlanRow is one proposed move.
type PlanRow struct {
	FileID     int64
	Src, Dst   string
	Category   string
	Source     string
	Reason     string
	Confidence float64
	Selected   bool
}

type Store struct{ db *sql.DB }

// DefaultPath is ~/Library/Application Support/mac-cleaner/state.db on macOS.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mac-cleaner", "state.db"), nil
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) BeginRun(kind string, roots []string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO runs (kind, roots, status, started_at) VALUES (?, ?, 'running', ?)`,
		kind, strings.Join(roots, "\n"), now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishRun(id int64, runErr error) error {
	status, msg := "done", sql.NullString{}
	if runErr != nil {
		status, msg = "failed", sql.NullString{String: runErr.Error(), Valid: true}
	}
	_, err := s.db.Exec(`UPDATE runs SET status = ?, error = ?, finished_at = ? WHERE id = ?`, status, msg, now(), id)
	return err
}

// Lookup returns the latest state of the file with this identity, or nil.
func (s *Store) Lookup(dev, inode uint64) (*Head, error) {
	return lookup(s.db, dev, inode)
}

// Observe records what a scan saw. A new identity starts a chain with a "seen"
// event; a known one gets a "changed" event only if something differs.
func (s *Store) Observe(runID int64, o Observation) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	head, err := lookup(tx, o.Dev, o.Inode)
	if err != nil {
		return 0, err
	}
	switch {
	case head == nil:
	case head.State == o.State:
		return head.FileID, nil
	case head.Path != o.Path && head.Hash != o.Hash:
		// Same inode but a different name and content: the inode was reused by an
		// unrelated file. Detach the old identity; its history stays intact.
		if _, err := tx.Exec(`UPDATE files SET dev = NULL, inode = NULL WHERE id = ?`, head.FileID); err != nil {
			return 0, err
		}
		head = nil
	default:
		if err := appendEvent(tx, head.FileID, head.HeadEventID, runID, "changed", &head.State, o.State, o.Meta); err != nil {
			return 0, err
		}
		return head.FileID, tx.Commit()
	}

	res, err := tx.Exec(`INSERT INTO files (dev, inode, content_hash) VALUES (?, ?, ?)`, int64(o.Dev), int64(o.Inode), o.Hash)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := appendEvent(tx, id, 0, runID, "seen", nil, o.State, o.Meta); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// History walks a file's chain from the newest event to the oldest.
func (s *Store) History(fileID int64) ([]Event, error) {
	var next sql.NullInt64
	if err := s.db.QueryRow(`SELECT head_event_id FROM files WHERE id = ?`, fileID).Scan(&next); err != nil {
		return nil, err
	}
	var events []Event
	for next.Valid {
		var (
			e                 Event
			prev, run         sql.NullInt64
			oldPath, oldHash  sql.NullString
			oldSize, oldMTime sql.NullInt64
			created           string
		)
		err := s.db.QueryRow(`SELECT id, prev_event_id, run_id, op, old_path, old_hash, old_size, old_mtime,
				new_path, new_hash, new_size, new_mtime, created_at FROM events WHERE id = ?`, next.Int64).
			Scan(&e.ID, &prev, &run, &e.Op, &oldPath, &oldHash, &oldSize, &oldMTime,
				&e.New.Path, &e.New.Hash, &e.New.Size, &e.New.MTime, &created)
		if err != nil {
			return nil, err
		}
		e.PrevID, e.RunID = prev.Int64, run.Int64
		if oldPath.Valid {
			e.Old = &State{Path: oldPath.String, Hash: oldHash.String, Size: oldSize.Int64, MTime: oldMTime.Int64}
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		events = append(events, e)
		next = prev
	}
	return events, nil
}

func (s *Store) SavePlan(runID int64, rows []PlanRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO plan_items (run_id, file_id, src, dst, category, source, reason, confidence, selected)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.Exec(runID, r.FileID, r.Src, r.Dst, r.Category, r.Source, r.Reason, r.Confidence, r.Selected); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecordMove appends a "moved" (or "undone") event: the same file, now at newPath.
func (s *Store) RecordMove(runID, fileID int64, op, newPath string) error {
	return s.record(runID, fileID, op, newPath, "")
}

// RecordClean appends a "cleaned" event to a folder's history; meta lists
// what was deleted.
func (s *Store) RecordClean(runID, fileID int64, meta string) error {
	return s.record(runID, fileID, "cleaned", "", meta)
}

// record appends an event that keeps the file's state, at newPath if given.
func (s *Store) record(runID, fileID int64, op, newPath, meta string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var h Head
	err = tx.QueryRow(`SELECT f.id, e.id, e.new_path, e.new_hash, e.new_size, e.new_mtime
		FROM files f JOIN events e ON e.id = f.head_event_id WHERE f.id = ?`, fileID).
		Scan(&h.FileID, &h.HeadEventID, &h.Path, &h.Hash, &h.Size, &h.MTime)
	if err != nil {
		return err
	}
	next := h.State
	if newPath != "" {
		next.Path = newPath
	}
	if err := appendEvent(tx, fileID, h.HeadEventID, runID, op, &h.State, next, meta); err != nil {
		return err
	}
	return tx.Commit()
}

// Move is one recorded move of an apply run.
type Move struct {
	FileID     int64
	Dev, Inode uint64
	From, To   string
	Removed    bool // an empty folder that was deleted, not moved
}

// Moves returns the moves an apply run made, in the order it made them, along
// with the empty folders it removed (From == To for those).
func (s *Store) Moves(runID int64) ([]Move, error) {
	rows, err := s.db.Query(`SELECT e.file_id, COALESCE(f.dev, 0), COALESCE(f.inode, 0), e.old_path, e.new_path, e.op
		FROM events e JOIN files f ON f.id = e.file_id
		WHERE e.run_id = ? AND e.op IN ('moved', 'removed') ORDER BY e.id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Move
	for rows.Next() {
		var m Move
		var dev, inode int64
		var op string
		if err := rows.Scan(&m.FileID, &dev, &inode, &m.From, &m.To, &op); err != nil {
			return nil, err
		}
		m.Removed = op == "removed"
		m.Dev, m.Inode = uint64(dev), uint64(inode)
		out = append(out, m)
	}
	return out, rows.Err()
}

// LastUndoableApply returns the newest finished apply run that no undo run
// has reverted yet, or 0.
func (s *Store) LastUndoableApply() (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM runs a WHERE kind = 'apply' AND status = 'done'
		AND NOT EXISTS (SELECT 1 FROM runs u WHERE u.kind = 'undo' AND u.status = 'done' AND u.roots = 'apply #' || a.id)
		ORDER BY id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// Undone reports whether an undo run already reverted this apply run.
func (s *Store) Undone(applyRunID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE kind = 'undo' AND status = 'done' AND roots = ?`,
		"apply #"+strconv.FormatInt(applyRunID, 10)).Scan(&n)
	return n > 0, err
}

// ApplySummary counts what an apply run moved and how many bytes its cleaning
// freed, from the history it recorded.
func (s *Store) ApplySummary(runID int64) (moved int, freed int64, finished time.Time, err error) {
	var at sql.NullString
	if err = s.db.QueryRow(`SELECT finished_at FROM runs WHERE id = ?`, runID).Scan(&at); err != nil {
		return
	}
	finished, _ = time.Parse(time.RFC3339Nano, at.String)
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE run_id = ? AND op IN ('moved', 'removed')`, runID).Scan(&moved); err != nil {
		return
	}
	rows, err := s.db.Query(`SELECT meta FROM events WHERE run_id = ? AND op = 'cleaned'`, runID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var meta string
		if err = rows.Scan(&meta); err != nil {
			return
		}
		var deleted []struct{ Bytes int64 }
		if json.Unmarshal([]byte(meta), &deleted) == nil {
			for _, d := range deleted {
				freed += d.Bytes
			}
		}
	}
	err = rows.Err()
	return
}

func (s *Store) AddCreatedDir(runID int64, path string) error {
	_, err := s.db.Exec(`INSERT INTO created_dirs (run_id, path) VALUES (?, ?)`, runID, path)
	return err
}

// CreatedDirs returns the folders a run created, deepest first.
func (s *Store) CreatedDirs(runID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM created_dirs WHERE run_id = ? ORDER BY length(path) DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Setting returns a remembered value, or "" if there is none.
func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func lookup(q querier, dev, inode uint64) (*Head, error) {
	var h Head
	err := q.QueryRow(`SELECT f.id, e.id, e.new_path, e.new_hash, e.new_size, e.new_mtime
		FROM files f JOIN events e ON e.id = f.head_event_id
		WHERE f.dev = ? AND f.inode = ?`, int64(dev), int64(inode)).
		Scan(&h.FileID, &h.HeadEventID, &h.Path, &h.Hash, &h.Size, &h.MTime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func appendEvent(tx *sql.Tx, fileID, prevID, runID int64, op string, old *State, next State, meta string) error {
	var (
		oldPath, oldHash  sql.NullString
		oldSize, oldMTime sql.NullInt64
	)
	if old != nil {
		oldPath = sql.NullString{String: old.Path, Valid: true}
		oldHash = sql.NullString{String: old.Hash, Valid: true}
		oldSize = sql.NullInt64{Int64: old.Size, Valid: true}
		oldMTime = sql.NullInt64{Int64: old.MTime, Valid: true}
	}
	res, err := tx.Exec(`INSERT INTO events (file_id, prev_event_id, run_id, op,
			old_path, old_hash, old_size, old_mtime, new_path, new_hash, new_size, new_mtime, meta, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fileID, nullID(prevID), nullID(runID), op,
		oldPath, oldHash, oldSize, oldMTime, next.Path, next.Hash, next.Size, next.MTime,
		sql.NullString{String: meta, Valid: meta != ""}, now())
	if err != nil {
		return err
	}
	eventID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE files SET head_event_id = ?, content_hash = ? WHERE id = ?`, eventID, next.Hash, fileID)
	return err
}

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id != 0} }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

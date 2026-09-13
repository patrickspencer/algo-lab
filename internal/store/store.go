// Package store persists problem data in a local SQLite database so the
// problem list and any descriptions that have been viewed survive restarts.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver

	"github.com/patrickspencer/algo-lab-public/internal/catalog"
)

// Store wraps the SQLite database.
type Store struct {
	db   *sql.DB
	Path string
}

// Stats summarises what the database holds.
type Stats struct {
	Problems int
	Details  int
	LastSync time.Time // zero if the list has never been synced
}

// DefaultPath returns the database location: $ALGORITHMS_DB if set, otherwise
// <user config dir>/algo-lab/algorithms.db (on macOS that is
// ~/Library/Application Support/algo-lab/algorithms.db).
func DefaultPath() (string, error) {
	if p := os.Getenv("ALGORITHMS_DB"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "algo-lab", "algorithms.db"), nil
}

const schema = `
CREATE TABLE IF NOT EXISTS problems (
	title_slug  TEXT PRIMARY KEY,
	frontend_id TEXT NOT NULL,
	title       TEXT NOT NULL,
	difficulty  TEXT NOT NULL,
	ac_rate     REAL NOT NULL,
	paid_only   INTEGER NOT NULL,
	topic_tags  TEXT NOT NULL,   -- JSON array of tag names
	position    INTEGER NOT NULL, -- order returned by the API
	updated_at  TEXT NOT NULL,
	status      TEXT NOT NULL DEFAULT '' -- 'ac' solved, 'notac' attempted (from the signed-in account)
);
CREATE INDEX IF NOT EXISTS problems_position ON problems(position);

CREATE TABLE IF NOT EXISTS details (
	title_slug TEXT PRIMARY KEY,
	data       TEXT NOT NULL,     -- JSON-encoded catalog.Detail
	fetched_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- Per-user problem progress: 'solved' or 'attempted'. Absent means not started.
CREATE TABLE IF NOT EXISTS progress (
	user_id    INTEGER NOT NULL,
	title_slug TEXT NOT NULL,
	status     TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (user_id, title_slug)
);

-- People using the web app. The local (TUI) user is id 0 and is also the
-- first person to sign in on the web, so they keep seeing their own data.
CREATE TABLE IF NOT EXISTS users (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	token      TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL
);

-- Browser sessions: one row per signed-in device, so logging in on a new
-- browser never evicts the others. A session lives until the person logs out.
CREATE TABLE IF NOT EXISTS sessions (
	token      TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL,
	created_at TEXT NOT NULL
);

-- Explicit snapshots of a solution (ctrl+s). Never deleted automatically.
CREATE TABLE IF NOT EXISTS attempts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	title_slug TEXT NOT NULL,
	lang       TEXT NOT NULL,
	code       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	user_id    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS attempts_slug ON attempts(title_slug, id);

-- AI hints, one per problem and level (regenerated on request).
CREATE TABLE IF NOT EXISTS hints (
	title_slug TEXT NOT NULL,
	level      INTEGER NOT NULL,
	provider   TEXT NOT NULL,
	text       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (title_slug, level)
);

-- AI code reviews of a solution attempt.
CREATE TABLE IF NOT EXISTS reviews (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	title_slug TEXT NOT NULL,
	lang       TEXT NOT NULL,
	code       TEXT NOT NULL,
	provider   TEXT NOT NULL,
	review     TEXT NOT NULL,   -- JSON-encoded ai.Review
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS reviews_slug ON reviews(title_slug, id);

-- Scratchpads: free-standing snippets to run and tinker with.
CREATE TABLE IF NOT EXISTS scratches (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	lang       TEXT NOT NULL,
	code       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	user_id    INTEGER NOT NULL DEFAULT 0
);

-- Work in progress, autosaved as you type; one per user, problem and language.
CREATE TABLE IF NOT EXISTS drafts (
	user_id    INTEGER NOT NULL DEFAULT 0,
	title_slug TEXT NOT NULL,
	lang       TEXT NOT NULL,
	code       TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (user_id, title_slug, lang)
);`

// LocalUser is the id of the person using the TUI (and the first person to
// sign in on the web app).
const LocalUser int64 = 0

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create dir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// modernc sqlite is happiest with a single connection.
	db.SetMaxOpenConns(1)
	if err := checkSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: %s: %w (set ALGORITHMS_DB to use a different file)", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{db: db, Path: path}, nil
}

// migrate brings databases created by older versions up to date.
func migrate(db *sql.DB) error {
	// Password hashes on users (added with password login).
	if !hasColumn(db, "users", "password_hash") {
		if _, err := db.Exec(`ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// Solved status on problems (added with sign-in).
	if !hasColumn(db, "problems", "status") {
		if _, err := db.Exec(`ALTER TABLE problems ADD COLUMN status TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// user_id columns on attempts and scratches (added with duo mode).
	for _, table := range []string{"attempts", "scratches"} {
		if !hasColumn(db, table, "user_id") {
			if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0`, table)); err != nil {
				return err
			}
		}
	}
	// drafts gained user_id in its primary key, which SQLite cannot alter in
	// place: rebuild the table and copy the rows over as the local user's.
	if !hasColumn(db, "drafts", "user_id") {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		for _, stmt := range []string{
			`CREATE TABLE drafts_new (
				user_id    INTEGER NOT NULL DEFAULT 0,
				title_slug TEXT NOT NULL,
				lang       TEXT NOT NULL,
				code       TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				PRIMARY KEY (user_id, title_slug, lang)
			)`,
			`INSERT INTO drafts_new (user_id, title_slug, lang, code, updated_at)
				SELECT 0, title_slug, lang, code, updated_at FROM drafts`,
			`DROP TABLE drafts`,
			`ALTER TABLE drafts_new RENAME TO drafts`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) bool {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk) == nil && name == column {
			return true
		}
	}
	return false
}

// expectedColumns is what each table must contain. CREATE TABLE IF NOT EXISTS
// silently keeps a pre-existing table, so an unrelated program that happens to
// use the same file and table names would otherwise cause confusing query
// errors, or worse, get its data overwritten.
var expectedColumns = map[string][]string{
	"problems":  {"title_slug", "frontend_id", "title", "difficulty", "ac_rate", "paid_only", "topic_tags", "position", "updated_at"},
	"details":   {"title_slug", "data", "fetched_at"},
	"meta":      {"key", "value"},
	"attempts":  {"id", "title_slug", "lang", "code", "created_at"},
	"drafts":    {"title_slug", "lang", "code", "updated_at"},
	"hints":     {"title_slug", "level", "provider", "text", "created_at"},
	"reviews":   {"id", "title_slug", "lang", "code", "provider", "review", "created_at"},
	"scratches": {"id", "lang", "code", "created_at", "updated_at"},
	"sessions":  {"token", "user_id", "created_at"},
}

func checkSchema(db *sql.DB) error {
	for table, want := range expectedColumns {
		rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			have[name] = true
		}
		rows.Close()
		if len(have) == 0 {
			continue // table does not exist yet; migration will create it
		}
		for _, col := range want {
			if !have[col] {
				return fmt.Errorf("table %q lacks column %q; the database was not created by algo-lab", table, col)
			}
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Problems returns every cached problem in API order.
func (s *Store) Problems(ctx context.Context) ([]catalog.Problem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT title_slug, frontend_id, title, difficulty, ac_rate, paid_only, topic_tags, status
		FROM problems ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []catalog.Problem
	for rows.Next() {
		var p catalog.Problem
		var paid int
		var tags string
		if err := rows.Scan(&p.TitleSlug, &p.FrontendID, &p.Title, &p.Difficulty, &p.AcRate, &paid, &tags, &p.Status); err != nil {
			return nil, err
		}
		p.PaidOnly = paid != 0
		var names []string
		if err := json.Unmarshal([]byte(tags), &names); err != nil {
			return nil, fmt.Errorf("store: bad tags for %s: %w", p.TitleSlug, err)
		}
		for _, n := range names {
			p.TopicTags = append(p.TopicTags, catalog.TopicTag{Name: n})
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveProblems replaces the cached problem list and records the sync time.
func (s *Store) SaveProblems(ctx context.Context, problems []catalog.Problem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `DELETE FROM problems`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO problems (title_slug, frontend_id, title, difficulty, ac_rate, paid_only, topic_tags, position, updated_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for i, p := range problems {
		names := make([]string, 0, len(p.TopicTags))
		for _, t := range p.TopicTags {
			names = append(names, t.Name)
		}
		tags, _ := json.Marshal(names)
		paid := 0
		if p.PaidOnly {
			paid = 1
		}
		if _, err := stmt.ExecContext(ctx, p.TitleSlug, p.FrontendID, p.Title, p.Difficulty, p.AcRate, paid, string(tags), i, now, p.Status); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES ('last_sync', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, now); err != nil {
		return err
	}
	return tx.Commit()
}

// SetProblemStatus records a solved/attempted status for one problem
// (e.g. right after an accepted submission).
func (s *Store) SetProblemStatus(ctx context.Context, slug, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE problems SET status = ? WHERE title_slug = ?`, status, slug)
	return err
}

// Detail returns a cached problem description, or ok=false if not cached.
func (s *Store) Detail(ctx context.Context, slug string) (d *catalog.Detail, fetchedAt time.Time, ok bool, err error) {
	var data, at string
	err = s.db.QueryRowContext(ctx, `SELECT data, fetched_at FROM details WHERE title_slug = ?`, slug).Scan(&data, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	d = new(catalog.Detail)
	if err := json.Unmarshal([]byte(data), d); err != nil {
		return nil, time.Time{}, false, fmt.Errorf("store: bad detail for %s: %w", slug, err)
	}
	fetchedAt, _ = time.Parse(time.RFC3339, at)
	return d, fetchedAt, true, nil
}

// SaveDetail caches a problem description.
func (s *Store) SaveDetail(ctx context.Context, d *catalog.Detail) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO details (title_slug, data, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT(title_slug) DO UPDATE SET data = excluded.data, fetched_at = excluded.fetched_at`,
		d.TitleSlug, string(data), time.Now().UTC().Format(time.RFC3339))
	return err
}

// Stats reports row counts and the last list sync time.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM problems`).Scan(&st.Problems); err != nil {
		return st, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM details`).Scan(&st.Details); err != nil {
		return st, err
	}
	var last string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'last_sync'`).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	if last != "" {
		if t, err := time.Parse(time.RFC3339, last); err == nil {
			st.LastSync = t.Local()
		}
	}
	return st, nil
}

// Attempt is one saved solution snapshot.
type Attempt struct {
	ID        int64
	TitleSlug string
	Lang      string
	Code      string
	CreatedAt time.Time
}

// Attempts lists a user's saved attempts for a problem, newest first.
func (s *Store) Attempts(ctx context.Context, user int64, slug string) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title_slug, lang, code, created_at FROM attempts WHERE user_id = ? AND title_slug = ? ORDER BY id DESC`, user, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		var at string
		if err := rows.Scan(&a.ID, &a.TitleSlug, &a.Lang, &a.Code, &at); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			a.CreatedAt = t.Local()
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveAttempt stores a new snapshot and returns its id.
func (s *Store) SaveAttempt(ctx context.Context, user int64, slug, lang, code string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO attempts (title_slug, lang, code, created_at, user_id) VALUES (?, ?, ?, ?, ?)`,
		slug, lang, code, time.Now().UTC().Format(time.RFC3339), user)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteAttempt removes one of the user's snapshots.
func (s *Store) DeleteAttempt(ctx context.Context, user, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM attempts WHERE id = ? AND user_id = ?`, id, user)
	return err
}

// AttemptCounts returns the user's number of attempts per problem slug.
func (s *Store) AttemptCounts(ctx context.Context, user int64) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT title_slug, COUNT(*) FROM attempts WHERE user_id = ? GROUP BY title_slug`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var slug string
		var n int
		if err := rows.Scan(&slug, &n); err != nil {
			return nil, err
		}
		out[slug] = n
	}
	return out, rows.Err()
}

// Draft returns the user's autosaved work in progress for a problem and language.
func (s *Store) Draft(ctx context.Context, user int64, slug, lang string) (code string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT code FROM drafts WHERE user_id = ? AND title_slug = ? AND lang = ?`, user, slug, lang).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return code, true, nil
}

// SaveDraft upserts the user's work in progress for a problem and language.
func (s *Store) SaveDraft(ctx context.Context, user int64, slug, lang, code string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO drafts (user_id, title_slug, lang, code, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, title_slug, lang) DO UPDATE SET code = excluded.code, updated_at = excluded.updated_at`,
		user, slug, lang, code, time.Now().UTC().Format(time.RFC3339))
	return err
}

// Meta reads a key from the meta table ("" if absent).
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta writes a key to the meta table.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Hint is a cached AI hint.
type Hint struct {
	Level     int
	Provider  string
	Text      string
	CreatedAt time.Time
}

// Hints returns the cached hints for a problem keyed by level.
func (s *Store) Hints(ctx context.Context, slug string) (map[int]Hint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT level, provider, text, created_at FROM hints WHERE title_slug = ?`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]Hint{}
	for rows.Next() {
		var h Hint
		var at string
		if err := rows.Scan(&h.Level, &h.Provider, &h.Text, &at); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			h.CreatedAt = t.Local()
		}
		out[h.Level] = h
	}
	return out, rows.Err()
}

// SaveHint upserts a hint for a problem and level.
func (s *Store) SaveHint(ctx context.Context, slug string, level int, provider, text string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO hints (title_slug, level, provider, text, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(title_slug, level) DO UPDATE SET provider = excluded.provider, text = excluded.text, created_at = excluded.created_at`,
		slug, level, provider, text, time.Now().UTC().Format(time.RFC3339))
	return err
}

// ReviewRecord is a stored code review.
type ReviewRecord struct {
	ID        int64
	TitleSlug string
	Lang      string
	Code      string
	Provider  string
	Review    string // JSON
	CreatedAt time.Time
}

// LatestReview returns the most recent review for a problem, or ok=false.
func (s *Store) LatestReview(ctx context.Context, slug string) (r ReviewRecord, ok bool, err error) {
	var at string
	err = s.db.QueryRowContext(ctx, `
		SELECT id, title_slug, lang, code, provider, review, created_at FROM reviews
		WHERE title_slug = ? ORDER BY id DESC LIMIT 1`, slug).
		Scan(&r.ID, &r.TitleSlug, &r.Lang, &r.Code, &r.Provider, &r.Review, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		r.CreatedAt = t.Local()
	}
	return r, true, nil
}

// SaveReview stores a review and returns its id.
func (s *Store) SaveReview(ctx context.Context, slug, lang, code, provider, reviewJSON string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO reviews (title_slug, lang, code, provider, review, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		slug, lang, code, provider, reviewJSON, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Scratch is a free-standing snippet.
type Scratch struct {
	ID        int64
	Lang      string
	Code      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Scratches lists a user's scratchpads, most recently updated first.
func (s *Store) Scratches(ctx context.Context, user int64) ([]Scratch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, lang, code, created_at, updated_at FROM scratches WHERE user_id = ? ORDER BY updated_at DESC, id DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Scratch
	for rows.Next() {
		var sc Scratch
		var c, u string
		if err := rows.Scan(&sc.ID, &sc.Lang, &sc.Code, &c, &u); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, c); err == nil {
			sc.CreatedAt = t.Local()
		}
		if t, err := time.Parse(time.RFC3339Nano, u); err == nil {
			sc.UpdatedAt = t.Local()
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// SaveScratch inserts (id == 0) or updates a user's scratchpad and returns its id.
func (s *Store) SaveScratch(ctx context.Context, user, id int64, lang, code string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano) // sub-second so ordering by update is stable
	if id == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO scratches (lang, code, created_at, updated_at, user_id) VALUES (?, ?, ?, ?, ?)`, lang, code, now, now, user)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE scratches SET lang = ?, code = ?, updated_at = ? WHERE id = ? AND user_id = ?`, lang, code, now, id, user)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, fmt.Errorf("store: scratch %d not found", id)
	}
	return id, nil
}

// DeleteScratch removes one of the user's scratchpads.
func (s *Store) DeleteScratch(ctx context.Context, user, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM scratches WHERE id = ? AND user_id = ?`, id, user)
	return err
}

// User is someone who has signed in on the web app.
type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// Users lists everyone who has signed in.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var at string
		if err := rows.Scan(&u.ID, &u.Name, &at); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			u.CreatedAt = t.Local()
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserByToken resolves a session token, or ok=false.
func (s *Store) UserByToken(ctx context.Context, token string) (u User, ok bool, err error) {
	var at string
	// Prefer the sessions table (multiple concurrent logins); fall back to
	// the legacy single token on the user row for cookies issued before
	// sessions existed.
	err = s.db.QueryRowContext(ctx, `
		SELECT u.id, u.name, u.created_at FROM sessions s
		JOIN users u ON u.id = s.user_id WHERE s.token = ?`, token).Scan(&u.ID, &u.Name, &at)
	if err == sql.ErrNoRows {
		err = s.db.QueryRowContext(ctx, `SELECT id, name, created_at FROM users WHERE token = ?`, token).Scan(&u.ID, &u.Name, &at)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return u, false, nil
	}
	if err != nil {
		return u, false, err
	}
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		u.CreatedAt = t.Local()
	}
	return u, true, nil
}

// FindUser looks up a user by name (case-insensitive), returning their
// password hash ("" if none) and current token.
func (s *Store) FindUser(ctx context.Context, name string) (u User, hash, token string, ok bool, err error) {
	var at string
	err = s.db.QueryRowContext(ctx,
		`SELECT id, name, token, password_hash, created_at FROM users WHERE name = ? COLLATE NOCASE`, strings.TrimSpace(name)).
		Scan(&u.ID, &u.Name, &token, &hash, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", "", false, nil
	}
	if err != nil {
		return User{}, "", "", false, err
	}
	if t, perr := time.Parse(time.RFC3339, at); perr == nil {
		u.CreatedAt = t.Local()
	}
	return u, hash, token, true, nil
}

// CreateUser inserts a new user with the given password hash ("" for none)
// and session token. The very first user gets id 0 (the local/TUI user), so
// the person who set the app up keeps their existing data.
func (s *Store) CreateUser(ctx context.Context, name, hash, token string) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return User{}, errors.New("store: name required")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return User{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if count == 0 {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO users (id, name, token, password_hash, created_at) VALUES (?, ?, ?, ?, ?)`, LocalUser, name, token, hash, now); err != nil {
			return User{}, err
		}
		return User{ID: LocalUser, Name: name, CreatedAt: time.Now()}, nil
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (name, token, password_hash, created_at) VALUES (?, ?, ?, ?)`, name, token, hash, now)
	if err != nil {
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return User{ID: id, Name: name, CreatedAt: time.Now()}, nil
}

// UpdateToken sets a user's session token (used on each successful login).
func (s *Store) UpdateToken(ctx context.Context, id int64, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET token = ? WHERE id = ?`, token, id)
	return err
}

// AddSession records a new browser session for a user. Each login adds a
// session rather than replacing one, so signing in on another device does not
// log the user out here.
func (s *Store) AddSession(ctx context.Context, userID int64, token string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, created_at) VALUES (?, ?, ?)`,
		token, userID, time.Now().UTC().Format(time.RFC3339))
	return err
}

// DeleteSession removes one browser session (used on logout). Other sessions
// for the same user are left untouched.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// SetPassword creates the named user if needed and sets their password hash.
// Used by the admin command that provisions accounts.
func (s *Store) SetPassword(ctx context.Context, name, hash string) (User, error) {
	u, _, _, ok, err := s.FindUser(ctx, name)
	if err != nil {
		return User{}, err
	}
	if ok {
		if _, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, u.ID); err != nil {
			return User{}, err
		}
		return u, nil
	}
	return s.CreateUser(ctx, name, hash, "")
}

// Progress returns a user's per-problem status keyed by slug ("solved" or "attempted").
func (s *Store) Progress(ctx context.Context, user int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT title_slug, status FROM progress WHERE user_id = ?`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var slug, status string
		if err := rows.Scan(&slug, &status); err != nil {
			return nil, err
		}
		out[slug] = status
	}
	return out, rows.Err()
}

// SetProgress sets (or, with status=="", clears) a user's status for a problem.
func (s *Store) SetProgress(ctx context.Context, user int64, slug, status string) error {
	if status == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM progress WHERE user_id = ? AND title_slug = ?`, user, slug)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO progress (user_id, title_slug, status, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, title_slug) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`,
		user, slug, status, time.Now().UTC().Format(time.RFC3339))
	return err
}

// MarkAttempted records that a user has worked on a problem, without
// downgrading a problem they have already marked solved.
func (s *Store) MarkAttempted(ctx context.Context, user int64, slug string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO progress (user_id, title_slug, status, updated_at) VALUES (?, ?, 'attempted', ?)
		ON CONFLICT(user_id, title_slug) DO NOTHING`, user, slug, time.Now().UTC().Format(time.RFC3339))
	return err
}

// SeedProgress fills a user's progress from a map of slug to status
// ("ac" -> solved, "notac" -> attempted), without overwriting a solved mark.
func (s *Store) SeedProgress(ctx context.Context, user int64, statuses map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().UTC().Format(time.RFC3339)
	for slug, st := range statuses {
		switch st {
		case "ac":
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO progress (user_id, title_slug, status, updated_at) VALUES (?, ?, 'solved', ?)
				ON CONFLICT(user_id, title_slug) DO UPDATE SET status='solved', updated_at=excluded.updated_at`, user, slug, now); err != nil {
				return err
			}
		case "notac":
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO progress (user_id, title_slug, status, updated_at) VALUES (?, ?, 'attempted', ?)
				ON CONFLICT(user_id, title_slug) DO NOTHING`, user, slug, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

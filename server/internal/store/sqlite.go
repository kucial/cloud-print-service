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

	"github.com/kucial/cloud-print-service/server/internal/domain"

	_ "modernc.org/sqlite" // pure Go: keeps the CGO_ENABLED=0 builds
)

// SQLiteStore keeps jobs, attempts and printer overrides in one SQLite file, so a
// restart loses nothing. It is the default store: one site, one server, one file to
// back up. The connection is single (SQLite has one writer anyway), which also makes
// id allocation inside a transaction race-free.
type SQLiteStore struct {
	db *sql.DB
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS jobs (
	seq         INTEGER PRIMARY KEY,
	id          TEXT NOT NULL UNIQUE,
	client_id   TEXT NOT NULL,
	external_id TEXT NOT NULL,
	printer_id  TEXT NOT NULL,
	state       TEXT NOT NULL,
	queued_at   INTEGER NOT NULL,          -- unix nanos, for ordering and ranges
	payload     BLOB NOT NULL,
	idem_key    TEXT NOT NULL,
	doc         TEXT NOT NULL              -- the job as JSON (fields the API shows)
);
CREATE UNIQUE INDEX IF NOT EXISTS jobs_external ON jobs(client_id, external_id) WHERE external_id <> '';
CREATE INDEX IF NOT EXISTS jobs_queued ON jobs(queued_at);
CREATE INDEX IF NOT EXISTS jobs_state ON jobs(state);
CREATE INDEX IF NOT EXISTS jobs_printer ON jobs(printer_id, queued_at);

CREATE TABLE IF NOT EXISTS attempts (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id TEXT NOT NULL,
	doc    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS attempts_job ON attempts(job_id, id);

CREATE TABLE IF NOT EXISTS printer_overrides (
	printer_id TEXT PRIMARY KEY,
	patch      TEXT NOT NULL,              -- operator edits, as a JSON metadata patch
	updated_at INTEGER NOT NULL
);
`

// OpenSQLite opens (creating if needed) the database at path and prepares the schema.
func OpenSQLite(ctx context.Context, path string) (*SQLiteStore, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
	}
	// The file holds job payloads (business data) and token hashes: owner-only. SQLite
	// would create it with the process umask, so create it first.
	if f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600); err == nil {
		f.Close()
	} else {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	// WAL keeps readers off the writer's lock; busy_timeout rides out a backup holding it.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("prepare schema in %s: %w", path, err)
	}
	return &SQLiteStore{db: db}, nil
}

// DB is shared with the token store so both live in one file.
func (s *SQLiteStore) DB() *sql.DB { return s.db }

func (s *SQLiteStore) CreateJob(ctx context.Context, j *domain.Job) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM jobs`).Scan(&seq); err != nil {
		return err
	}
	if j.ID == "" {
		j.ID = fmt.Sprintf("job_%d", seq)
	}
	doc, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs
		(seq, id, client_id, external_id, printer_id, state, queued_at, payload, idem_key, doc)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		seq, j.ID, j.ClientID, j.ExternalID, j.PrinterID, string(j.State), j.QueuedAt.UnixNano(),
		[]byte(j.Payload), j.IdempotencyKey, string(doc))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") && strings.Contains(err.Error(), "external_id") {
			return fmt.Errorf("external_id %q already used by this client", j.ExternalID)
		}
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("job %s already exists", j.ID)
		}
		return err
	}
	return tx.Commit()
}

const jobColumns = `payload, idem_key, doc`

func scanJob(row interface{ Scan(...any) error }) (*domain.Job, error) {
	var payload []byte
	var idem, doc string
	if err := row.Scan(&payload, &idem, &doc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var j domain.Job
	if err := json.Unmarshal([]byte(doc), &j); err != nil {
		return nil, fmt.Errorf("decode job: %w", err)
	}
	j.Payload, j.IdempotencyKey = string(payload), idem
	return &j, nil
}

func (s *SQLiteStore) GetJob(ctx context.Context, id string) (*domain.Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
}

func (s *SQLiteStore) GetByExternal(ctx context.Context, clientID, externalID string) (*domain.Job, error) {
	return scanJob(s.db.QueryRowContext(ctx,
		`SELECT `+jobColumns+` FROM jobs WHERE client_id = ? AND external_id = ? AND external_id <> ''`,
		clientID, externalID))
}

func (s *SQLiteStore) UpdateJob(ctx context.Context, j *domain.Job) error {
	doc, err := json.Marshal(j)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET printer_id = ?, state = ?, payload = ?, idem_key = ?, doc = ?
		WHERE id = ?`, j.PrinterID, string(j.State), []byte(j.Payload), j.IdempotencyKey, string(doc), j.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) ListJobs(ctx context.Context, f Filter) ([]domain.Job, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		where = append(where, cond)
		args = append(args, v)
	}
	if f.ClientID != "" {
		add("client_id = ?", f.ClientID)
	}
	if f.PrinterID != "" {
		add("printer_id = ?", f.PrinterID)
	}
	if f.State != "" {
		add("state = ?", f.State)
	}
	if f.ExternalID != "" {
		add("external_id = ?", f.ExternalID)
	}
	if f.From != nil {
		add("queued_at >= ?", f.From.UnixNano())
	}
	if f.To != nil {
		add("queued_at <= ?", f.To.UnixNano())
	}
	var ids []string
	for _, id := range f.IDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(f.IDs) > 0 {
		if len(ids) == 0 {
			return []domain.Job{}, nil
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		where = append(where, "id IN ("+ph+")")
		for _, id := range ids {
			args = append(args, id)
		}
	}
	q := `SELECT ` + jobColumns + ` FROM jobs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY queued_at DESC, seq DESC"
	// An id list keeps the caller's order (as the memory store does), so it is
	// limited after reordering, not in SQL.
	if f.Limit > 0 && len(ids) == 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		byID := make(map[string]domain.Job, len(out))
		for _, j := range out {
			byID[j.ID] = j
		}
		out = out[:0]
		for _, id := range ids {
			if j, ok := byID[id]; ok {
				out = append(out, j)
			}
		}
		if f.Limit > 0 && len(out) > f.Limit {
			out = out[:f.Limit]
		}
	}
	return out, nil
}

func (s *SQLiteStore) AddAttempt(ctx context.Context, a *domain.Attempt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO attempts (job_id, doc) VALUES (?, '{}')`, a.JobID)
	if err != nil {
		return err
	}
	if a.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	doc, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attempts SET doc = ? WHERE id = ?`, string(doc), a.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) Attempts(ctx context.Context, jobID string) ([]domain.Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT doc FROM attempts WHERE job_id = ? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Attempt
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var a domain.Attempt
		if err := json.Unmarshal([]byte(doc), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RequeueUnfinished: a job left `sending` by a crash or restart may already be on the
// label, so it becomes sent_unknown — never printed again. Queued jobs stay queued;
// the job service replays them (they were never sent).
func (s *SQLiteStore) RequeueUnfinished(ctx context.Context) (int, error) {
	jobs, err := s.ListJobs(ctx, Filter{State: string(domain.JobSending)})
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for i := range jobs {
		j := &jobs[i]
		j.State = domain.JobSentUnknown
		j.LastError = "cps restarted while the job was in flight; printed state unknown"
		j.FinishedAt = &now
		if err := s.UpdateJob(ctx, j); err != nil {
			return i, err
		}
	}
	return len(jobs), nil
}

func (s *SQLiteStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *SQLiteStore) Close() error { return s.db.Close() }

// PrinterOverrides returns every saved operator edit, keyed by printer id.
func (s *SQLiteStore) PrinterOverrides(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT printer_id, patch FROM printer_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var id, patch string
		if err := rows.Scan(&id, &patch); err != nil {
			return nil, err
		}
		out[id] = json.RawMessage(patch)
	}
	return out, rows.Err()
}

// SavePrinterOverride stores the operator edits for one printer, replacing the last.
func (s *SQLiteStore) SavePrinterOverride(ctx context.Context, printerID string, patch json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO printer_overrides (printer_id, patch, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(printer_id) DO UPDATE SET patch = excluded.patch, updated_at = excluded.updated_at`,
		printerID, string(patch), time.Now().UnixNano())
	return err
}

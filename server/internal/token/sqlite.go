package token

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLiteStore keeps issued tokens — business-system tokens and agent credentials — in
// the server's database, so a restart neither logs out integrations nor forces every
// agent to enroll again. Only the hash of a secret is stored, never the secret.
type SQLiteStore struct {
	db *sql.DB
}

const tokenSchema = `
CREATE TABLE IF NOT EXISTS tokens (
	seq       INTEGER PRIMARY KEY,
	id        TEXT NOT NULL UNIQUE,
	hash      TEXT NOT NULL UNIQUE,
	client_id TEXT NOT NULL,
	doc       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS tokens_client ON tokens(client_id);
`

// NewSQLiteStore uses an open database (the job store's), creating its table if needed.
func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error) {
	if _, err := db.ExecContext(ctx, tokenSchema); err != nil {
		return nil, fmt.Errorf("prepare token schema: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Create(ctx context.Context, t *Token, hash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM tokens`).Scan(&seq); err != nil {
		return err
	}
	if t.ID == "" {
		t.ID = fmt.Sprintf("tok_%d_%s", seq, t.Prefix)
	}
	doc, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tokens (seq, id, hash, client_id, doc) VALUES (?, ?, ?, ?, ?)`,
		seq, t.ID, hash, t.ClientID, string(doc)); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return errors.New("token already exists")
		}
		return err
	}
	return tx.Commit()
}

func scanToken(row interface{ Scan(...any) error }) (*Token, error) {
	var doc string
	if err := row.Scan(&doc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var t Token
	if err := json.Unmarshal([]byte(doc), &t); err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}
	return &t, nil
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (*Token, error) {
	return scanToken(s.db.QueryRowContext(ctx, `SELECT doc FROM tokens WHERE id = ?`, id))
}

func (s *SQLiteStore) GetByHash(ctx context.Context, hash string) (*Token, error) {
	return scanToken(s.db.QueryRowContext(ctx, `SELECT doc FROM tokens WHERE hash = ?`, hash))
}

func (s *SQLiteStore) List(ctx context.Context, clientID string) ([]Token, error) {
	q, args := `SELECT doc FROM tokens ORDER BY seq`, []any{}
	if clientID != "" {
		q, args = `SELECT doc FROM tokens WHERE client_id = ? ORDER BY seq`, []any{clientID}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// update applies fn to one token inside a transaction.
func (s *SQLiteStore) update(ctx context.Context, id string, fn func(t *Token)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t, err := scanToken(tx.QueryRowContext(ctx, `SELECT doc FROM tokens WHERE id = ?`, id))
	if err != nil {
		return err
	}
	fn(t)
	doc, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tokens SET doc = ? WHERE id = ?`, string(doc), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) Revoke(ctx context.Context, id string, at time.Time) error {
	return s.update(ctx, id, func(t *Token) {
		if t.RevokedAt == nil {
			t.RevokedAt = &at
		}
	})
}

func (s *SQLiteStore) Touch(ctx context.Context, id string, at time.Time) error {
	return s.update(ctx, id, func(t *Token) {
		t.LastUsedAt = &at
		t.Uses++
	})
}

func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

package aguistore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
)

// SQL is a Store over one database/sql table, aguistore_threads, which
// OpenSQL creates when it is missing.
//
// Its statements use ? placeholders and an INSERT ... ON CONFLICT upsert, as
// SQLite 3.35 and later runs them, RETURNING included; the tests run modernc.org/sqlite. A save
// is one statement, so concurrent runs on a thread each land whole, the later
// one winning.
type SQL struct {
	db  *sql.DB
	cfg config
}

const schema = `CREATE TABLE IF NOT EXISTS aguistore_threads (
	user_id TEXT NOT NULL,
	thread_id TEXT NOT NULL,
	title TEXT NOT NULL,
	preview TEXT NOT NULL,
	messages TEXT NOT NULL,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (user_id, thread_id)
)`

// OpenSQL returns a store over db, creating its table if need be.
func OpenSQL(ctx context.Context, db *sql.DB, opts ...Option) (*SQL, error) {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, fmt.Errorf("aguistore: creating the table: %w", err)
	}
	return &SQL{db: db, cfg: configure(opts)}, nil
}

// LoadThread returns the thread's messages.
func (s *SQL) LoadThread(ctx context.Context, userID, threadID string) ([]aguix.Message, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT messages FROM aguistore_threads WHERE user_id = ? AND thread_id = ?`, userID, threadID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("aguistore: loading the thread: %w", err)
	}
	messages, err := decodeMessages([]byte(raw))
	return messages, err == nil, err
}

// SaveThread upserts the thread, leaving a stored title as it is.
func (s *SQL) SaveThread(ctx context.Context, userID, threadID string, messages []aguix.Message) error {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("aguistore: encoding the thread: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO aguistore_threads
		(user_id, thread_id, title, preview, messages, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, thread_id) DO UPDATE SET
		preview = excluded.preview, messages = excluded.messages, updated_at = excluded.updated_at`,
		userID, threadID, titleFrom(messages), previewFrom(messages), string(encoded), s.cfg.now().UnixMicro())
	if err != nil {
		return fmt.Errorf("aguistore: saving the thread: %w", err)
	}
	return nil
}

// ListThreads returns the user's threads, newest first.
func (s *SQL) ListThreads(ctx context.Context, userID string, limit int) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id, title, preview, updated_at FROM aguistore_threads
		WHERE user_id = ? ORDER BY updated_at DESC, thread_id LIMIT ?`, userID, max(limit, 0))
	if err != nil {
		return nil, fmt.Errorf("aguistore: listing threads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Thread{}
	for rows.Next() {
		var (
			t  Thread
			at int64
		)
		if err := rows.Scan(&t.ID, &t.Title, &t.Preview, &at); err != nil {
			return nil, fmt.Errorf("aguistore: listing threads: %w", err)
		}
		t.UpdatedAt = time.UnixMicro(at).UTC()
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("aguistore: listing threads: %w", err)
	}
	return out, nil
}

// RenameThread sets the thread's title.
func (s *SQL) RenameThread(ctx context.Context, userID, threadID, title string) error {
	var id string
	err := s.db.QueryRowContext(ctx, `UPDATE aguistore_threads SET title = ?
		WHERE user_id = ? AND thread_id = ? RETURNING thread_id`, title, userID, threadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: thread %q", services.ErrNotFound, threadID)
	}
	if err != nil {
		return fmt.Errorf("aguistore: renaming the thread: %w", err)
	}
	return nil
}

// DeleteThread removes the thread.
func (s *SQL) DeleteThread(ctx context.Context, userID, threadID string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM aguistore_threads WHERE user_id = ? AND thread_id = ?`, userID, threadID); err != nil {
		return fmt.Errorf("aguistore: deleting the thread: %w", err)
	}
	return nil
}

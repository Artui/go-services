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

// SQL is a RunStore over two database/sql tables, aguistore_threads for the
// messages and aguistore_runs for the run status, which OpenSQL creates when
// they are missing; a database aguistore v0.1.0 made gains the second.
//
// It speaks SQLite unless WithDialect names Postgres or MySQL. A save is one
// statement, so concurrent runs on a thread each land whole, the later one
// winning. A list is ordered by the query alone: newest first, then by thread
// id byte for byte, as the Memory store orders it.
type SQL struct {
	db  *sql.DB
	cfg config
}

// schema is the threads table in SQLite, the one table aguistore v0.1.0 made.
var schema = schemas(SQLite)[0]

// schemas are the store's tables in d's column types.
func schemas(d Dialect) []string {
	key, text := d.keyType(), d.textType()
	return []string{`CREATE TABLE IF NOT EXISTS aguistore_threads (
	user_id ` + key + ` NOT NULL,
	thread_id ` + key + ` NOT NULL,
	title ` + text + ` NOT NULL,
	preview ` + text + ` NOT NULL,
	messages ` + text + ` NOT NULL,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (user_id, thread_id)
)`, `CREATE TABLE IF NOT EXISTS aguistore_runs (
	user_id ` + key + ` NOT NULL,
	thread_id ` + key + ` NOT NULL,
	run_id ` + text + ` NOT NULL,
	status ` + text + ` NOT NULL,
	reason ` + text + ` NOT NULL,
	interrupts ` + text + ` NOT NULL,
	last_read_at BIGINT,
	PRIMARY KEY (user_id, thread_id)
)`}
}

// OpenSQL returns a store over db, creating its tables if need be.
func OpenSQL(ctx context.Context, db *sql.DB, opts ...Option) (*SQL, error) {
	cfg := configure(opts)
	for _, create := range schemas(cfg.dialect) {
		if _, err := db.ExecContext(ctx, create); err != nil {
			return nil, fmt.Errorf("aguistore: creating the tables: %w", err)
		}
	}
	return &SQL{db: db, cfg: cfg}, nil
}

// q renders a statement in the store's dialect.
func (s *SQL) q(query string) string { return s.cfg.dialect.rebind(query) }

// LoadThread returns the thread's messages.
func (s *SQL) LoadThread(ctx context.Context, userID, threadID string) ([]aguix.Message, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		s.q(`SELECT messages FROM aguistore_threads WHERE user_id = ? AND thread_id = ?`), userID, threadID,
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
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO aguistore_threads
		(user_id, thread_id, title, preview, messages, updated_at) VALUES (?, ?, ?, ?, ?, ?)`+
		s.cfg.dialect.upsert("user_id", "thread_id")+
		`preview = excluded.preview, messages = excluded.messages, updated_at = excluded.updated_at`),
		userID, threadID, titleFrom(messages), previewFrom(messages), string(encoded), s.cfg.now().UnixMicro())
	if err != nil {
		return fmt.Errorf("aguistore: saving the thread: %w", err)
	}
	return nil
}

// ListThreads returns the user's threads, newest first.
func (s *SQL) ListThreads(ctx context.Context, userID string, limit int) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT t.thread_id, t.title, t.preview, t.updated_at,
		COALESCE(r.status, ''), COALESCE(r.reason, ''), COALESCE(r.interrupts, ''), r.last_read_at
		FROM aguistore_threads t LEFT JOIN aguistore_runs r ON r.user_id = t.user_id AND r.thread_id = t.thread_id
		WHERE t.user_id = ? ORDER BY t.updated_at DESC, t.thread_id LIMIT ?`), userID, max(limit, 0))
	if err != nil {
		return nil, fmt.Errorf("aguistore: listing threads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Thread{}
	for rows.Next() {
		var (
			t          Thread
			at         int64
			interrupts string
			read       sql.NullInt64
		)
		if err := rows.Scan(&t.ID, &t.Title, &t.Preview, &at, &t.Status, &t.Reason, &interrupts, &read); err != nil {
			return nil, fmt.Errorf("aguistore: listing threads: %w", err)
		}
		t.UpdatedAt = time.UnixMicro(at).UTC()
		if interrupts != "" {
			t.Interrupts = json.RawMessage(interrupts)
		}
		if read.Valid {
			t.LastReadAt = time.UnixMicro(read.Int64).UTC()
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("aguistore: listing threads: %w", err)
	}
	return out, nil
}

// RenameThread sets the thread's title. An update that touched no row is
// checked against the table, since MySQL counts only the rows it changed.
func (s *SQL) RenameThread(ctx context.Context, userID, threadID, title string) error {
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE aguistore_threads SET title = ?
		WHERE user_id = ? AND thread_id = ?`), title, userID, threadID)
	if err != nil {
		return fmt.Errorf("aguistore: renaming the thread: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		if err != nil {
			return fmt.Errorf("aguistore: renaming the thread: %w", err)
		}
		return nil
	}
	return s.exists(ctx, userID, threadID, "renaming the thread")
}

// exists answers nil when the user has the thread, else an ErrNotFound.
func (s *SQL) exists(ctx context.Context, userID, threadID, doing string) error {
	var one int
	err := s.db.QueryRowContext(ctx, s.q(`SELECT 1 FROM aguistore_threads WHERE user_id = ? AND thread_id = ?`),
		userID, threadID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: thread %q", services.ErrNotFound, threadID)
	}
	if err != nil {
		return fmt.Errorf("aguistore: %s: %w", doing, err)
	}
	return nil
}

// DeleteThread removes the thread and its run status.
func (s *SQL) DeleteThread(ctx context.Context, userID, threadID string) error {
	for _, table := range []string{"aguistore_threads", "aguistore_runs"} {
		if _, err := s.db.ExecContext(ctx,
			s.q(`DELETE FROM `+table+` WHERE user_id = ? AND thread_id = ?`), userID, threadID); err != nil {
			return fmt.Errorf("aguistore: deleting the thread: %w", err)
		}
	}
	return nil
}

// StartRun marks the thread running. A resume's claim is a conditional update
// on the run it parked under, so of two resumes of one park only one wins.
func (s *SQL) StartRun(ctx context.Context, userID, threadID, runID string, resumes []string) (bool, error) {
	if len(resumes) == 0 {
		_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO aguistore_runs
			(user_id, thread_id, run_id, status, reason, interrupts) VALUES (?, ?, ?, ?, '', '')`+
			s.cfg.dialect.upsert("user_id", "thread_id")+
			`run_id = excluded.run_id, status = excluded.status, reason = '', interrupts = ''`),
			userID, threadID, runID, StatusRunning)
		if err != nil {
			return false, fmt.Errorf("aguistore: starting the run: %w", err)
		}
		return true, nil
	}
	var parkedRun, status, interrupts string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT run_id, status, interrupts FROM aguistore_runs
		WHERE user_id = ? AND thread_id = ?`), userID, threadID).Scan(&parkedRun, &status, &interrupts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("aguistore: claiming the run: %w", err)
	}
	if !claimable(status, []byte(interrupts), resumes) {
		return false, nil
	}
	return s.take(ctx, userID, threadID, runID, parkedRun)
}

// take moves the park of parkedRun to runID, unless another run took it since.
func (s *SQL) take(ctx context.Context, userID, threadID, runID, parkedRun string) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE aguistore_runs SET run_id = ?, status = ?, reason = '', interrupts = ''
		WHERE user_id = ? AND thread_id = ? AND run_id = ? AND status = ?`),
		runID, StatusRunning, userID, threadID, parkedRun, StatusWaiting)
	if err != nil {
		return false, fmt.Errorf("aguistore: claiming the run: %w", err)
	}
	won, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("aguistore: claiming the run: %w", err)
	}
	return won == 1, nil
}

// FinishRun records how runID ended, unless another run has replaced it.
func (s *SQL) FinishRun(
	ctx context.Context, userID, threadID, runID, status, reason string, interrupts json.RawMessage,
) error {
	if err := checkFinish(status, interrupts); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, s.q(`UPDATE aguistore_runs SET status = ?, reason = ?, interrupts = ?
		WHERE user_id = ? AND thread_id = ? AND run_id = ?`),
		status, reason, string(interrupts), userID, threadID, runID); err != nil {
		return fmt.Errorf("aguistore: finishing the run: %w", err)
	}
	return nil
}

// MarkRead stamps the thread read.
func (s *SQL) MarkRead(ctx context.Context, userID, threadID string) error {
	if err := s.exists(ctx, userID, threadID, "marking the thread read"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, s.q(`INSERT INTO aguistore_runs
		(user_id, thread_id, run_id, status, reason, interrupts, last_read_at) VALUES (?, ?, '', ?, '', '', ?)`+
		s.cfg.dialect.upsert("user_id", "thread_id")+`last_read_at = excluded.last_read_at`),
		userID, threadID, StatusIdle, s.cfg.now().UnixMicro()); err != nil {
		return fmt.Errorf("aguistore: marking the thread read: %w", err)
	}
	return nil
}

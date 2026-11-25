package aguistore

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/unicode/runenames"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// ADKMemory is ADK's long-term memory (memory.Service) over one database/sql
// table, aguistore_memory, which OpenADKMemory creates when it is missing.
//
// It keeps each session's events with content, keyed by app, user and session,
// so a search answers from the asking user's own sessions only. Adding a
// session again replaces what it stored, as ADK's in-memory service does. A
// search is that one's keyword match: text is NFC-normalised and lower-cased,
// and an event scores one for each query word among its own words (a word
// that mixes Latin with another script also counting each run of one script),
// or anywhere in its text for a query word with non-ASCII letters. Thought
// parts are left out, and the ten best come back, highest first and oldest
// first among equals. It speaks the dialect
// WithDialect names, SQLite by default, and reads no other option.
type ADKMemory struct {
	db *sql.DB
	d  Dialect
}

var _ memory.Service = (*ADKMemory)(nil)

// maxMemories is how many entries a search returns, ADK's in-memory cap.
const maxMemories = 10

// memorySchema is the memory table in d's column types.
func memorySchema(d Dialect) string {
	key, text := d.keyType(), d.textType()
	return `CREATE TABLE IF NOT EXISTS aguistore_memory (
	app_name ` + key + ` NOT NULL,
	user_id ` + key + ` NOT NULL,
	session_id ` + key + ` NOT NULL,
	seq BIGINT NOT NULL,
	event_id ` + text + ` NOT NULL,
	author ` + text + ` NOT NULL,
	at BIGINT NOT NULL,
	content ` + text + ` NOT NULL,
	metadata ` + text + ` NOT NULL,
	PRIMARY KEY (app_name, user_id, session_id, seq)
)`
}

// OpenADKMemory returns a memory service over db, creating its table if need be.
func OpenADKMemory(ctx context.Context, db *sql.DB, opts ...Option) (*ADKMemory, error) {
	d := configure(opts).dialect
	if _, err := db.ExecContext(ctx, memorySchema(d)); err != nil {
		return nil, fmt.Errorf("aguistore: creating the memory table: %w", err)
	}
	return &ADKMemory{db: db, d: d}, nil
}

// memoryRow is one stored event, encoded.
type memoryRow struct {
	eventID, author, content, metadata string
	at                                 int64
}

// AddSessionToMemory stores the session's events in one transaction,
// replacing what an earlier add of the same session stored.
func (m *ADKMemory) AddSessionToMemory(ctx context.Context, s session.Session) error {
	if s == nil {
		return errors.New("aguistore: AddSessionToMemory needs a session")
	}
	rows, err := memoryRows(s)
	if err != nil {
		return fmt.Errorf("aguistore: encoding the session's memory: %w", err)
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("aguistore: adding to memory: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	insert := m.d.rebind(`INSERT INTO aguistore_memory
		(app_name, user_id, session_id, seq, event_id, author, at, content, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)` + m.d.upsert("app_name", "user_id", "session_id", "seq") +
		`event_id = excluded.event_id, author = excluded.author, at = excluded.at,
		content = excluded.content, metadata = excluded.metadata`)
	for i, r := range rows {
		if _, err := tx.ExecContext(ctx, insert, s.AppName(), s.UserID(), s.ID(), i,
			r.eventID, r.author, r.at, r.content, r.metadata); err != nil {
			return fmt.Errorf("aguistore: adding to memory: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, m.d.rebind(`DELETE FROM aguistore_memory
		WHERE app_name = ? AND user_id = ? AND session_id = ? AND seq >= ?`),
		s.AppName(), s.UserID(), s.ID(), len(rows)); err != nil {
		return fmt.Errorf("aguistore: adding to memory: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("aguistore: adding to memory: %w", err)
	}
	return nil
}

// memoryRows encodes the session's events that carry visible content.
func memoryRows(s session.Session) ([]memoryRow, error) {
	var out []memoryRow
	events := s.Events()
	if events == nil {
		return nil, nil
	}
	for ev := range events.All() {
		if ev == nil || ev.Content == nil {
			continue
		}
		visible := *ev.Content
		visible.Parts = slices.DeleteFunc(slices.Clone(ev.Content.Parts), func(p *genai.Part) bool {
			return p == nil || p.Thought
		})
		if len(visible.Parts) == 0 {
			continue
		}
		content, err := json.Marshal(&visible)
		if err != nil {
			return nil, err
		}
		metadata, err := json.Marshal(ev.CustomMetadata)
		if err != nil {
			return nil, err
		}
		out = append(out, memoryRow{
			eventID: ev.ID, author: ev.Author, at: ev.Timestamp.UnixMicro(),
			content: string(content), metadata: string(metadata),
		})
	}
	return out, nil
}

// SearchMemory returns the user's entries that share a word with the query.
func (m *ADKMemory) SearchMemory(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResponse, error) {
	if req == nil {
		return nil, errors.New("aguistore: SearchMemory needs a request")
	}
	res := &memory.SearchResponse{}
	query := queryWords(req.Query)
	if len(query) == 0 {
		return res, nil
	}
	rows, err := m.db.QueryContext(ctx, m.d.rebind(`SELECT event_id, author, at, content, metadata
		FROM aguistore_memory WHERE app_name = ? AND user_id = ? ORDER BY at, session_id, seq`),
		req.AppName, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("aguistore: searching memory: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type scored struct {
		score int
		entry memory.Entry
	}
	var found []scored
	for rows.Next() {
		var r memoryRow
		if err := rows.Scan(&r.eventID, &r.author, &r.at, &r.content, &r.metadata); err != nil {
			return nil, fmt.Errorf("aguistore: searching memory: %w", err)
		}
		entry, err := r.entry()
		if err != nil {
			return nil, fmt.Errorf("aguistore: decoding a memory: %w", err)
		}
		if score := overlap(query, entry.Content); score > 0 {
			found = append(found, scored{score, entry})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("aguistore: searching memory: %w", err)
	}
	slices.SortStableFunc(found, func(a, b scored) int { return cmp.Compare(b.score, a.score) })
	for _, f := range found[:min(len(found), maxMemories)] {
		res.Memories = append(res.Memories, f.entry)
	}
	return res, nil
}

func (r memoryRow) entry() (memory.Entry, error) {
	e := memory.Entry{ID: r.eventID, Author: r.author, Timestamp: time.UnixMicro(r.at).UTC()}
	if err := json.Unmarshal([]byte(r.content), &e.Content); err != nil {
		return e, err
	}
	return e, json.Unmarshal([]byte(r.metadata), &e.CustomMetadata)
}

// overlap counts the query's words the content's text holds: as a word, or,
// for a word with non-ASCII letters, anywhere in the text, as ADK's in-memory
// service matches them.
func overlap(query map[string]bool, c *genai.Content) int {
	var texts []string
	for _, p := range c.Parts {
		texts = append(texts, p.Text)
	}
	text := strings.Join(texts, " ")
	own := searchableWords(text)
	lower := cases.Lower(language.Und).String(norm.NFC.String(text))
	n := 0
	for w, substring := range query {
		if _, ok := own[w]; ok || (substring && strings.Contains(lower, w)) {
			n++
		}
	}
	return n
}

// queryWords maps each of the query's words to whether it may match inside a
// longer word, which one with non-ASCII letters may.
func queryWords(text string) map[string]bool {
	out := map[string]bool{}
	for w := range words(text) {
		out[w] = hasNonASCII(w)
	}
	return out
}

// words splits NFC-normalised text into its lower-cased words: runs of
// letters, digits and _.
func words(text string) map[string]struct{} {
	lower := cases.Lower(language.Und)
	out := map[string]struct{}{}
	for w := range strings.FieldsFuncSeq(norm.NFC.String(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_'
	}) {
		out[lower.String(w)] = struct{}{}
	}
	return out
}

// searchableWords is words plus, for a word that mixes Latin and other
// scripts, each run of one script, so "Python" is found in "私はPythonを使う".
func searchableWords(text string) map[string]struct{} {
	out := words(text)
	for _, w := range slices.Collect(maps.Keys(out)) {
		if !hasNonASCII(w) {
			continue
		}
		start, runLatin := 0, false
		for i, r := range w {
			latin := isLatin(r)
			if i > 0 && latin != runLatin {
				out[w[start:i]] = struct{}{}
				start = i
			}
			runLatin = latin
		}
		out[w[start:]] = struct{}{}
	}
	return out
}

func isLatin(r rune) bool {
	if r <= unicode.MaxASCII {
		return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_'
	}
	return strings.HasPrefix(runenames.Name(r), "LATIN")
}

func hasNonASCII(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r > unicode.MaxASCII }) >= 0
}

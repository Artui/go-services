package aguistore

import (
	"strconv"
	"strings"
)

// Dialect is the SQL a database/sql store speaks: its placeholders, its
// upsert, and column types under which keys compare and sort byte for byte,
// as Go's strings do.
//
// SQLite is the zero value. Pick another with WithDialect; the store's
// statements are written once, with ? placeholders and an upsert whose update
// reads the new row as excluded, and each dialect renders them as its own.
type Dialect struct {
	name     string
	key      string
	text     string
	numbered bool
	mysql    bool
}

var (
	// SQLite runs on SQLite 3.35 and later; the tests use modernc.org/sqlite.
	SQLite = Dialect{}
	// Postgres runs on PostgreSQL 13 and later, keys collated "C"; CI tests it
	// with github.com/jackc/pgx/v5/stdlib.
	Postgres = Dialect{name: "postgres", key: `TEXT COLLATE "C"`, text: "TEXT", numbered: true}
	// MySQL runs on MySQL 8.0.19 and later, keys VARCHAR(255) in utf8mb4_bin, so
	// a user or thread id is at most 255 characters; CI tests it with
	// github.com/go-sql-driver/mysql.
	MySQL = Dialect{
		name: "mysql", key: "VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin",
		text: "LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin", mysql: true,
	}
)

// WithDialect sets the SQL dialect OpenSQL speaks; the default is SQLite.
func WithDialect(d Dialect) Option {
	return func(c *config) { c.dialect = d }
}

// String names the dialect.
func (d Dialect) String() string {
	if d.name == "" {
		return "sqlite"
	}
	return d.name
}

// keyType is the column type of a user, thread or other key.
func (d Dialect) keyType() string {
	if d.key == "" {
		return "TEXT"
	}
	return d.key
}

// textType is the column type of any other text.
func (d Dialect) textType() string {
	if d.text == "" {
		return "TEXT"
	}
	return d.text
}

// rebind renders a statement's ? placeholders as the dialect's; no statement
// here holds a ? inside a literal.
func (d Dialect) rebind(query string) string {
	if !d.numbered {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r != '?' {
			b.WriteRune(r)
			continue
		}
		n++
		b.WriteString("$" + strconv.Itoa(n))
	}
	return b.String()
}

// upsert is what follows an INSERT's VALUES (...) to update the row whose keys
// collide, the SET list after it reading the new row as excluded.
func (d Dialect) upsert(keys ...string) string {
	if d.mysql {
		return " AS excluded ON DUPLICATE KEY UPDATE "
	}
	return " ON CONFLICT (" + strings.Join(keys, ", ") + ") DO UPDATE SET "
}

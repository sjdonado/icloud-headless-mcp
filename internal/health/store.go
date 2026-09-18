// Package health ingests an Apple Health app-export folder (monthly JSONL
// per metric plus monthly tombstone files) into SQLite and answers
// read-only questions about it. Only the importer opens the database
// read-write; every tool opens it read-only.
package health

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Schema is the store layout, ported as-is: the shape is deliberate and
// shared with the systems that already consume it.
const Schema = `
PRAGMA journal_mode=WAL;

CREATE TABLE IF NOT EXISTS samples (
    uuid          TEXT PRIMARY KEY,
    metric        TEXT NOT NULL,
    record_type   TEXT,
    start_at      TEXT NOT NULL,
    end_at        TEXT,
    local_date    TEXT,
    timezone      TEXT,
    -- One of these two is set, never both.
    value_num     REAL,
    value_code    INTEGER,
    value_label   TEXT,
    value_type    TEXT,
    unit          TEXT,
    source        TEXT,
    source_bundle TEXT,
    device        TEXT,
    user_entered  INTEGER NOT NULL DEFAULT 0,
    recorded_at   TEXT,
    schema_version INTEGER
);
CREATE INDEX IF NOT EXISTS s_metric_date ON samples (metric, local_date);
CREATE INDEX IF NOT EXISTS s_start       ON samples (start_at);

CREATE TABLE IF NOT EXISTS imports (
    id INTEGER PRIMARY KEY, file_name TEXT NOT NULL, metric TEXT,
    rows_seen INTEGER NOT NULL, rows_new INTEGER NOT NULL, imported_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tombstones (
    uuid TEXT PRIMARY KEY, recorded_at TEXT, applied INTEGER NOT NULL DEFAULT 0
);
`

// OpenRW opens the store for the importer: read-write, WAL applied. It
// creates the parent directory and the schema on first use.
func OpenRW(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("no health database configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		return nil, err
	}
	// Single connection: PRAGMAs below then hold for every statement on
	// this handle, and the single-threaded importer wants no more.
	// IMMEDIATE (not deferred) transactions serialize a timer run against
	// a manual one at BEGIN time instead of failing busy at first write.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(Schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	// The file itself, not just its parent: MkdirAll cannot fix what the
	// umask gives the database on creation.
	_ = os.Chmod(path, 0o600)
	_ = os.Chmod(path+"-wal", 0o600)
	_ = os.Chmod(path+"-shm", 0o600)
	return db, nil
}

// OpenRO opens the store for query tools: read-only at the driver level
// plus query-only at the engine level, so a tool cannot damage the data
// even past a gate bug. One pooled connection, so both PRAGMAs hold for
// every statement on the handle.
func OpenRO(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("no health database configured")
	}
	// Only ? and # break the query string the mode flag rides in; slashes
	// stay literal so the path keeps pointing where it should.
	escaped := strings.NewReplacer("?", "%3F", "#", "%23").Replace(path)
	db, err := sql.Open("sqlite", "file:"+escaped+"?mode=ro")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

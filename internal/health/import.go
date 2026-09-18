package health

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// storer is what the importer reads and writes: *sql.DB in production
// and self-test, *sql.Tx inside one file's transaction. Both satisfy it,
// so the import path is written once.
type storer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// record mirrors one export envelope line. Optional strings are pointers
// so an absent field stores NULL, never "": empty string asserts "known
// to be blank" where NULL means absent, and the distinction is load-bearing
// for WHERE ... IS NULL queries and for re-imports over adopted stores.
type record struct {
	UUID          string          `json:"uuid"`
	Metric        string          `json:"metric"`
	RecordType    *string         `json:"recordType"`
	Start         string          `json:"start"`
	End           *string         `json:"end"`
	LocalDate     *string         `json:"localDate"`
	Timezone      *string         `json:"timezone"`
	Value         json.RawMessage `json:"value"`
	Unit          *string         `json:"unit"`
	Source        *string         `json:"source"`
	SourceBundle  *string         `json:"sourceBundleId"`
	Device        *string         `json:"device"`
	WasEntered    any             `json:"wasUserEntered"`
	RecordedAt    *string         `json:"recordedAt"`
	SchemaVersion any             `json:"schemaVersion"`
}

// splitValue divides the polymorphic value without flattening it. The app
// emits an object for both kinds, distinguished by a type field, so the
// branch is on value.type, not on the JSON kind: {"amount":N,"type":
// "quantity"} stores an amount, {"code":C,"label":L,"type":"category"}
// stores a code plus its readable label. A bare JSON number stays a
// quantity as a guard, though the app never emits it. A null or absent
// value is unknown, never zero: parsing JSON null as a number succeeds
// and would silently mint 0-amount samples into every SUM. An object whose
// type is neither quantity nor category, or a quantity with no numeric
// amount, is an error: storing the row would mint a number that silently
// vanished, which is exactly what the null-not-zero rule exists to
// prevent. Failing the file is loud; a mislabeled row is silent.
func splitValue(raw json.RawMessage) (num *float64, code *int64, label string, typ string, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil, "", "unknown", nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return &f, nil, "", "quantity", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return nil, nil, strings.TrimSpace(s), "category", nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil, strings.TrimSpace(string(raw)), "unknown", nil
	}
	t, _ := obj["type"].(string)
	if t == "quantity" {
		amt, ok := obj["amount"].(float64)
		if !ok {
			return nil, nil, "", "", fmt.Errorf("quantity value without a numeric amount")
		}
		return &amt, nil, "", "quantity", nil
	}
	// A workout has no single number, so the faithful store is the raw
	// object under a workout type: strictly better than the old
	// importer's three NULLs, and distinct from the quantity bug (where a
	// number existed and was discarded).
	if t == "workout" {
		compact, err := json.Marshal(obj)
		if err != nil {
			return nil, nil, "", "", fmt.Errorf("workout value not re-encodable: %v", err)
		}
		return nil, nil, string(compact), "workout", nil
	}
	if t != "category" {
		return nil, nil, "", "", fmt.Errorf("unknown value type %q", t)
	}
	if v, ok := obj["code"]; ok {
		switch n := v.(type) {
		case float64:
			i := int64(n)
			code = &i
		case string:
			if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
				code = &i
			}
		}
	}
	for _, key := range []string{"label", "value", "displayString", "name"} {
		if s, ok := obj[key].(string); ok && strings.TrimSpace(s) != "" {
			label = strings.TrimSpace(s)
			break
		}
	}
	if label == "" {
		if compact, err := json.Marshal(obj); err == nil {
			label = string(compact)
		}
	}
	return nil, code, label, "category", nil
}

func asBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case float64:
		return b != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(b))
		return s == "true" || s == "1" || s == "yes"
	}
	return false
}

func asInt(v any) any {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
	case bool:
		if n {
			return int64(1)
		}
		return int64(0)
	}
	return nil
}

// FileResult is one imported file's ledger row. New counts inserts;
// Updated counts existing rows whose parsed content differed and was
// rewritten. Identical re-imports report zeros for both, so "0 new"
// always means "changed nothing" again.
type FileResult struct {
	File    string
	Metric  string
	Seen    int
	New     int
	Updated int
}

// ImportDir imports every monthly file under root/<metric>/ plus
// root/_tombstones/, in sorted order, recording one ledger row per file.
// That is the layout the app writes: metric directories sit at the top
// level with _tombstones as their sibling; there is no raw/ level.
// Months copy individually and in any order: tombstones apply on arrival
// in both directions, so import order cannot strand a deletion.
func ImportDir(db *sql.DB, root string, now time.Time) ([]FileResult, error) {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("export dir %s is not a directory", root)
	}
	var files []struct {
		path, metric string
		tomb         bool
	}
	var top []string
	var loose []string
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			if strings.HasSuffix(e.Name(), ".jsonl") {
				loose = append(loose, e.Name())
			}
			continue
		}
		top = append(top, e.Name())
		month, err := os.ReadDir(filepath.Join(root, e.Name()))
		if err != nil {
			return nil, err
		}
		tomb := e.Name() == "_tombstones"
		metric := ""
		if !tomb {
			metric = e.Name()
		}
		for _, m := range month {
			if m.IsDir() || !strings.HasSuffix(m.Name(), ".jsonl") {
				continue
			}
			files = append(files, struct {
				path, metric string
				tomb         bool
			}{filepath.Join(root, e.Name(), m.Name()), metric, tomb})
		}
	}
	// Loud mismatch, never a silent success: directories exist but zero
	// sample files were read, so this root is some other shape (a raw/
	// level, a bare folder, a typo'd path). Reporting "0 new" here once
	// applied deletions while ingesting nothing, so this fails and names
	// what it found. Two quiet cases: an empty root (nothing staged yet),
	// and a tombstones-only root, which is the legitimate early-arrival
	// staging the tombstone spec scenario requires.
	samples := 0
	for _, f := range files {
		if !f.tomb {
			samples++
		}
	}
	if samples == 0 && len(top) > 0 && !(len(top) == 1 && top[0] == "_tombstones") {
		known := []string{}
		for _, name := range top {
			if name != "_tombstones" {
				known = append(known, name)
			}
		}
		return nil, fmt.Errorf("export dir %s holds %s but no <metric>/YYYY-MM.jsonl sample files: want metric directories beside _tombstones/",
			root, strings.Join(known, ", "))
	}
	if len(loose) > 0 {
		return nil, fmt.Errorf("export dir %s holds loose monthly files (%s): move them under a <metric>/ directory",
			root, strings.Join(loose, ", "))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	var out []FileResult
	for _, f := range files {
		// One file, one transaction, ledger row included: a crash or a
		// concurrent import leaves whole files, never half a file with
		// no ledger row, and BEGIN IMMEDIATE serializes a timer run
		// against a manual one (plus busy_timeout on the handles).
		tx, err := db.Begin()
		if err != nil {
			return out, err
		}
		seen, fresh, refreshed, ferr := importFile(tx, f.path, f.metric, f.tomb)
		if ferr != nil {
			_ = tx.Rollback()
			return out, fmt.Errorf("%s: %w", f.path, ferr)
		}
		rel, _ := filepath.Rel(root, f.path)
		if _, err := tx.Exec(`INSERT INTO imports(file_name, metric, rows_seen, rows_new, imported_at) VALUES(?,?,?,?,?)`,
			rel, f.metric, seen, fresh, now.UTC().Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			return out, err
		}
		if err := tx.Commit(); err != nil {
			return out, err
		}
		out = append(out, FileResult{File: rel, Metric: f.metric, Seen: seen, New: fresh, Updated: refreshed})
	}
	return out, nil
}

func importFile(db storer, path, metric string, tombstones bool) (seen, fresh, refreshed int, err error) {
	fh, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, err
	}
	defer fh.Close()
	scan := bufio.NewScanner(fh)
	scan.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scan.Scan() {
		line := bytes.TrimSpace(scan.Bytes())
		if len(line) == 0 {
			continue
		}
		seen++
		n, u, err := importLine(db, line, metric, tombstones)
		if err != nil {
			return seen, fresh, refreshed, fmt.Errorf("line %d: %w", seen, err)
		}
		fresh += n
		refreshed += u
	}
	return seen, fresh, refreshed, scan.Err()
}

func importLine(db storer, line []byte, metric string, tombstones bool) (fresh, refreshed int, err error) {
	if tombstones {
		return importTombstone(db, line)
	}
	var r record
	if err := json.Unmarshal(line, &r); err != nil {
		return 0, 0, err
	}
	if r.UUID == "" || r.Start == "" {
		return 0, 0, fmt.Errorf("record without uuid or start")
	}
	if r.Metric == "" {
		r.Metric = metric
	}
	// A tombstoned uuid never materializes: skipping the insert keeps
	// re-imports at zero new rows with identical counts, whichever side
	// arrived first.
	if gone, err := tombstoned(db, r.UUID); err != nil {
		return 0, 0, err
	} else if gone {
		return 0, 0, nil
	}
	num, code, label, typ, err := splitValue(r.Value)
	if err != nil {
		return 0, 0, err
	}
	// Absent label is NULL, not "": quantities carry no label, and an
	// empty string would assert "known to be blank" where NULL means
	// absent (and would rewrite adopted NULL rows for no gain).
	var labelArg any
	if label != "" {
		labelArg = label
	}
	entered := boolInt(asBool(r.WasEntered))
	schema := asInt(r.SchemaVersion)
	res, err := db.Exec(`INSERT OR IGNORE INTO samples(uuid, metric, record_type, start_at, end_at, local_date, timezone, value_num, value_code, value_label, value_type, unit, source, source_bundle, device, user_entered, recorded_at, schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.UUID, r.Metric, r.RecordType, r.Start, r.End, r.LocalDate, r.Timezone,
		num, code, labelArg, typ, r.Unit, r.Source, r.SourceBundle, r.Device,
		entered, r.RecordedAt, schema)
	if err != nil {
		return 0, 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	if affected == 1 {
		if err := applyPendingTombstone(db, r.UUID); err != nil {
			return 1, 0, err
		}
		return 1, 0, nil
	}
	// The row exists: refresh it only when the parsed content differs,
	// and say so. An unconditional rewrite here once overwrote good rows
	// with a broken parse while reporting 0 new, so identical content
	// writes nothing and any rewrite is counted as updated, never new.
	res, err = db.Exec(`UPDATE samples SET metric=?, record_type=?, start_at=?, end_at=?, local_date=?, timezone=?, value_num=?, value_code=?, value_label=?, value_type=?, unit=?, source=?, source_bundle=?, device=?, user_entered=?, recorded_at=?, schema_version=? WHERE uuid=? AND NOT (metric IS ? AND record_type IS ? AND start_at IS ? AND end_at IS ? AND local_date IS ? AND timezone IS ? AND value_num IS ? AND value_code IS ? AND value_label IS ? AND value_type IS ? AND unit IS ? AND source IS ? AND source_bundle IS ? AND device IS ? AND user_entered IS ? AND recorded_at IS ? AND schema_version IS ?)`,
		r.Metric, r.RecordType, r.Start, r.End, r.LocalDate, r.Timezone,
		num, code, labelArg, typ, r.Unit, r.Source, r.SourceBundle, r.Device,
		entered, r.RecordedAt, schema, r.UUID,
		r.Metric, r.RecordType, r.Start, r.End, r.LocalDate, r.Timezone,
		num, code, labelArg, typ, r.Unit, r.Source, r.SourceBundle, r.Device,
		entered, r.RecordedAt, schema)
	if err != nil {
		return 0, 0, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	if changed == 0 {
		return 0, 0, nil
	}
	return 0, 1, applyPendingTombstone(db, r.UUID)
}

func importTombstone(db storer, line []byte) (int, int, error) {
	var t struct {
		UUID       string `json:"uuid"`
		RecordedAt string `json:"recordedAt"`
	}
	if err := json.Unmarshal(line, &t); err != nil {
		return 0, 0, err
	}
	if t.UUID == "" {
		return 0, 0, fmt.Errorf("tombstone without uuid")
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO tombstones(uuid, recorded_at, applied) VALUES(?,?,0)`,
		t.UUID, t.RecordedAt); err != nil {
		return 0, 0, err
	}
	res, err := db.Exec(`DELETE FROM samples WHERE uuid=?`, t.UUID)
	if err != nil {
		return 0, 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	if n > 0 {
		_, err = db.Exec(`UPDATE tombstones SET applied=1 WHERE uuid=?`, t.UUID)
		return 0, 0, err
	}
	return 0, 0, nil
}

// tombstoned reports whether a tombstone row names this uuid, marking it
// applied: the sample is deleted on the phone, so this import must not
// resurrect it.
func tombstoned(db storer, uuid string) (bool, error) {
	var applied int
	err := db.QueryRow(`SELECT applied FROM tombstones WHERE uuid=?`, uuid).Scan(&applied)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := db.Exec(`UPDATE tombstones SET applied=1 WHERE uuid=?`, uuid); err != nil {
		return false, err
	}
	return true, nil
}

// applyPendingTombstone deletes a just-arrived sample when its tombstone
// got here first. Months copy individually, so arrival order is
// meaningless and the tombstone table is the rendezvous. In practice the
// importLine pre-check skips tombstoned uuids before insert; this covers
// the residual path where the row already exists.
func applyPendingTombstone(db storer, uuid string) error {
	var applied int
	err := db.QueryRow(`SELECT applied FROM tombstones WHERE uuid=?`, uuid).Scan(&applied)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM samples WHERE uuid=?`, uuid); err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE tombstones SET applied=1 WHERE uuid=?`, uuid)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

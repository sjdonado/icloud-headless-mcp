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

// record mirrors one export envelope line. Pointers distinguish absent
// from zero; value stays raw until splitValue decides its shape.
type record struct {
	UUID          string          `json:"uuid"`
	Metric        string          `json:"metric"`
	RecordType    string          `json:"recordType"`
	Start         string          `json:"start"`
	End           string          `json:"end"`
	LocalDate     string          `json:"localDate"`
	Timezone      string          `json:"timezone"`
	Value         json.RawMessage `json:"value"`
	Unit          string          `json:"unit"`
	Source        string          `json:"source"`
	SourceBundle  string          `json:"sourceBundleId"`
	Device        string          `json:"device"`
	WasEntered    any             `json:"wasUserEntered"`
	RecordedAt    string          `json:"recordedAt"`
	SchemaVersion any             `json:"schemaVersion"`
}

// splitValue divides the polymorphic value without flattening it: a JSON
// number is a quantity amount; a JSON string or object is a category
// carrying a readable label, with a code when one is present. A null or
// absent value is unknown, never zero: parsing JSON null as a number
// succeeds and would silently mint 0-amount samples into every SUM.
func splitValue(raw json.RawMessage) (num *float64, code *int64, label string, typ string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil, "", "unknown"
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return &f, nil, "", "quantity"
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return nil, nil, strings.TrimSpace(s), "category"
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil, strings.TrimSpace(string(raw)), "unknown"
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
	return nil, code, label, "category"
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

// FileResult is one imported file's ledger row.
type FileResult struct {
	File   string
	Metric string
	Seen   int
	New    int
}

// ImportDir imports every monthly file under root/raw/<metric>/ plus
// root/_tombstones/, in sorted order, recording one ledger row per file.
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
	raw := filepath.Join(root, "raw")
	rawExists := false
	entries, err := os.ReadDir(raw)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		rawExists = true
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		month, err := os.ReadDir(filepath.Join(raw, e.Name()))
		if err != nil {
			return nil, err
		}
		for _, m := range month {
			if m.IsDir() || !strings.HasSuffix(m.Name(), ".jsonl") {
				continue
			}
			files = append(files, struct {
				path, metric string
				tomb         bool
			}{filepath.Join(raw, e.Name(), m.Name()), e.Name(), false})
		}
	}
	tombs, err := os.ReadDir(filepath.Join(root, "_tombstones"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	tombsExist := err == nil
	if !rawExists && !tombsExist {
		return nil, fmt.Errorf("export dir %s holds neither raw/ nor _tombstones/", root)
	}
	for _, m := range tombs {
		if m.IsDir() || !strings.HasSuffix(m.Name(), ".jsonl") {
			continue
		}
		files = append(files, struct {
			path, metric string
			tomb         bool
		}{filepath.Join(root, "_tombstones", m.Name()), "", true})
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
		seen, fresh, ferr := importFile(tx, f.path, f.metric, f.tomb)
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
		out = append(out, FileResult{File: rel, Metric: f.metric, Seen: seen, New: fresh})
	}
	return out, nil
}

func importFile(db storer, path, metric string, tombstones bool) (seen, fresh int, err error) {
	fh, err := os.Open(path)
	if err != nil {
		return 0, 0, err
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
		n, err := importLine(db, line, metric, tombstones)
		if err != nil {
			return seen, fresh, fmt.Errorf("line %d: %w", seen, err)
		}
		fresh += n
	}
	return seen, fresh, scan.Err()
}

func importLine(db storer, line []byte, metric string, tombstones bool) (int, error) {
	if tombstones {
		return importTombstone(db, line)
	}
	var r record
	if err := json.Unmarshal(line, &r); err != nil {
		return 0, err
	}
	if r.UUID == "" || r.Start == "" {
		return 0, fmt.Errorf("record without uuid or start")
	}
	if r.Metric == "" {
		r.Metric = metric
	}
	// A tombstoned uuid never materializes: skipping the insert keeps
	// re-imports at zero new rows with identical counts, whichever side
	// arrived first.
	if gone, err := tombstoned(db, r.UUID); err != nil {
		return 0, err
	} else if gone {
		return 0, nil
	}
	num, code, label, typ := splitValue(r.Value)
	var res sql.Result
	var err error
	res, err = db.Exec(`INSERT OR IGNORE INTO samples(uuid, metric, record_type, start_at, end_at, local_date, timezone, value_num, value_code, value_label, value_type, unit, source, source_bundle, device, user_entered, recorded_at, schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.UUID, r.Metric, r.RecordType, r.Start, r.End, r.LocalDate, r.Timezone,
		num, code, label, typ, r.Unit, r.Source, r.SourceBundle, r.Device,
		boolInt(asBool(r.WasEntered)), r.RecordedAt, asInt(r.SchemaVersion))
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		_, err = db.Exec(`UPDATE samples SET metric=?, record_type=?, start_at=?, end_at=?, local_date=?, timezone=?, value_num=?, value_code=?, value_label=?, value_type=?, unit=?, source=?, source_bundle=?, device=?, user_entered=?, recorded_at=?, schema_version=? WHERE uuid=?`,
			r.Metric, r.RecordType, r.Start, r.End, r.LocalDate, r.Timezone,
			num, code, label, typ, r.Unit, r.Source, r.SourceBundle, r.Device,
			boolInt(asBool(r.WasEntered)), r.RecordedAt, asInt(r.SchemaVersion), r.UUID)
		if err != nil {
			return 0, err
		}
		return 0, applyPendingTombstone(db, r.UUID)
	}
	if err := applyPendingTombstone(db, r.UUID); err != nil {
		return 1, err
	}
	return 1, nil
}

func importTombstone(db storer, line []byte) (int, error) {
	var t struct {
		UUID       string `json:"uuid"`
		RecordedAt string `json:"recordedAt"`
	}
	if err := json.Unmarshal(line, &t); err != nil {
		return 0, err
	}
	if t.UUID == "" {
		return 0, fmt.Errorf("tombstone without uuid")
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO tombstones(uuid, recorded_at, applied) VALUES(?,?,0)`,
		t.UUID, t.RecordedAt); err != nil {
		return 0, err
	}
	res, err := db.Exec(`DELETE FROM samples WHERE uuid=?`, t.UUID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		_, err = db.Exec(`UPDATE tombstones SET applied=1 WHERE uuid=?`, t.UUID)
		return 0, err
	}
	return 0, nil
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

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

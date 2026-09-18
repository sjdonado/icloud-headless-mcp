package health

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := OpenRW(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const stepLine = `{"uuid":"step-1","metric":"steps","recordType":"HKQuantityTypeIdentifierStepCount","start":"2026-09-01T08:00:00+02:00","end":"2026-09-01T08:01:00+02:00","localDate":"2026-09-01","timezone":"Europe/Amsterdam","value":120,"unit":"count","source":"test-source","sourceBundleId":"com.example.test","device":"iPhone","wasUserEntered":false,"recordedAt":"2026-09-01T08:02:00+02:00","schemaVersion":1}
`

const sleepLine = `{"uuid":"sleep-1","metric":"sleep","recordType":"HKCategoryTypeIdentifierSleepAnalysis","start":"2026-09-01T23:10:00+02:00","end":"2026-09-02T00:40:00+02:00","localDate":"2026-09-01","timezone":"Europe/Amsterdam","value":{"code":4,"label":"deep sleep"},"unit":"","source":"test-source","sourceBundleId":"com.example.test","device":"Watch","wasUserEntered":false,"recordedAt":"2026-09-02T06:00:00+02:00","schemaVersion":1}
`

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportQuantityAndCategory(t *testing.T) {
	db, _ := testDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "raw", "steps", "2026-09.jsonl"), stepLine)
	writeFile(t, filepath.Join(root, "raw", "sleep", "2026-09.jsonl"), sleepLine)
	res, err := ImportDir(db, root, time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].New != 1 || res[1].New != 1 {
		t.Fatalf("each file should report one new row: %+v", res)
	}
	var num float64
	if err := db.QueryRow(`SELECT value_num FROM samples WHERE uuid='step-1'`).Scan(&num); err != nil || num != 120 {
		t.Fatalf("quantity amount = %v, err %v, want 120", num, err)
	}
	var code int64
	var label, typ string
	if err := db.QueryRow(`SELECT value_code, value_label, value_type FROM samples WHERE uuid='sleep-1'`).Scan(&code, &label, &typ); err != nil {
		t.Fatal(err)
	}
	if code != 4 || label != "deep sleep" || typ != "category" {
		t.Fatalf("category split = (%d, %q, %q), want (4, deep sleep, category)", code, label, typ)
	}
}

func TestImportUnknownMetricAndReimport(t *testing.T) {
	db, _ := testDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "raw", "mindful-frobnicate", "2026-09.jsonl"),
		`{"uuid":"step-1","recordType":"HKQuantityTypeIdentifierStepCount","start":"2026-09-01T08:00:00+02:00","end":"2026-09-01T08:01:00+02:00","localDate":"2026-09-01","timezone":"Europe/Amsterdam","value":120,"unit":"count","source":"test-source","sourceBundleId":"com.example.test","device":"iPhone","wasUserEntered":false,"recordedAt":"2026-09-01T08:02:00+02:00","schemaVersion":1}
`)
	res, err := ImportDir(db, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].New != 1 {
		t.Fatalf("unknown metric should import, not reject: %+v", res)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM samples WHERE metric='mindful-frobnicate'`); got != 1 {
		t.Fatalf("unknown metric rows = %d, want 1", got)
	}
	res, err = ImportDir(db, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res[0].New != 0 {
		t.Fatalf("re-import should report zero new rows: %+v", res)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM samples`); got != 1 {
		t.Fatalf("total rows after re-import = %d, want 1", got)
	}
}

func TestTombstoneBeforeSample(t *testing.T) {
	db, _ := testDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "_tombstones", "2026-09.jsonl"),
		"{\"uuid\":\"step-9\",\"recordedAt\":\"2026-09-10T00:00:00+02:00\"}\n")
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM tombstones WHERE uuid='step-9' AND applied=0`); got != 1 {
		t.Fatal("early tombstone should wait stored and unapplied")
	}
	writeFile(t, filepath.Join(root, "raw", "steps", "2026-09.jsonl"),
		"{\"uuid\":\"step-9\",\"metric\":\"steps\",\"recordType\":\"x\",\"start\":\"2026-09-09T08:00:00+02:00\",\"end\":\"2026-09-09T08:01:00+02:00\",\"localDate\":\"2026-09-09\",\"timezone\":\"Europe/Amsterdam\",\"value\":50,\"unit\":\"count\",\"source\":\"s\",\"sourceBundleId\":\"b\",\"device\":\"d\",\"wasUserEntered\":false,\"recordedAt\":\"2026-09-09T08:02:00+02:00\",\"schemaVersion\":1}\n")
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM samples WHERE uuid='step-9'`); got != 0 {
		t.Fatal("sample arriving after its tombstone must not survive")
	}
	if got := count(t, db, `SELECT COUNT(*) FROM tombstones WHERE uuid='step-9' AND applied=1`); got != 1 {
		t.Fatal("tombstone should be marked applied")
	}
}

func TestRoundTripGeneratedMonth(t *testing.T) {
	db, _ := testDB(t)
	root := t.TempDir()
	var first, second strings.Builder
	// 500 step samples across September plus 60 sleep segments, two of
	// which the tombstone file deletes (one before its sample arrives).
	for i := 0; i < 500; i++ {
		day := 1 + i%30
		first.WriteString(`{"uuid":"g-step-` + itoa(i) + `","metric":"StepCount","recordType":"q","start":"2026-09-` + pad(day) + `T08:00:00+02:00","end":"2026-09-` + pad(day) + `T08:01:00+02:00","localDate":"2026-09-` + pad(day) + `","timezone":"Europe/Amsterdam","value":` + itoa(100+i) + `,"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-` + pad(day) + `T08:02:00+02:00","schemaVersion":1}` + "\n")
	}
	for i := 0; i < 60; i++ {
		day := 1 + i%30
		second.WriteString(`{"uuid":"g-sleep-` + itoa(i) + `","metric":"SleepAnalysis","recordType":"c","start":"2026-09-` + pad(day) + `T23:00:00+02:00","end":"2026-09-` + pad(day) + `T23:30:00+02:00","localDate":"2026-09-` + pad(day) + `","timezone":"Europe/Amsterdam","value":{"code":2,"label":"core sleep"},"unit":"","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-` + pad(day) + `T06:00:00+02:00","schemaVersion":1}` + "\n")
	}
	writeFile(t, filepath.Join(root, "raw", "StepCount", "2026-09.jsonl"), first.String())
	writeFile(t, filepath.Join(root, "raw", "SleepAnalysis", "2026-09.jsonl"), second.String())
	writeFile(t, filepath.Join(root, "_tombstones", "2026-09.jsonl"),
		"{\"uuid\":\"g-step-7\",\"recordedAt\":\"2026-09-10T00:00:00+02:00\"}\n"+
			"{\"uuid\":\"g-sleep-3\",\"recordedAt\":\"2026-09-10T00:00:00+02:00\"}\n")
	res, err := ImportDir(db, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fresh := 0
	for _, r := range res {
		fresh += r.New
	}
	// Tombstoned uuids never materialize, whichever side arrived first:
	// 560 rows minus the 2 deleted, all in one pass.
	if fresh != 558 {
		t.Fatalf("first import new = %d, want 558", fresh)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM samples`); got != 558 {
		t.Fatalf("stored rows = %d, want 558", got)
	}
	res, err = ImportDir(db, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.New != 0 {
			t.Fatalf("second import of %s reports %d new rows, want 0", r.File, r.New)
		}
	}
	if got := count(t, db, `SELECT COUNT(*) FROM samples`); got != 558 {
		t.Fatalf("stored rows after re-import = %d, want identical 558", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM tombstones WHERE applied=1`); got != 2 {
		t.Fatalf("applied tombstones = %d, want 2", got)
	}
}

func pad(n int) string {
	return fmt.Sprintf("%02d", n)
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

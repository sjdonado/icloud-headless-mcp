package health

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
)

func fixtureConfig(t *testing.T, dbPath string) *config.Config {
	t.Helper()
	return &config.Config{
		HealthDB: dbPath,
		AgentTZ:  "Europe/Amsterdam",
	}
}

// windowSince covers the fixed 2026-09 fixtures no matter when the suite
// runs: windows anchor at the owner's today, so ask for enough days.
func windowSince(t *testing.T, from string) int {
	t.Helper()
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		t.Fatal(err)
	}
	days := int(time.Since(start).Hours()/24) + 5
	if days < 30 {
		days = 30
	}
	return days
}

func populatedDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "health.sqlite")
	db, err := OpenRW(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "StepCount", "2026-09.jsonl"),
		`{"uuid":"s1","metric":"StepCount","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:30:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":5000,"type":"quantity"},"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:31:00+02:00","schemaVersion":1}
{"uuid":"s2","metric":"StepCount","recordType":"q","start":"2026-09-17T18:00:00+02:00","end":"2026-09-17T18:10:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":1000,"type":"quantity"},"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T18:11:00+02:00","schemaVersion":1}
`)
	writeFile(t, filepath.Join(root, "RestingHeartRate", "2026-09.jsonl"),
		`{"uuid":"r1","metric":"RestingHeartRate","recordType":"q","start":"2026-09-17T06:00:00+02:00","end":"2026-09-17T06:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":52,"type":"quantity"},"unit":"bpm","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T06:02:00+02:00","schemaVersion":1}
`)
	writeFile(t, filepath.Join(root, "SleepAnalysis", "2026-09.jsonl"),
		`{"uuid":"n1","metric":"SleepAnalysis","recordType":"c","start":"2026-09-16T23:00:00+02:00","end":"2026-09-16T23:50:00+02:00","localDate":"2026-09-16","timezone":"Europe/Amsterdam","value":{"code":4,"label":"deep sleep","type":"category"},"unit":"","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T06:00:00+02:00","schemaVersion":1}
{"uuid":"n2","metric":"SleepAnalysis","recordType":"c","start":"2026-09-16T23:50:00+02:00","end":"2026-09-17T01:00:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"code":2,"label":"core sleep","type":"category"},"unit":"","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T06:00:00+02:00","schemaVersion":1}
`)
	writeFile(t, filepath.Join(root, "HeartRate", "2026-09.jsonl"),
		`{"uuid":"h1","metric":"HeartRate","recordType":"q","start":"2026-09-17T07:00:00+02:00","end":"2026-09-17T07:20:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":130,"type":"quantity"},"unit":"bpm","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T07:21:00+02:00","schemaVersion":1}
{"uuid":"h2","metric":"HeartRate","recordType":"q","start":"2026-09-17T09:00:00+02:00","end":"2026-09-17T09:05:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":70,"type":"quantity"},"unit":"bpm","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T09:06:00+02:00","schemaVersion":1}
`)
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatusNamesBlindSpots(t *testing.T) {
	out := Status(fixtureConfig(t, populatedDB(t)))
	metrics, _ := out["metrics"].(map[string]any)
	if len(metrics) != 4 {
		t.Fatalf("metrics = %v, want 4 present", metrics)
	}
	var missing []string
	for _, m := range out["missing"].([]map[string]any) {
		r, _ := m["rollup"].(string)
		missing = append(missing, r)
		if m["reason"] == nil || m["reason"] == "" {
			t.Fatalf("missing entry carries no reason: %v", m)
		}
		if hints, _ := m["folder_hints"].([]string); len(hints) == 0 {
			t.Fatalf("missing entry names no folder hints: %v", m)
		}
	}
	for _, want := range []string{"energy", "hrv"} {
		found := false
		for _, m := range missing {
			if m == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing = %v, want %q named", missing, want)
		}
	}
	if out["unapplied_tombstones"] != 0 {
		t.Fatalf("unapplied = %v, want 0", out["unapplied_tombstones"])
	}
	if out["last_import"] == nil {
		t.Fatal("last_import should describe the fixture run")
	}
}

func TestDaysRollupAndNulls(t *testing.T) {
	out := Days(fixtureConfig(t, populatedDB(t)), windowSince(t, "2026-09-01"))
	days, _ := out["days"].([]map[string]any)
	var got map[string]any
	for _, d := range days {
		if d["date"] == "2026-09-17" {
			got = d
		}
	}
	if got == nil {
		t.Fatal("2026-09-17 missing from window")
	}
	if got["steps"] != 6000.0 {
		t.Fatalf("steps = %v, want 6000", got["steps"])
	}
	if got["resting_bpm"] != 52.0 {
		t.Fatalf("resting_bpm = %v, want 52", got["resting_bpm"])
	}
	if got["hrv"] != nil {
		t.Fatalf("hrv = %v, want null (never imported)", got["hrv"])
	}
	if _, hasErr := got["hrv_error"]; hasErr {
		t.Fatalf("absent metric should be null without error, got %v", got)
	}
}

func TestSleepKeepsStages(t *testing.T) {
	out := Sleep(fixtureConfig(t, populatedDB(t)), windowSince(t, "2026-09-01"))
	nights, _ := out["nights"].([]map[string]any)
	if len(nights) != 1 {
		t.Fatalf("nights = %v, want exactly one episode", nights)
	}
	stages, _ := nights[0]["stages"].(map[string]any)
	if stages["deep sleep"] != 50.0 || stages["core sleep"] != 70.0 {
		t.Fatalf("stages = %v, want deep 50 + core 70", stages)
	}
	if nights[0]["night"] != "2026-09-16" {
		t.Fatalf("night = %v, want onset date 2026-09-16", nights[0]["night"])
	}
}

func TestEffortFloor(t *testing.T) {
	out := Effort(fixtureConfig(t, populatedDB(t)), windowSince(t, "2026-09-01"), 100)
	days, _ := out["days"].([]map[string]any)
	var got map[string]any
	for _, d := range days {
		if d["date"] == "2026-09-17" {
			got = d
		}
	}
	if got["minutes_above_floor"] != 20.0 {
		t.Fatalf("minutes = %v, want 20 (130bpm x 20min; 70bpm excluded)", got["minutes_above_floor"])
	}
}

func TestRecoveryThinHistory(t *testing.T) {
	out := Recovery(fixtureConfig(t, populatedDB(t)), 7, 28)
	rhr, _ := out["resting_bpm"].(map[string]any)
	if rhr["recent"] != nil || rhr["delta"] != nil {
		t.Fatalf("one covered day should be null, got %v", rhr)
	}
	if _, hasErr := rhr["recent_error"]; !hasErr {
		t.Fatalf("thin history should carry a reason, got %v", rhr)
	}
}

func TestSelectGate(t *testing.T) {
	cfg := fixtureConfig(t, populatedDB(t))
	if _, err := SQL(cfg, "SELECT COUNT(*) AS n FROM samples"); err != nil {
		t.Fatalf("plain SELECT refused: %v", err)
	}
	for _, bad := range []string{
		"INSERT INTO samples(uuid) VALUES('x')",
		"UPDATE samples SET metric='x'",
		"DELETE FROM samples",
		"DROP TABLE samples",
		"SELECT 1; DELETE FROM samples",
		"WITH x AS (SELECT 1) SELECT * FROM x",
		"PRAGMA journal_mode=DELETE",
		"",
		"   ",
	} {
		if _, err := SQL(cfg, bad); err == nil {
			t.Fatalf("%q should be refused", bad)
		}
	}
	out, err := SQL(cfg, "/* leading comment */ SELECT COUNT(*) AS n FROM samples;")
	if err != nil {
		t.Fatalf("comment-prefixed SELECT refused: %v", err)
	}
	rows, _ := out["rows"].([]map[string]any)
	if len(rows) != 1 || rows[0]["n"] != int64(7) {
		t.Fatalf("count query = %v, want one row n=7", rows)
	}
}

func TestEmptyStoreSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	db, err := OpenRW(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	cfg := fixtureConfig(t, path)
	if out := Status(cfg); out["metrics_present_count"] != 0 {
		t.Fatalf("empty status = %v, want zero metrics", out)
	}
	if out := Days(cfg, 7); out["days"] == nil {
		t.Fatal("empty days should return dated nulls, not nil")
	} else if days := out["days"].([]map[string]any); len(days) != 7 || days[0]["steps"] != nil {
		t.Fatalf("empty days = %v, want 7 dated null rows", out)
	}
	if out := Sleep(cfg, 7); len(out["nights"].([]map[string]any)) != 0 {
		t.Fatalf("empty sleep should be empty nights with note, got %v", out)
	}
}

func TestUnconfiguredTools(t *testing.T) {
	cfg := &config.Config{AgentTZ: "Europe/Amsterdam"}
	for name, out := range map[string]any{
		"status": Status(cfg), "days": Days(cfg, 7), "sleep": Sleep(cfg, 7),
		"effort": Effort(cfg, 7, 100), "recovery": Recovery(cfg, 7, 28),
	} {
		m, _ := out.(map[string]any)
		if m["unconfigured"] != true {
			t.Fatalf("%s should report unconfigured, got %v", name, out)
		}
	}
	if out, err := SQL(cfg, "SELECT 1"); err != nil || out["unconfigured"] != true {
		t.Fatalf("unconfigured SQL should report, not invent a database: %v %v", out, err)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestSplitValueNullAndString(t *testing.T) {
	if num, _, _, typ, err := splitValue([]byte("null")); err != nil || num != nil || typ != "unknown" {
		t.Fatalf("null should be unknown, got %v %q err %v", num, typ, err)
	}
	if num, _, label, typ, err := splitValue([]byte(`"light sleep"`)); err != nil || num != nil || label != "light sleep" || typ != "category" {
		t.Fatalf("string value = (%v,%q,%q), want (nil,light sleep,category)", num, label, typ)
	}
}

func TestSplitHistorySumsTogether(t *testing.T) {
	db, dbPath := testDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "StepCount", "2026-09.jsonl"),
		`{"uuid":"a1","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":100,"type":"quantity"},"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:02:00+02:00","schemaVersion":1}
`)
	writeFile(t, filepath.Join(root, "steps", "2026-09.jsonl"),
		`{"uuid":"a2","recordType":"q","start":"2026-09-17T09:00:00+02:00","end":"2026-09-17T09:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":200,"type":"quantity"},"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T09:02:00+02:00","schemaVersion":1}
`)
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out := Days(fixtureConfig(t, dbPath), windowSince(t, "2026-09-01"))
	for _, d := range out["days"].([]map[string]any) {
		if d["date"] == "2026-09-17" && d["steps"] != 300.0 {
			t.Fatalf("split history steps = %v, want 300", d["steps"])
		}
	}
}

func TestSleepKeysByOnsetLocalDate(t *testing.T) {
	db, dbPath := testDB(t)
	root := t.TempDir()
	// Timestamps in +01:00 whose onset Format would read 2026-09-16 while
	// the exporter stamped localDate 2026-09-17: the stamp wins.
	writeFile(t, filepath.Join(root, "SleepAnalysis", "2026-09.jsonl"),
		`{"uuid":"m1","metric":"SleepAnalysis","recordType":"c","start":"2026-09-16T23:30:00+01:00","end":"2026-09-17T00:30:00+01:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"code":2,"label":"core sleep","type":"category"},"unit":"","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T06:00:00+02:00","schemaVersion":1}
`)
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out := Sleep(fixtureConfig(t, dbPath), windowSince(t, "2026-09-01"))
	nights := out["nights"].([]map[string]any)
	if len(nights) != 1 || nights[0]["night"] != "2026-09-17" {
		t.Fatalf("night should key by onset localDate 2026-09-17, got %v", nights)
	}
}

func TestEffortNullDayAndSkips(t *testing.T) {
	db, dbPath := testDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "HeartRate", "2026-09.jsonl"),
		`{"uuid":"e1","metric":"HeartRate","recordType":"q","start":"2026-09-17T07:00:00+02:00","end":"2026-09-17T07:10:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":150,"type":"quantity"},"unit":"bpm","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T07:11:00+02:00","schemaVersion":1}
{"uuid":"e2","metric":"HeartRate","recordType":"q","start":"not-a-time","end":"2026-09-17T08:10:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":150,"type":"quantity"},"unit":"bpm","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:11:00+02:00","schemaVersion":1}
`)
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out := Effort(fixtureConfig(t, dbPath), windowSince(t, "2026-09-01"), 100)
	var d17, other map[string]any
	for _, d := range out["days"].([]map[string]any) {
		if d["date"] == "2026-09-17" {
			d17 = d
		} else if other == nil && d["minutes_above_floor"] == nil {
			other = d
		}
	}
	if d17["minutes_above_floor"] != 10.0 || d17["skipped_samples"] != 1 {
		t.Fatalf("2026-09-17 = %v, want 10 minutes and 1 skip", d17)
	}
	if other == nil {
		t.Fatal("a day with no samples should read null, found none")
	}
}

func TestWindowValidation(t *testing.T) {
	args := func(v any) mcpserver.Args {
		return mcpserver.Args{"days": v}
	}
	if _, err := window(args(0), "days", 14); err == nil {
		t.Fatal("zero days should fail")
	}
	if _, err := window(args(-5), "days", 14); err == nil {
		t.Fatal("negative days should fail")
	}
	if _, err := window(args(367), "days", 14); err == nil {
		t.Fatal("huge days should fail")
	}
	if n, err := window(args(30), "days", 14); err != nil || n != 30 {
		t.Fatalf("30 days = %d err %v, want 30 nil", n, err)
	}
}

func TestEmptyLayoutErrors(t *testing.T) {
	db, _ := testDB(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "unrelated"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportDir(db, root, time.Now()); err == nil {
		t.Fatal("dir with neither raw/ nor _tombstones/ should error, not succeed empty")
	}
}

func TestSelectGateSemicolons(t *testing.T) {
	cfg := fixtureConfig(t, populatedDB(t))
	if _, err := SQL(cfg, "SELECT COUNT(*) AS n FROM samples;"); err != nil {
		t.Fatalf("one trailing semicolon should pass: %v", err)
	}
	if _, err := SQL(cfg, "SELECT ';'"); err == nil {
		t.Fatal("semicolon inside a literal should be refused (lexical gate)")
	}
	if _, err := SQL(cfg, "SELECT 1;;"); err == nil {
		t.Fatal("double semicolon should be refused")
	}
}

func TestStatusStalenessVerdict(t *testing.T) {
	out := Status(fixtureConfig(t, populatedDB(t)))
	if out["newest_sample_date"] != "2026-09-17" {
		t.Fatalf("newest_sample_date = %v, want 2026-09-17", out["newest_sample_date"])
	}
	today, err := ownerToday(fixtureConfig(t, populatedDB(t)))
	if err != nil {
		t.Skip("no owner zone in test env")
	}
	wantDays, err := daysBetween("2026-09-17", today)
	if err != nil {
		t.Fatal(err)
	}
	if out["days_since_newest"] != wantDays {
		t.Fatalf("days_since_newest = %v, want %d", out["days_since_newest"], wantDays)
	}
	if out["stale"] != (wantDays > 1) {
		t.Fatalf("stale = %v for %d days since newest, want %v", out["stale"], wantDays, wantDays > 1)
	}
	if last, _ := out["last_sample_import"].(map[string]any); last == nil || last["file"] == nil {
		t.Fatalf("last_sample_import should name a sample file, got %v", out["last_sample_import"])
	} else if f, _ := last["file"].(string); len(f) >= 12 && f[:12] == "_tombstones/" {
		t.Fatalf("last_sample_import must never be a tombstone file, got %v", f)
	}
	// A tombstone file imported after every sample: last_import names it
	// (literally true), last_sample_import must still name samples.
	dbPath := filepath.Join(t.TempDir(), "stale.sqlite")
	db, err := OpenRW(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Zebra", "2026-09.jsonl"),
		`{"uuid":"z1","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":5,"type":"quantity"},"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:02:00+02:00","schemaVersion":1}
`)
	writeFile(t, filepath.Join(root, "_tombstones", "2026-09.jsonl"),
		"{\"uuid\":\"never-arrives\",\"recordedAt\":\"2026-09-18T00:00:00+02:00\"}\n")
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out2 := Status(fixtureConfig(t, dbPath))
	lastFile, _ := out2["last_import"].(map[string]any)["file"].(string)
	sampleFile, _ := out2["last_sample_import"].(map[string]any)["file"].(string)
	if !strings.HasPrefix(lastFile, "_tombstones/") {
		t.Fatalf("last_import should name the tombstone file here, got %v", lastFile)
	}
	if strings.HasPrefix(sampleFile, "_tombstones/") || !strings.Contains(sampleFile, "Zebra") {
		t.Fatalf("last_sample_import should name the sample file, got %v", sampleFile)
	}
}

func TestSplitValueTypedObjects(t *testing.T) {
	num, _, _, typ, err := splitValue([]byte(`{"amount":58,"type":"quantity"}`))
	if err != nil || typ != "quantity" || num == nil || *num != 58 {
		t.Fatalf("typed quantity = (%v,%q) err %v, want (58,quantity) nil", num, typ, err)
	}
	_, code, label, typ, err := splitValue([]byte(`{"code":3,"label":"asleepCore","type":"category"}`))
	if err != nil || typ != "category" || code == nil || *code != 3 || label != "asleepCore" {
		t.Fatalf("typed category = (%v,%q,%q) err %v", code, label, typ, err)
	}
	if _, _, _, _, err := splitValue([]byte(`{"amount":1,"type":"mystery"}`)); err == nil {
		t.Fatal("unknown value type should fail the row, not store it")
	}
	if _, _, _, _, err := splitValue([]byte(`{"type":"quantity"}`)); err == nil {
		t.Fatal("quantity without amount should fail the row, not store a vanished number")
	}
}

func TestDoubleImportPreservesValues(t *testing.T) {
	db, _ := testDB(t)
	root := t.TempDir()
	line := `{"uuid":"d1","metric":"StepCount","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":999,"type":"quantity"},"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:02:00+02:00","schemaVersion":1}
`
	writeFile(t, filepath.Join(root, "StepCount", "2026-09.jsonl"), line)
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := ImportDir(db, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.New != 0 || r.Updated != 0 {
			t.Fatalf("identical re-import of %s reports new=%d updated=%d, want 0 0", r.File, r.New, r.Updated)
		}
	}
	var got float64
	var typ string
	if err := db.QueryRow(`SELECT value_num, value_type FROM samples WHERE uuid='d1'`).Scan(&got, &typ); err != nil || got != 999 || typ != "quantity" {
		t.Fatalf("stored row after double import = (%v,%q) err %v, want (999,quantity)", got, typ, err)
	}
}

func TestChangedReimportCountsUpdated(t *testing.T) {
	db, _ := testDB(t)
	root := t.TempDir()
	p := filepath.Join(root, "StepCount", "2026-09.jsonl")
	writeFile(t, p, `{"uuid":"c9","metric":"StepCount","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":100,"type":"quantity"},"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:02:00+02:00","schemaVersion":1}
`)
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, `{"uuid":"c9","metric":"StepCount","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":200,"type":"quantity"},"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:02:00+02:00","schemaVersion":1}
`)
	res, err := ImportDir(db, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].New != 0 || res[0].Updated != 1 {
		t.Fatalf("changed re-import = %+v, want 0 new 1 updated", res)
	}
	var got float64
	if err := db.QueryRow(`SELECT value_num FROM samples WHERE uuid='c9'`).Scan(&got); err != nil || got != 200 {
		t.Fatalf("refreshed value = %v err %v, want 200", got, err)
	}
}

func TestWorkoutStoresWholeObject(t *testing.T) {
	db, _ := testDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "workout", "2026-09.jsonl"),
		`{"uuid":"w1","metric":"workout","recordType":"HKWorkoutType","start":"2026-09-17T07:00:00+02:00","end":"2026-09-17T07:36:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"duration_s":2163,"totalDistance_m":2269.98,"totalEnergy_kcal":101.46,"type":"workout","workoutType":"walking"},"unit":"","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T07:37:00+02:00","schemaVersion":1}
`)
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	var typ, label string
	var num sql.NullFloat64
	var code sql.NullInt64
	if err := db.QueryRow(`SELECT value_num, value_code, value_label, value_type FROM samples WHERE uuid='w1'`).Scan(&num, &code, &label, &typ); err != nil {
		t.Fatal(err)
	}
	if typ != "workout" || num.Valid || code.Valid {
		t.Fatalf("workout row = (%v,%v,%q,%q), want NULL numbers and workout type", num, code, label, typ)
	}
	for _, want := range []string{"2163", "walking"} {
		if !strings.Contains(label, want) {
			t.Fatalf("workout label %q should keep %q", label, want)
		}
	}
}

func TestAbsentStringsStoreNull(t *testing.T) {
	db, _ := testDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "StepCount", "2026-09.jsonl"),
		`{"uuid":"n1","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":5,"type":"quantity"},"source":"s","sourceBundleId":"b","wasUserEntered":false,"schemaVersion":1}
`)
	if _, err := ImportDir(db, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	var unit, device, recorded sql.NullString
	var label sql.NullString
	if err := db.QueryRow(`SELECT unit, device, recorded_at, value_label FROM samples WHERE uuid='n1'`).Scan(&unit, &device, &recorded, &label); err != nil {
		t.Fatal(err)
	}
	if unit.Valid || device.Valid || recorded.Valid || label.Valid {
		t.Fatalf("absent fields should be NULL, got %v %v %v %v", unit, device, recorded, label)
	}
}

func TestFailedRunReturnsProgress(t *testing.T) {
	db, _ := testDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "aaa", "2026-09.jsonl"),
		`{"uuid":"g1","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":5,"type":"quantity"},"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:02:00+02:00","schemaVersion":1}
`)
	writeFile(t, filepath.Join(root, "zzz", "2026-09.jsonl"),
		`{"uuid":"b1","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"amount":1,"type":"mystery"},"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:02:00+02:00","schemaVersion":1}
`)
	res, err := ImportDir(db, root, time.Now())
	if err == nil {
		t.Fatal("unknown value type should fail the run")
	}
	if len(res) != 1 || res[0].New != 1 {
		t.Fatalf("failed run should still return the committed file, got %+v", res)
	}
}

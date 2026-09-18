package health

import (
	"os"
	"path/filepath"
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
	writeFile(t, filepath.Join(root, "raw", "StepCount", "2026-09.jsonl"),
		`{"uuid":"s1","metric":"StepCount","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:30:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":5000,"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:31:00+02:00","schemaVersion":1}
{"uuid":"s2","metric":"StepCount","recordType":"q","start":"2026-09-17T18:00:00+02:00","end":"2026-09-17T18:10:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":1000,"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T18:11:00+02:00","schemaVersion":1}
`)
	writeFile(t, filepath.Join(root, "raw", "RestingHeartRate", "2026-09.jsonl"),
		`{"uuid":"r1","metric":"RestingHeartRate","recordType":"q","start":"2026-09-17T06:00:00+02:00","end":"2026-09-17T06:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":52,"unit":"bpm","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T06:02:00+02:00","schemaVersion":1}
`)
	writeFile(t, filepath.Join(root, "raw", "SleepAnalysis", "2026-09.jsonl"),
		`{"uuid":"n1","metric":"SleepAnalysis","recordType":"c","start":"2026-09-16T23:00:00+02:00","end":"2026-09-16T23:50:00+02:00","localDate":"2026-09-16","timezone":"Europe/Amsterdam","value":{"code":4,"label":"deep sleep"},"unit":"","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T06:00:00+02:00","schemaVersion":1}
{"uuid":"n2","metric":"SleepAnalysis","recordType":"c","start":"2026-09-16T23:50:00+02:00","end":"2026-09-17T01:00:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"code":2,"label":"core sleep"},"unit":"","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T06:00:00+02:00","schemaVersion":1}
`)
	writeFile(t, filepath.Join(root, "raw", "HeartRate", "2026-09.jsonl"),
		`{"uuid":"h1","metric":"HeartRate","recordType":"q","start":"2026-09-17T07:00:00+02:00","end":"2026-09-17T07:20:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":130,"unit":"bpm","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T07:21:00+02:00","schemaVersion":1}
{"uuid":"h2","metric":"HeartRate","recordType":"q","start":"2026-09-17T09:00:00+02:00","end":"2026-09-17T09:05:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":70,"unit":"bpm","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T09:06:00+02:00","schemaVersion":1}
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
		missing = append(missing, m["metric"].(string))
		if m["reason"] == nil || m["reason"] == "" {
			t.Fatalf("missing entry carries no reason: %v", m)
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
	if num, _, _, typ := splitValue([]byte("null")); num != nil || typ != "unknown" {
		t.Fatalf("null should be unknown, got %v %q", num, typ)
	}
	if num, _, label, typ := splitValue([]byte(`"light sleep"`)); num != nil || label != "light sleep" || typ != "category" {
		t.Fatalf("string value = (%v,%q,%q), want (nil,light sleep,category)", num, label, typ)
	}
}

func TestSplitHistorySumsTogether(t *testing.T) {
	db, dbPath := testDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "raw", "StepCount", "2026-09.jsonl"),
		`{"uuid":"a1","recordType":"q","start":"2026-09-17T08:00:00+02:00","end":"2026-09-17T08:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":100,"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:02:00+02:00","schemaVersion":1}
`)
	writeFile(t, filepath.Join(root, "raw", "steps", "2026-09.jsonl"),
		`{"uuid":"a2","recordType":"q","start":"2026-09-17T09:00:00+02:00","end":"2026-09-17T09:01:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":200,"unit":"count","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T09:02:00+02:00","schemaVersion":1}
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
	writeFile(t, filepath.Join(root, "raw", "SleepAnalysis", "2026-09.jsonl"),
		`{"uuid":"m1","metric":"SleepAnalysis","recordType":"c","start":"2026-09-16T23:30:00+01:00","end":"2026-09-17T00:30:00+01:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":{"code":2,"label":"core sleep"},"unit":"","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T06:00:00+02:00","schemaVersion":1}
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
	writeFile(t, filepath.Join(root, "raw", "HeartRate", "2026-09.jsonl"),
		`{"uuid":"e1","metric":"HeartRate","recordType":"q","start":"2026-09-17T07:00:00+02:00","end":"2026-09-17T07:10:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":150,"unit":"bpm","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T07:11:00+02:00","schemaVersion":1}
{"uuid":"e2","metric":"HeartRate","recordType":"q","start":"not-a-time","end":"2026-09-17T08:10:00+02:00","localDate":"2026-09-17","timezone":"Europe/Amsterdam","value":150,"unit":"bpm","source":"s","sourceBundleId":"b","device":"d","wasUserEntered":false,"recordedAt":"2026-09-17T08:11:00+02:00","schemaVersion":1}
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

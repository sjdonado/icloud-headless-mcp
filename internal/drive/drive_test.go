package drive

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
)

var fixedNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	return &config.Config{
		DriveStaging: filepath.Join(root, "staging"),
		DriveEtags:   filepath.Join(root, "etags.json"),
		DriveLibs:    `[{"name": "Example App Exports", "kind": "folder", "dest": "example-app"}]`,
	}
}

func TestStatusFreshRun(t *testing.T) {
	cfg := testConfig(t)
	staged := filepath.Join(cfg.DriveStaging, "example-app")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	// Emptied by the sync: zero staged is the healthy answer.
	if err := os.WriteFile(cfg.DriveEtags, []byte(`{"abc": "e1", "def": "e2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	past := fixedNow.Add(-2 * time.Hour)
	if err := os.Chtimes(cfg.DriveEtags, past, past); err != nil {
		t.Fatal(err)
	}
	out, err := Status(cfg, fixedNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if out["stale"] != false {
		t.Fatalf("stale = %v", out["stale"])
	}
	if out["files_tracked"] != 2 {
		t.Fatalf("files_tracked = %v", out["files_tracked"])
	}
	libs, _ := out["libraries"].(map[string]any)
	lib, _ := libs["Example App Exports"].(map[string]any)
	if lib["staged_now"] != 0 || lib["kind"] != "folder" || lib["staged_under"] != "example-app" {
		t.Fatalf("library = %v", lib)
	}
	lastPull, _ := out["last_pull"].(map[string]any)
	if lastPull["hours_ago"] != 2.0 {
		t.Fatalf("last_pull = %v", lastPull)
	}
}

func TestStatusStaleRun(t *testing.T) {
	cfg := testConfig(t)
	if err := os.WriteFile(cfg.DriveEtags, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	past := fixedNow.Add(-30 * time.Hour)
	if err := os.Chtimes(cfg.DriveEtags, past, past); err != nil {
		t.Fatal(err)
	}
	out, err := Status(cfg, fixedNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if out["stale"] != true {
		t.Fatalf("stale = %v", out["stale"])
	}
}

func TestStatusNeverRan(t *testing.T) {
	cfg := testConfig(t)
	out, err := Status(cfg, fixedNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if out["stale"] != nil {
		t.Fatalf("stale = %v, want nil without a run", out["stale"])
	}
	if out["files_tracked"] != 0 {
		t.Fatalf("files_tracked = %v", out["files_tracked"])
	}
}

func TestStatusUnparseableEtags(t *testing.T) {
	cfg := testConfig(t)
	if err := os.WriteFile(cfg.DriveEtags, []byte(`{oops`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := Status(cfg, fixedNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	msg, _ := out["error"].(string)
	if msg == "" {
		t.Fatalf("result = %v, want the unparseable-file error", out)
	}
}

func TestStatusStagedFilesCounted(t *testing.T) {
	cfg := testConfig(t)
	staged := filepath.Join(cfg.DriveStaging, "example-app", "reports")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "a.csv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.DriveEtags, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := Status(cfg, fixedNow)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	libs, _ := out["libraries"].(map[string]any)
	lib, _ := libs["Example App Exports"].(map[string]any)
	if lib["staged_now"] != 1 {
		t.Fatalf("staged_now = %v", lib["staged_now"])
	}
	if newest, _ := lib["newest_staged_file"].(map[string]any); newest["at"] == nil {
		t.Fatalf("newest_staged_file = %v", newest)
	}
}

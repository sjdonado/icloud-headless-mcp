package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			t.Setenv(k, v)
		}
		os.Unsetenv(k)
	}
}

func TestLoadRequiresCredentials(t *testing.T) {
	clearEnv(t, "ICLOUD_APPLE_ID", "ICLOUD_APP_PASSWORD", "AGENT_TZ")
	t.Setenv("TZ", "UTC") // a UTC host must name the owner's zone
	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded without credentials")
	} else if got := err.Error(); !contains(got, "ICLOUD_APPLE_ID") {
		t.Fatalf("error %q does not name the missing key", got)
	}
	setEnv(t, "ICLOUD_APPLE_ID", "you@icloud.com")
	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded without app password")
	} else if got := err.Error(); !contains(got, "ICLOUD_APP_PASSWORD") {
		t.Fatalf("error %q does not name the missing key", got)
	}
	setEnv(t, "ICLOUD_APP_PASSWORD", "xxxx")
	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded without AGENT_TZ")
	} else if got := err.Error(); !contains(got, "AGENT_TZ") {
		t.Fatalf("error %q does not name the missing key", got)
	}
}

func TestLoadDefaults(t *testing.T) {
	setEnv(t, "ICLOUD_APPLE_ID", "you@icloud.com")
	setEnv(t, "ICLOUD_APP_PASSWORD", "xxxx")
	setEnv(t, "AGENT_TZ", "Europe/Amsterdam")
	clearEnv(t, "AGENT_DEFAULT_CALENDAR", "ICLOUD_CDP", "DRIVE_LIBRARIES", "ICLOUD_STATE", "ICLOUD_SHARED_STATE",
		"MAIL_ATTACHMENTS_DIR", "DRIVE_STAGING", "DRIVE_ETAGS", "AGENT_TZ_FILE")
	t.Setenv("HOME", "/tmp/fakehome")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := map[string]string{
		"CDP":            c.CDP,
		"DriveLibs":      c.DriveLibs,
		"TZFile":         c.TZFile,
		"StateDir":       c.StateDir,
		"SharedState":    c.SharedState,
		"AttachmentsDir": c.AttachmentsDir,
		"DriveStaging":   c.DriveStaging,
		"DriveEtags":     c.DriveEtags,
	}
	want := map[string]string{
		"CDP":            "http://127.0.0.1:9222",
		"DriveLibs":      "[]",
		"TZFile":         "/etc/agent/timezone",
		"StateDir":       "/tmp/fakehome/.icloud-mcp",
		"SharedState":    "/tmp/fakehome/.icloud-mcp/shared",
		"AttachmentsDir": "/tmp/fakehome/.icloud-mcp/mail-attachments",
		"DriveStaging":   "/tmp/fakehome/.icloud-mcp/drive-staging",
		"DriveEtags":     "/tmp/fakehome/.icloud-mcp/state/drive-etags.json",
	}
	for k, w := range want {
		if cases[k] != w {
			t.Errorf("%s = %q, want %q", k, cases[k], w)
		}
	}
	if c.DefaultCal != "" {
		t.Errorf("DefaultCal = %q, want empty", c.DefaultCal)
	}
}

func TestLocalTimezoneFileWins(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "timezone")
	if err := os.WriteFile(f, []byte("America/Bogota\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Config{TZFile: f, AgentTZ: "Europe/Amsterdam"}
	got, err := c.LocalTimezone()
	if err != nil || got != "America/Bogota" {
		t.Fatalf("LocalTimezone = %q, %v; want America/Bogota", got, err)
	}
}

func TestLocalTimezoneBlankFileFallsThrough(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "timezone")
	if err := os.WriteFile(f, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Config{TZFile: f, AgentTZ: "Europe/Amsterdam"}
	got, err := c.LocalTimezone()
	if err != nil || got != "Europe/Amsterdam" {
		t.Fatalf("LocalTimezone = %q, %v; want Europe/Amsterdam", got, err)
	}
}

func TestLocalTimezoneMissingFileUsesEnv(t *testing.T) {
	c := &Config{TZFile: filepath.Join(t.TempDir(), "absent"), AgentTZ: "Europe/Amsterdam"}
	got, err := c.LocalTimezone()
	if err != nil || got != "Europe/Amsterdam" {
		t.Fatalf("LocalTimezone = %q, %v; want Europe/Amsterdam", got, err)
	}
}

func TestLocalTimezoneNeitherRaises(t *testing.T) {
	c := &Config{TZFile: filepath.Join(t.TempDir(), "absent")}
	if _, err := c.LocalTimezone(); err == nil {
		t.Fatal("LocalTimezone succeeded with no file and no AGENT_TZ")
	} else if got := err.Error(); !contains(got, "AGENT_TZ") {
		t.Fatalf("error %q does not mention AGENT_TZ", got)
	}
}

func TestLocalTimezoneParsesAsIANAZone(t *testing.T) {
	c := &Config{TZFile: filepath.Join(t.TempDir(), "absent"), AgentTZ: "Europe/Amsterdam"}
	name, err := c.LocalTimezone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.LoadLocation(name); err != nil {
		t.Fatalf("zone %q does not load: %v", name, err)
	}
}

func TestHostZone(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "localtime")
	old := localtime
	localtime = link
	defer func() { localtime = old }()
	t.Setenv("TZ", "")
	for target, want := range map[string]string{
		"/var/db/timezone/zoneinfo/Europe/Berlin": "Europe/Berlin",  // macOS
		"/usr/share/zoneinfo/America/Bogota":      "America/Bogota", // Linux
		"/usr/share/zoneinfo/Etc/UTC":             "",               // VPS: must be named
		"/nowhere":                                "",
	} {
		_ = os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if got := hostZone(); got != want {
			t.Errorf("link %s: hostZone = %q, want %q", target, got, want)
		}
	}
	t.Setenv("TZ", ":Asia/Tokyo")
	if got := hostZone(); got != "Asia/Tokyo" {
		t.Errorf("TZ wins: got %q", got)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

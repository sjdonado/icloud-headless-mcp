// Package config is the account's environment contract: every key the
// server reads, its default, and the fail-fast rule, so a missing
// required key names the key instead of failing later.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds every environment input. Field comments name the source key
// and default; "(required)" fields refuse to load when unset.
type Config struct {
	AppleID        string // ICLOUD_APPLE_ID (required)
	AppPassword    string // ICLOUD_APP_PASSWORD (required)
	AgentTZ        string // AGENT_TZ, default the host's zone unless that is UTC (see Load)
	TZFile         string // AGENT_TZ_FILE, default /etc/agent/timezone
	DefaultCal     string // AGENT_DEFAULT_CALENDAR, default ""
	StateDir       string // ICLOUD_STATE, default $HOME/.icloud-mcp
	SharedState    string // ICLOUD_SHARED_STATE, default $ICLOUD_STATE/shared
	CDP            string // ICLOUD_CDP, default http://127.0.0.1:9222
	AttachmentsDir string // MAIL_ATTACHMENTS_DIR, default $ICLOUD_STATE/mail-attachments
	DriveStaging   string // DRIVE_STAGING, default $ICLOUD_STATE/drive-staging
	DriveEtags     string // DRIVE_ETAGS, default $ICLOUD_STATE/state/drive-etags.json
	DriveLibs      string // DRIVE_LIBRARIES, default "[]"
	HealthDB       string // HEALTH_DB, default $ICLOUD_STATE/state/health.sqlite. Owned exclusively by this service account: the extension's importer is its sole writer, so never point it at another uid's live store; adopting one means copying it into place (operator, manual migrate).
	HealthExport   string // HEALTH_EXPORT_DIR, default "" (extension off)
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// Load reads the environment once. A missing required key is an error naming
// the key at startup instead of failing on first use.
func Load() (*Config, error) {
	c := LoadEnv()
	if c.AppleID == "" {
		return nil, fmt.Errorf("missing required environment: ICLOUD_APPLE_ID")
	}
	if c.AppPassword == "" {
		return nil, fmt.Errorf("missing required environment: ICLOUD_APP_PASSWORD")
	}
	// A laptop's own zone is its owner's zone, so it is the default. A
	// server's usually is not: a host on UTC (the VPS case) or one whose
	// zone cannot be read must name the owner's zone, because a zone
	// guessed here types the wrong hour into an Apple picker and nothing
	// errors.
	if c.AgentTZ == "" {
		return nil, fmt.Errorf("missing required environment: AGENT_TZ (the host's zone " +
			"is UTC or unreadable, so name the owner's IANA zone, e.g. Europe/Amsterdam)")
	}
	return c, nil
}

// LoadEnv reads the environment without requiring credentials. Helpers
// that must start without them (reask, drain, drive-fetch, resident,
// login) run under it.
func LoadEnv() *Config {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	c := &Config{
		AppleID:     os.Getenv("ICLOUD_APPLE_ID"),
		AppPassword: os.Getenv("ICLOUD_APP_PASSWORD"),
		AgentTZ:     getenv("AGENT_TZ", hostZone()),
		TZFile:      getenv("AGENT_TZ_FILE", "/etc/agent/timezone"),
		DefaultCal:  os.Getenv("AGENT_DEFAULT_CALENDAR"),
		StateDir:    getenv("ICLOUD_STATE", filepath.Join(home, ".icloud-mcp")),
		CDP:         getenv("ICLOUD_CDP", "http://127.0.0.1:9222"),
		DriveLibs:   getenv("DRIVE_LIBRARIES", "[]"),
	}
	c.SharedState = getenv("ICLOUD_SHARED_STATE", filepath.Join(c.StateDir, "shared"))
	c.AttachmentsDir = getenv("MAIL_ATTACHMENTS_DIR", filepath.Join(c.StateDir, "mail-attachments"))
	c.DriveStaging = getenv("DRIVE_STAGING", filepath.Join(c.StateDir, "drive-staging"))
	c.DriveEtags = getenv("DRIVE_ETAGS", filepath.Join(c.StateDir, "state", "drive-etags.json"))
	c.HealthDB = getenv("HEALTH_DB", filepath.Join(c.StateDir, "state", "health.sqlite"))
	c.HealthExport = os.Getenv("HEALTH_EXPORT_DIR")
	return c
}

// CDPPort takes the DevTools port from the configured CDP URL so a
// served port and a polled port cannot disagree. Defaults to 9222.
func (c *Config) CDPPort() int {
	if u, err := url.Parse(c.CDP); err == nil {
		if port, err := strconv.Atoi(u.Port()); err == nil && port > 0 {
			return port
		}
	}
	return 9222
}

// else AGENT_TZ. Neither existing raises rather than guessing: a zone
// guessed here types the wrong hour into an Apple picker and nothing
// errors.
func (c *Config) LocalTimezone() (string, error) {
	if raw, err := os.ReadFile(c.TZFile); err == nil {
		if name := strings.TrimSpace(string(raw)); name != "" {
			return name, nil
		}
	}
	if c.AgentTZ == "" {
		return "", fmt.Errorf("no timezone: %s is unreadable or empty and AGENT_TZ is "+
			"unset, so nothing here can say what a wall-clock time means", c.TZFile)
	}
	return c.AgentTZ, nil
}

// localtime is the host's zone link. A variable so tests can point it at
// a fixture.
var localtime = "/etc/localtime"

// hostZone is the host's IANA zone name from TZ or the /etc/localtime
// link (both macOS and Linux keep it under a zoneinfo directory), or ""
// when it is UTC or cannot be read.
func hostZone() string {
	name := strings.TrimPrefix(os.Getenv("TZ"), ":")
	if name == "" {
		target, err := os.Readlink(localtime)
		if err != nil {
			return ""
		}
		_, name, _ = strings.Cut(target, "zoneinfo/")
	}
	switch name {
	case "", "UTC", "Etc/UTC", "Etc/UCT", "UCT", "GMT", "Etc/GMT", "Universal", "Zulu":
		return ""
	}
	return name
}

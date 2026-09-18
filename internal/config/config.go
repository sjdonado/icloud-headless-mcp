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
	AgentTZ        string // AGENT_TZ (required as fallback; see LocalTimezone)
	TZFile         string // AGENT_TZ_FILE, default /etc/agent/timezone
	DefaultList    string // AGENT_DEFAULT_LIST, default "Today"
	DefaultCal     string // AGENT_DEFAULT_CALENDAR, default ""
	StateDir       string // ICLOUD_STATE, default $HOME
	SharedState    string // ICLOUD_SHARED_STATE, default $HOME/shared-state
	CDP            string // ICLOUD_CDP, default http://127.0.0.1:9222
	AttachmentsDir string // MAIL_ATTACHMENTS_DIR, default $HOME/mail-attachments
	DriveStaging   string // DRIVE_STAGING, default $ICLOUD_STATE/drive-staging
	DriveEtags     string // DRIVE_ETAGS, default $ICLOUD_STATE/state/drive-etags.json
	DriveLibs      string // DRIVE_LIBRARIES, default "[]"
	Transport      string // AGENT_MCP_TRANSPORT, default "stdio"
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
	// Required even when the TZ file would suffice: a zone guessed later
	// types the wrong hour into an Apple picker and nothing errors.
	if c.AgentTZ == "" {
		return nil, fmt.Errorf("missing required environment: AGENT_TZ")
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
		AppleID:        os.Getenv("ICLOUD_APPLE_ID"),
		AppPassword:    os.Getenv("ICLOUD_APP_PASSWORD"),
		AgentTZ:        os.Getenv("AGENT_TZ"),
		TZFile:         getenv("AGENT_TZ_FILE", "/etc/agent/timezone"),
		DefaultList:    getenv("AGENT_DEFAULT_LIST", "Today"),
		DefaultCal:     os.Getenv("AGENT_DEFAULT_CALENDAR"),
		StateDir:       getenv("ICLOUD_STATE", home),
		SharedState:    getenv("ICLOUD_SHARED_STATE", filepath.Join(home, "shared-state")),
		CDP:            getenv("ICLOUD_CDP", "http://127.0.0.1:9222"),
		AttachmentsDir: getenv("MAIL_ATTACHMENTS_DIR", filepath.Join(home, "mail-attachments")),
		DriveLibs:      getenv("DRIVE_LIBRARIES", "[]"),
		Transport:      getenv("AGENT_MCP_TRANSPORT", "stdio"),
	}
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

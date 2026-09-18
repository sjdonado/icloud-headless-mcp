// Subcommand drive-fetch (icloud-mcp drive-fetch) pulls the configured Drive libraries and
// stages files for another account to import. Exit 0 fetched, 1 nothing,
// 2 not runnable, 3 quiet hours.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/drivefetch"
	"github.com/sjdonado/icloud-headless-mcp/internal/dvlibraries"
)

const driveURL = "https://www.icloud.com/iclouddrive/"

func httpClient() *http.Client {
	return &http.Client{Timeout: 120 * time.Second}
}

func runDriveFetch() int {
	cfg := config.LoadEnv()
	state := browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP}
	zone, err := cfg.LocalTimezone()
	if err != nil {
		fmt.Fprintf(os.Stderr, "no timezone: %v\n", err)
		return 2
	}
	owner, err := time.LoadLocation(zone)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unknown timezone %q\n", zone)
		return 2
	}
	if browser.QuietHoursAt(owner, time.Now()) {
		fmt.Println("quiet hours: a fresh Drive load could raise an approval prompt nobody is awake for; skipped")
		return 3
	}
	libs := dvlibraries.ParseLibraries(cfg.DriveLibs)
	if len(libs) == 0 {
		fmt.Fprintln(os.Stderr, "DRIVE_LIBRARIES names no library, so there is nothing to pull")
		return 2
	}
	etags := map[string]any{}
	if raw, err := os.ReadFile(cfg.DriveEtags); err == nil {
		if jerr := json.Unmarshal(raw, &etags); jerr != nil {
			// An unparseable etag file means an interrupted run; refetch
			// nothing blindly. Surface it instead of silently re-pulling
			// the world.
			fmt.Fprintf(os.Stderr, "%s is not readable as JSON: %v\n", cfg.DriveEtags, jerr)
			return 1
		}
	}
	unlock, err := browser.AppLock(cfg.StateDir, "Drive", 150*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer unlock()
	cdp := browser.NewCDP(cfg.CDP)
	cookies, err := cdp.AllCookies()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot reach the browser; nothing fetched")
		return 2
	}
	if !browser.HasSessionToken(cookies) {
		fmt.Fprintln(os.Stderr, "the iCloud browser session has expired; nothing fetched")
		return 2
	}
	if state.Blocked() {
		fmt.Fprintln(os.Stderr, "iCloud is waiting for a device approval; nothing fetched")
		return 2
	}
	tab, urls, err := browser.OpenDriveTab(cdp, zone, driveURL, 90000, func(urls []string) bool {
		drive, doc := false, false
		for _, u := range urls {
			if svc, _, _, ok := drivefetch.SplitRequest(u); ok {
				if svc == "drivews" {
					drive = true
				}
				if svc == "docws" {
					doc = true
				}
			}
		}
		return drive && doc
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "drive did not start: %v\n", err)
		return 1
	}
	defer tab.ClosePage()
	var driveBase, driveQ, docBase, docQ string
	for _, u := range urls {
		svc, base, q, ok := drivefetch.SplitRequest(u)
		if !ok {
			continue
		}
		if svc == "drivews" && driveBase == "" {
			driveBase, driveQ = base, q
		}
		if svc == "docws" && docBase == "" {
			docBase, docQ = base, q
		}
	}
	if driveBase == "" || docBase == "" {
		fmt.Fprintln(os.Stderr, "drive did not start talking to its API within 90s")
		return 1
	}
	cookies, err = cdp.AllCookies()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not read browser cookies: %v\n", err)
		return 1
	}
	api := &drivefetch.API{
		DriveBase: driveBase, DriveQ: driveQ,
		DocBase: docBase, DocQ: docQ,
		Cookies: browser.CookieHeader(cookies),
		Client:  httpClient(),
	}
	var out, errout strings.Builder
	code := drivefetch.Run(api, etags, cfg.DriveStaging, libs, &out, &errout, time.Now())
	fmt.Print(out.String())
	fmt.Fprint(os.Stderr, errout.String())
	_ = os.MkdirAll(filepath.Dir(cfg.DriveEtags), 0o700)
	if raw, err := json.Marshal(etags); err == nil {
		_ = os.WriteFile(cfg.DriveEtags, raw, 0o600)
	}
	return code
}

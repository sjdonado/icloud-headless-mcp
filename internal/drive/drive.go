// Package drive is what the iCloud Drive pull last fetched, per library.
// Status only.
//
// The pull itself is not a tool and deliberately never will be: the fetcher
// runs as this account on a schedule, and whatever consumes the staged
// files runs as its own account. An MCP tool that could fetch, move, and
// import would collapse that separation into one.
package drive

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/dvlibraries"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
)

// age renders a timestamp the way drive_status rows read.
func age(ts *time.Time, now time.Time) map[string]any {
	if ts == nil {
		return map[string]any{"at": nil, "hours_ago": nil}
	}
	return map[string]any{
		"at":        ts.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"hours_ago": math.Round(now.Sub(*ts).Hours()*10) / 10,
	}
}

// Status reports when the pull last ran and what is staged per library.
// It cannot trigger the pull, and neither can anything else here.
func Status(cfg *config.Config, now time.Time) (map[string]any, error) {
	etags := map[string]any{}
	raw, err := os.ReadFile(cfg.DriveEtags)
	var lastRun *time.Time
	if err == nil {
		var parsed map[string]any
		if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
			return map[string]any{
				"error": cfg.DriveEtags + " is not readable as JSON: " + jerr.Error(),
				"note": "the pull writes this file at the end of every run, so an unparseable " +
					"one means a run was interrupted partway",
			}, nil
		}
		etags = parsed
		if info, serr := os.Stat(cfg.DriveEtags); serr == nil {
			t := info.ModTime()
			lastRun = &t
		}
	}
	perLibrary := map[string]any{}
	for _, lib := range dvlibraries.ParseLibraries(cfg.DriveLibs) {
		staged := filepath.Join(cfg.DriveStaging, lib.Dest)
		var newest *time.Time
		count := 0
		walkErr := filepath.Walk(staged, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			count++
			mt := info.ModTime()
			if newest == nil || mt.After(*newest) {
				newest = &mt
			}
			return nil
		})
		// A missing staging dir is normal (nothing pulled yet); any other
		// walk failure is a real error, not a healthy zero.
		if walkErr != nil && !os.IsNotExist(walkErr) {
			return nil, walkErr
		}
		perLibrary[lib.Name] = map[string]any{
			"kind":         lib.Kind,
			"staged_under": lib.Dest,
			// Zero is the healthy answer: the sync moves everything out of
			// staging after every fetch, so a non-zero count means a run
			// stopped between the fetch and the import.
			"staged_now":         count,
			"newest_staged_file": age(newest, now),
		}
	}
	var stale any
	if lastRun != nil {
		stale = now.Sub(*lastRun) > 25*time.Hour
	}
	return map[string]any{
		"last_pull": age(lastRun, now),
		// Named rather than implied, so the reader never does the
		// arithmetic; 25 hours is one daily run plus an hour of slack.
		"stale":    stale,
		"schedule": "a daily timer, persistent, so a missed run catches up on boot",
		// Total, not per library: the etag file is keyed by Apple's
		// opaque docwsid.
		"files_tracked": len(etags),
		"libraries":     perLibrary,
		"note": "Status only. The pull runs as this account from a timer and the import runs as " +
			"the owning service's account; no tool here can trigger either, because the " +
			"fetcher deliberately cannot reach those databases. `last_pull` is the freshness " +
			"signal; `staged_now` is 0 in normal operation because the sync empties staging " +
			"after every run.",
	}, nil
}

// Handlers wires drive_status. Status takes no lock.
func Handlers(cfg *config.Config) map[string]server.ToolHandlerFunc {
	return map[string]server.ToolHandlerFunc{
		"drive_status": func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, err := Status(cfg, time.Now())
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcpserver.ResultJSON(out)
		},
	}
}

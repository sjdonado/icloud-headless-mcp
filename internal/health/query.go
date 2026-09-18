package health

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
)

// ownerToday is today in the owner's zone, as a localDate string. Health
// days are authoritative localDates; the zone only anchors window edges.
func ownerToday(cfg *config.Config) (string, error) {
	zone, err := cfg.LocalTimezone()
	if err != nil {
		return "", err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return "", fmt.Errorf("unknown timezone %q", zone)
	}
	return time.Now().In(loc).Format("2006-01-02"), nil
}

func lastNDays(today string, n int) []string {
	end, err := time.Parse("2006-01-02", today)
	if err != nil {
		return nil
	}
	days := make([]string, n)
	for i := range days {
		days[n-1-i] = end.AddDate(0, 0, -i).Format("2006-01-02")
	}
	return days
}

// unconfigured answers when the extension was never set up: no database
// path means off, a missing file means never imported. Tools stay
// registered either way.
func unconfigured(cfg *config.Config) (map[string]any, bool) {
	if cfg.HealthDB == "" {
		return map[string]any{"unconfigured": true,
			"detail": "no health database configured; set HEALTH_EXPORT_DIR and run health-import"}, true
	}
	if _, err := os.Stat(cfg.HealthDB); err != nil {
		return map[string]any{"unconfigured": true,
			"detail": "health database has nothing imported yet; set HEALTH_EXPORT_DIR and run health-import"}, true
	}
	return nil, false
}

func openQueryDB(cfg *config.Config) (*sql.DB, map[string]any, bool) {
	if out, off := unconfigured(cfg); off {
		return nil, out, false
	}
	db, err := OpenRO(cfg.HealthDB)
	if err != nil {
		return nil, map[string]any{"error": err.Error()}, false
	}
	return db, nil, true
}

func nullReason(reason string) (any, string) {
	return nil, reason
}

// window validates a day-count argument: negative counts would panic the
// shared server on slice construction, and unbounded ones would OOM it.
func window(args mcpserver.Args, name string, def int) (int, error) {
	n, err := args.Int(name, def)
	if err != nil {
		return 0, err
	}
	if n < 1 || n > 366 {
		return 0, fmt.Errorf("%s must be 1 to 366 days, got %d", name, n)
	}
	return n, nil
}

// Handlers wires the six read-only health tools. No ask: nothing here
// reaches the owner's devices.
func Handlers(cfg *config.Config) map[string]server.ToolHandlerFunc {
	return map[string]server.ToolHandlerFunc{
		"health_status": func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcpserver.ResultJSON(Status(cfg))
		},
		"health_days": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			days, err := window(args, "days", 14)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcpserver.ResultJSON(Days(cfg, days))
		},
		"health_sleep": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			nights, err := window(args, "nights", 14)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcpserver.ResultJSON(Sleep(cfg, nights))
		},
		"health_effort": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			days, err := window(args, "days", 14)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			floor, err := args.Int("floor_bpm", 100)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcpserver.ResultJSON(Effort(cfg, days, floor))
		},
		"health_recovery": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			recent, err := window(args, "recent", 7)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			baseline, err := window(args, "baseline", 28)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcpserver.ResultJSON(Recovery(cfg, recent, baseline))
		},
		"health_sql": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			q, err := args.Str("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out, err := SQL(cfg, q)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcpserver.ResultJSON(out)
		},
	}
}

package dav

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
)

// newClient builds a per-call Client, which dials and discovers on every
// tool call.
func newClient(cfg *config.Config, ask func(ctx context.Context, question string) string) (*Client, error) {
	c, err := Dial(cfg)
	if err != nil {
		return nil, err
	}
	c.Ask = ask
	return c, nil
}

func runMap(fn func(*Client) (map[string]any, error), cfg *config.Config, ask func(ctx context.Context, question string) string) (*mcp.CallToolResult, error) {
	c, err := newClient(cfg, ask)
	if err != nil {
		return mcpserver.ErrorResult(err.Error())
	}
	out, err := fn(c)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcpserver.ResultJSON(out)
}

// Handlers wires the six calendar/contact tools. Each builds a fresh Client
// per call; DAV takes no lock.
func Handlers(cfg *config.Config, ask func(ctx context.Context, question string) string) map[string]server.ToolHandlerFunc {
	return map[string]server.ToolHandlerFunc{
		"list_calendars": func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return runMap(func(c *Client) (map[string]any, error) {
				return c.ListCalendars(ctx)
			}, cfg, ask)
		},
		"list_events": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			daysAhead, err := args.Int("days_ahead", 7)
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			daysBack, err := args.Int("days_back", 0)
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			opt, err := args.OptAll("calendar", "start", "end")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			return runMap(func(c *Client) (map[string]any, error) {
				return c.ListEvents(ctx, daysAhead, daysBack, opt[0], opt[1], opt[2])
			}, cfg, ask)
		},
		"create_event": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			summary, err := args.Str("summary")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			start, err := args.Str("start")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			opt, err := args.OptAll("end", "calendar", "location", "description", "timezone")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			return runMap(func(c *Client) (map[string]any, error) {
				return c.CreateEvent(ctx, summary, start, opt[0], opt[1], opt[2], opt[3], opt[4])
			}, cfg, ask)
		},
		"update_event": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			uid, err := args.Str("uid")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			opt, err := args.OptAll("summary", "start", "end", "location", "description", "timezone")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			return runMap(func(c *Client) (map[string]any, error) {
				return c.UpdateEvent(ctx, uid, opt[0], opt[1], opt[2], opt[3], opt[4], opt[5])
			}, cfg, ask)
		},
		"delete_event": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			uid, err := args.Str("uid")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			return runMap(func(c *Client) (map[string]any, error) {
				return c.DeleteEvent(ctx, uid)
			}, cfg, ask)
		},
		"search_contacts": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			query, err := args.Str("query")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			limit, err := args.Int("limit", 10)
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			return runMap(func(c *Client) (map[string]any, error) {
				return c.SearchContacts(ctx, query, limit)
			}, cfg, ask)
		},
	}
}

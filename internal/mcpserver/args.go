package mcpserver

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
)

// Args normalizes a tool call's arguments. MCP clients send JSON objects;
// numbers arrive as float64 and missing keys as absent.
type Args map[string]any

// RequestArgs extracts the argument map from a tool call request.
func RequestArgs(req mcp.CallToolRequest) Args {
	if m, ok := req.Params.Arguments.(map[string]any); ok {
		return Args(m)
	}
	return Args{}
}

// Str returns a required string argument.
func (a Args) Str(name string) (string, error) {
	v, ok := a[name]
	if !ok || v == nil {
		return "", fmt.Errorf("missing required argument %q", name)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", name)
	}
	return s, nil
}

// OptStr returns an optional string argument, or nil when absent. A
// present-but-wrongly-typed value is an error, not a silent default: the
// default usually widens scope (All iCloud, INBOX), and silently reading
// a wider scope than asked is the failure this server exists to prevent.
func (a Args) OptStr(name string) (*string, error) {
	v, ok := a[name]
	if !ok || v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("argument %q must be a string", name)
	}
	return &s, nil
}

// Int returns an integer argument or the default when absent. JSON numbers
// arrive as float64; whole floats are accepted, anything else is an error.
func (a Args) Int(name string, def int) (int, error) {
	v, ok := a[name]
	if !ok || v == nil {
		return def, nil
	}
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("argument %q must be an integer", name)
		}
		return int(n), nil
	case int:
		return n, nil
	case string:
		i, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("argument %q must be an integer", name)
		}
		return i, nil
	default:
		return 0, fmt.Errorf("argument %q must be an integer", name)
	}
}

// Bool returns a boolean argument or the default when absent.
func (a Args) Bool(name string, def bool) (bool, error) {
	v, ok := a[name]
	if !ok || v == nil {
		return def, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("argument %q must be a boolean", name)
	}
	return b, nil
}

// OptAll fetches several optional string arguments at once, in order.
func (a Args) OptAll(names ...string) ([]*string, error) {
	out := make([]*string, len(names))
	for i, name := range names {
		s, err := a.OptStr(name)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

// StrOr returns an optional string argument or def when absent.
func (a Args) StrOr(name, def string) (string, error) {
	s, err := a.OptStr(name)
	if err != nil || s == nil {
		return def, err
	}
	return *s, nil
}

// TruncateRunes cuts s to n characters. Python slices str (characters);
// byte slicing splits multi-byte text mid-rune.
func TruncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// ErrorResult renders a domain error the way the Python tools do: a result
// carrying {"error": ...}, not a protocol-level failure.
func ErrorResult(msg string) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(map[string]any{"error": msg})
	if err != nil {
		return mcp.NewToolResultError(msg), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

package session

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// resultMap unwraps one ResultJSON call into the payload map.
func resultMap(t *testing.T, res *mcp.CallToolResult, err error) map[string]any {
	t.Helper()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty content")
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want TextContent", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("result text is not JSON: %v (%s)", err, text.Text)
	}
	return out
}

func itoa(n int) string      { return strconv.Itoa(n) }
func itoa64(n int64) string  { return strconv.FormatInt(n, 10) }
func quoted(s string) string { return strconv.Quote(s) }

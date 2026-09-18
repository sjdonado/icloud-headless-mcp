package reminders

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
)

func TestSnippetsInterpolateCleanly(t *testing.T) {
	for name, snippet := range map[string]string{
		"rowGeo": rowGeo, "toggleDay": toggleDay, "calMonth": calMonth,
		"prevMonth": prevMonth, "nextMonth": nextMonth, "clickDay": clickDay,
		"segments": segments, "saveBtn": saveBtn, "popoverOpen": popoverOpen,
		"scrollEnd": scrollEnd, "focusNewRow": focusNewRow,
		"timeCheckbox": timeCheckbox, "segment": segment,
		"timeSegments": timeSegments, "completeGeo": completeGeo,
	} {
		if strings.Contains(snippet, "%!") {
			t.Errorf("%s has a formatting artifact", name)
		}
		if !strings.Contains(snippet, "const walk") {
			t.Errorf("%s lost the shared walker", name)
		}
	}
}

func TestParseDue(t *testing.T) {
	when, at, errMap := parseDue("2026-08-20")
	if errMap != nil || at != nil {
		t.Fatalf("date-only: %v %v %v", when, at, errMap)
	}
	if when.Format("2006-01-02") != "2026-08-20" {
		t.Fatalf("when = %v", when)
	}
	when, at, errMap = parseDue("2026-08-20 17:30")
	if errMap != nil || at == nil || at.Hour != 17 || at.Minute != 30 {
		t.Fatalf("datetime: %v %v %v", when, at, errMap)
	}
	if _, _, errMap = parseDue("tomorrow"); errMap == nil {
		t.Fatal("bad date accepted")
	} else if msg, _ := errMap["error"].(string); !strings.Contains(msg, "due must start with a date as YYYY-MM-DD") {
		t.Fatalf("error = %q", msg)
	}
	if _, _, errMap = parseDue("2026-08-20 25:00"); errMap == nil {
		t.Fatal("bad time accepted")
	} else if msg, _ := errMap["error"].(string); !strings.Contains(msg, "HH:MM on a 24-hour clock") {
		t.Fatalf("error = %q", msg)
	}
}

func TestDayLabel(t *testing.T) {
	d := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	if got := dayLabel(d); got != "Thursday, August 20, 2026" {
		t.Fatalf("got %q", got)
	}
	d = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if got := dayLabel(d); got != "Saturday, August 1, 2026" {
		t.Fatalf("no leading zero: %q", got)
	}
}

func TestRowDate(t *testing.T) {
	d, ok := rowDate("9/3/2026, Overdue")
	if !ok || d.Format("2006-01-02") != "2026-09-03" {
		t.Fatalf("got %v %v", d, ok)
	}
	if _, ok := rowDate("Today, 4:00 PM"); ok {
		t.Fatal("relative word must not parse as numeric")
	}
}

// ckBlob builds a TitleDocument blob with text at field 2 > 3 > 2,
// zlib-wrapped like Apple's.
func ckBlob(t *testing.T, title string) string {
	t.Helper()
	field := func(num uint64, b []byte) []byte {
		var out []byte
		key := num<<3 | 2
		for key >= 0x80 {
			out = append(out, byte(key)|0x80)
			key >>= 7
		}
		out = append(out, byte(key))
		n := uint64(len(b))
		var ln []byte
		for {
			if n < 0x80 {
				ln = append(ln, byte(n))
				break
			}
			ln = append(ln, byte(n)|0x80)
			n >>= 7
		}
		return append(append(out, ln...), b...)
	}
	doc := field(2, field(3, field(2, []byte(title))))
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(doc); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestCkTitle(t *testing.T) {
	if got := ckTitle(ckBlob(t, "Buy milk")); got != "Buy milk" {
		t.Fatalf("got %q", got)
	}
	if got := ckTitle(""); got != "" {
		t.Fatalf("empty blob: %q", got)
	}
	if got := ckTitle("!!!not-base64!!!"); got != "" {
		t.Fatalf("garbage blob: %q", got)
	}
	// Fallback path: printable runs in raw bytes.
	if got := ckTitle(base64.StdEncoding.EncodeToString([]byte{0x00, 0x01, 'h', 'i', 0x00})); got != "hi" {
		t.Fatalf("fallback: %q", got)
	}
}

func TestLocalDay(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-08-20 17:30 CEST = 15:30 UTC.
	ms := float64(time.Date(2026, 8, 20, 15, 30, 0, 0, time.UTC).UnixMilli())
	if got := localDay(ms, loc, false); got != "2026-08-20 17:30" {
		t.Fatalf("timed: %v", got)
	}
	// All-day: midnight UTC renders as the day alone, not 02:00.
	ms = float64(time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC).UnixMilli())
	if got := localDay(ms, loc, true); got != "2026-08-20" {
		t.Fatalf("all-day: %v", got)
	}
	if got := localDay(0, loc, false); got != nil {
		t.Fatalf("zero: %v", got)
	}
}

func TestCheckDay(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{owner: loc, now: func() time.Time {
		return time.Date(2026, 8, 20, 12, 0, 0, 0, loc)
	}}
	when := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	if msg := c.checkDay("8/20/2026", when); msg != "" {
		t.Fatalf("numeric match: %q", msg)
	}
	if msg := c.checkDay("Today", when); msg != "" {
		t.Fatalf("today: %q", msg)
	}
	if msg := c.checkDay("8/21/2026", when); msg == "" {
		t.Fatal("wrong day accepted")
	}
	if msg := c.checkDay("sometime later", when); msg != "" {
		t.Fatalf("unrecognised form must not accuse: %q", msg)
	}
}

func TestCkSyncFullThenIncremental(t *testing.T) {
	dir := t.TempDir()
	c := &Client{state: browser.State{Dir: dir, Shared: dir}}
	calls := 0
	eval := func(expr string, arg any) (json.RawMessage, error) {
		calls++
		switch {
		case expr == ckZonesJS:
			return json.Marshal([]any{
				map[string]any{"scope": "private", "zoneName": "Reminders"},
			})
		default:
			args, _ := arg.([]any)
			tok, _ := args[1].(string)
			if tok == "" {
				return json.Marshal(map[string]any{
					"token": "tok-1",
					"changed": []any{
						map[string]any{"n": "r1", "t": "Reminder", "c": true, "cd": 1758000000000.0,
							"list": "l1", "title": "eGk="},
						map[string]any{"n": "l1", "t": "List", "name": "Reminders"},
					},
					"gone": []any{},
				})
			}
			return json.Marshal(map[string]any{
				"token": "tok-2", "changed": []any{}, "gone": []any{},
			})
		}
	}
	cache, err := c.ckSyncEval(eval)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	records, _ := cache["records"].(map[string]any)
	if len(records) != 2 {
		t.Fatalf("records = %v", records)
	}
	sync1, _ := cache["sync"].([]map[string]any)
	if len(sync1) != 1 || sync1[0]["pass"] != "full" {
		t.Fatalf("sync = %v", sync1)
	}
	// Second pass reuses the stored token: incremental, no refetch.
	cache2, err := c.ckSyncEval(eval)
	if err != nil {
		t.Fatalf("sync2: %v", err)
	}
	sync2, _ := cache2["sync"].([]map[string]any)
	if len(sync2) != 1 || sync2[0]["pass"] != "incremental" {
		t.Fatalf("sync2 = %v", sync2)
	}
}

func TestOpenErrorRoutesByFailure(t *testing.T) {
	approval := openError(&browser.NeedsApprovalError{Msg: "waiting"})
	if approval["needs_device_approval"] != true {
		t.Fatalf("lapsed grant should carry needs_device_approval: %v", approval)
	}
	login := openError(&browser.SignedOutError{Msg: "expired"})
	if login["needs_login"] != true {
		t.Fatalf("expired session should carry needs_login: %v", login)
	}
	if _, ok := login["needs_device_approval"]; ok {
		t.Fatalf("expired session must not carry needs_device_approval: %v", login)
	}
}

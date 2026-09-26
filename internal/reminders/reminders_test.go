package reminders

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
)

func TestSnippetsInterpolateCleanly(t *testing.T) {
	for name, snippet := range map[string]string{
		"rowGeo": rowGeo, "toggleDay": toggleDay, "calMonth": calMonth,
		"prevMonth": prevMonth, "nextMonth": nextMonth, "clickDay": clickDay,
		"segments": segments, "saveBtn": saveBtn, "popoverOpen": popoverOpen,
		"scrollEnd": scrollEnd, "rowWithAria": rowWithAria, "countAria": countAria, "focusEmptyRow": focusEmptyRow, "focusRowTitled": focusRowTitled, "focusInRow": focusInRow, "focusSegment": focusSegment,
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

// Probed from a real record on 2026-09-26: a NotesDocument holding
// "probe note: línea uno", and an AlarmTrigger's DateComponentsData for
// 2026-09-26 09:00 Europe/Berlin.
const (
	fxNotesDoc = "eJzjYBCq4WAQYJAqExIrKMpPSlXIyy9JtVLIObw2LzVRoTQvX0qAiwWkAqgGTGswgkUYgSKiUmBag0lKjIsDKPcfCPiB6uBsJRkuKS6BNecfhO50epvnvdle0V7A4YUQE4coEDNqAWkA/wcdbw=="
	fxAlarmDC  = "eyJ5ZWFyIjoyMDI2LCJtb250aCI6OSwiZGF5IjoyNiwiaG91ciI6OSwibWludXRlIjowLCJzZWNvbmQiOjAsInRpbWVab25lIjp7ImlkZW50aWZpZXIiOiJFdXJvcGUvQmVybGluIn19"
)

func fxTitle(t *testing.T, text string) string {
	t.Helper()
	// TitleDocument: zlib over a protobuf-ish doc; ckTitle reads the
	// longest printable string, so the text alone round-trips.
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	w.Write([]byte{0x0a, byte(len(text))})
	w.Write([]byte(text))
	w.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func fxRecords(t *testing.T) map[string]any {
	due := float64(time.Date(2026, 9, 26, 7, 0, 0, 0, time.UTC).UnixMilli())
	allDay := float64(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	created := float64(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC).UnixMilli())
	return map[string]any{
		"List/home":       map[string]any{"n": "List/home", "t": "List", "name": "Home"},
		"List/work":       map[string]any{"n": "List/work", "t": "List", "name": "Work"},
		"List/gone":       map[string]any{"n": "List/gone", "t": "List", "name": "Old", "del": true},
		"Reminder/A":      map[string]any{"n": "Reminder/A", "t": "Reminder", "list": "List/home", "title": fxTitle(t, "Call mom"), "notes": fxNotesDoc, "dd": due, "cr": created, "md": created, "pri": 1.0, "flag": true, "alarms": []any{"AL1"}, "rrules": 1.0, "tags": 2.0},
		"Reminder/B":      map[string]any{"n": "Reminder/B", "t": "Reminder", "list": "List/home", "title": fxTitle(t, "Call mom"), "cr": created},
		"Reminder/C":      map[string]any{"n": "Reminder/C", "t": "Reminder", "list": "List/work", "title": fxTitle(t, "Ship report"), "dd": allDay, "allday": true, "cr": created},
		"Reminder/D":      map[string]any{"n": "Reminder/D", "t": "Reminder", "list": "List/work", "title": fxTitle(t, "Done thing"), "c": true, "cd": due},
		"Reminder/E":      map[string]any{"n": "Reminder/E", "t": "Reminder", "list": "List/home", "title": fxTitle(t, "Deleted"), "del": true},
		"Alarm/AL1":       map[string]any{"n": "Alarm/AL1", "t": "Alarm", "rem": "Reminder/A"},
		"AlarmTrigger/T1": map[string]any{"n": "AlarmTrigger/T1", "t": "AlarmTrigger", "alarm": "Alarm/AL1", "ttype": "Date", "dc": fxAlarmDC},
	}
}

func TestOpenFromRecordsRichAndAllLists(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Amsterdam")
	c := &Client{owner: loc}
	out := c.openFromRecords(fxRecords(t), []map[string]any{{"zone": "private:", "pass": "full"}}, "")
	if err := mcpserver.CheckOutput("list_reminders", out); err != nil {
		t.Fatal(err)
	}
	if out["source"] != "records" || out["count"] != 3 {
		t.Fatalf("all lists = count %v, source %v; want the 3 open undeleted reminders", out["count"], out["source"])
	}
	rows := out["reminders"].([]map[string]any)
	byID := map[string]map[string]any{}
	for _, r := range rows {
		byID[r["id"].(string)] = r
	}
	a := byID["A"]
	for key, want := range map[string]any{
		"list": "Home", "title": "Call mom", "notes": "probe note: línea uno",
		"due": "2026-09-26T09:00:00+02:00", "all_day": false, "priority": "high", "flagged": true,
		"created": "2026-09-01T12:00:00+02:00", "recurring": true, "tag_count": 2, "completed": false,
	} {
		if a[key] != want {
			t.Errorf("A %s = %#v, want %#v", key, a[key], want)
		}
	}
	if al, _ := a["alarms"].([]map[string]any); len(al) != 1 || al[0]["at"] != "2026-09-26T09:00:00+02:00" || al[0]["type"] != "date" {
		t.Errorf("alarms = %v", a["alarms"])
	}
	if c := byID["C"]; c["due"] != "2026-09-28" || c["all_day"] != true || c["list"] != "Work" {
		t.Errorf("all-day = %v", c)
	}
	for _, missing := range []string{"notes", "due", "priority", "alarms", "recurring"} {
		if _, ok := byID["B"][missing]; ok {
			t.Errorf("B carries %s it does not hold", missing)
		}
	}
	// Home before Work; within Home, dated before undated.
	if rows[0]["id"] != "A" || rows[1]["id"] != "B" || rows[2]["id"] != "C" {
		t.Errorf("order = %v %v %v", rows[0]["id"], rows[1]["id"], rows[2]["id"])
	}
	one := c.openFromRecords(fxRecords(t), nil, "work")
	if one["count"] != 1 || one["reminders"].([]map[string]any)[0]["id"] != "C" {
		t.Errorf("one list = %v", one)
	}
	if bad := c.openFromRecords(fxRecords(t), nil, "nope"); bad["error"] == nil {
		t.Error("an unknown list is not an error")
	}
}

func TestSyncFailed(t *testing.T) {
	if !syncFailed(nil) || !syncFailed([]map[string]any{{"error": "x"}}) {
		t.Error("no zone answering must read as failed")
	}
	if syncFailed([]map[string]any{{"error": "x"}, {"pass": "full"}}) {
		t.Error("one zone answering is a usable sync")
	}
}

func TestCkCacheVersionInPath(t *testing.T) {
	c := &Client{state: browser.State{Dir: "/x", Shared: "/x"}}
	// The pre-version cache was reminders-ck.json; this one must not read it.
	if filepath.Base(c.ckCachePath()) != fmt.Sprintf("reminders-ck-v%d.json", ckCacheVersion) {
		t.Fatalf("cache path %s does not carry the version", c.ckCachePath())
	}
}

func TestCompleteParamsQueueIDAlone(t *testing.T) {
	if p := completeParams("ABC", "Call mom", "Home"); len(p) != 1 || p["id"] != "ABC" {
		t.Fatalf("id mode queued %v; a stale title beside the id would mislabel the replay", p)
	}
	if p := completeParams("", "Call mom", "Home"); p["title"] != "Call mom" || p["list_name"] != "Home" {
		t.Fatalf("title mode queued %v", p)
	}
}

func TestAlarmsSortedAndTypeOptional(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	c := &Client{owner: loc}
	late := base64.StdEncoding.EncodeToString([]byte(`{"year":2026,"month":9,"day":26,"hour":18,"minute":0,"second":0,"timeZone":{"identifier":"Europe/Berlin"}}`))
	records := map[string]any{
		"Alarm/1":        map[string]any{"t": "Alarm", "rem": "Reminder/A"},
		"Alarm/2":        map[string]any{"t": "Alarm", "rem": "Reminder/A"},
		"AlarmTrigger/1": map[string]any{"t": "AlarmTrigger", "alarm": "Alarm/1", "ttype": "Date", "dc": late},
		"AlarmTrigger/2": map[string]any{"t": "AlarmTrigger", "alarm": "Alarm/2", "dc": fxAlarmDC},
	}
	for run := 0; run < 20; run++ {
		got := c.indexRecords(records).alarms["Reminder/A"]
		if len(got) != 2 || got[0]["at"] != "2026-09-26T09:00:00+02:00" || got[1]["at"] != "2026-09-26T18:00:00+02:00" {
			t.Fatalf("run %d: alarms = %v, want sorted by time", run, got)
		}
		if _, ok := got[0]["type"]; ok {
			t.Fatalf("a trigger without Type reports type %v", got[0]["type"])
		}
	}
}

func TestMatchListsPrefersExactName(t *testing.T) {
	lists := map[string]string{"L1": "Work", "L2": "Homework", "L3": "Home"}
	if got := matchLists(lists, "work"); len(got) != 1 || got["L1"] != "Work" {
		t.Fatalf("work = %v, want Work alone", got)
	}
	if got := matchLists(lists, "hom"); len(got) != 2 {
		t.Fatalf("hom = %v, want the two substring matches", got)
	}
	if got := matchLists(lists, ""); len(got) != 3 {
		t.Fatalf("empty = %v, want every list", got)
	}
}

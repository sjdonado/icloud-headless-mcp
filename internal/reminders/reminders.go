// Package reminders is Reminders through the resident browser: CalDAV
// cannot do this, serving a legacy store whose writes succeed and are
// invisible everywhere. Same machinery as Notes: its own warm tab, exact
// class-token matching, real pointer events, and an honest failure when
// Apple's temporary access lapses.
package reminders

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
	"github.com/sjdonado/icloud-headless-mcp/internal/queue"
)

const (
	appName   = "Reminders"
	appURL    = "https://www.icloud.com/reminders/"
	frameHint = "reminders2"
	readyKind = "rm-list-menu-item"
)

// dueHintRe finds date-shaped text: the due date is an unclassed leaf, so
// it is found by shape rather than by selector.
var dueHintRe = regexp.MustCompile(`(?i)(\d{1,2}/\d{1,2}/\d{2,4})|\b(today|tomorrow|yesterday)\b|\b(mon|tue|wed|thu|fri|sat|sun)\b|\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)\b|\d{1,2}:\d{2}`)

// Client drives the Reminders app. Built per call; the per-app lock
// serialises concurrent calls.
type Client struct {
	cfg   *config.Config
	state browser.State
	owner *time.Location
	now   func() time.Time
	queue *queue.Queue
	Ask   func(ctx context.Context, question string) string
}

// Dial validates the environment contract. It performs no network I/O.
func Dial(cfg *config.Config) (*Client, error) {
	tzName, err := cfg.LocalTimezone()
	if err != nil {
		return nil, err
	}
	owner, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %v", tzName, err)
	}
	return &Client{
		cfg:   cfg,
		state: browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP},
		owner: owner,
		now:   time.Now,
		queue: queue.New(cfg.SharedState + "/pending.jsonl"),
	}, nil
}

func (c *Client) withApp(ctx context.Context, fn func(tab *browser.Tab) (map[string]any, error)) (map[string]any, error) {
	unlock, err := browser.AppLock(c.state.Dir, appName, 150*time.Second)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	defer unlock()
	_ = ctx
	tab, err := browser.App{Name: appName, URL: appURL, FrameHint: frameHint, ReadyClass: readyKind}.Open(c.state, c.owner, c.now())
	if err != nil {
		if _, ok := err.(*browser.NeedsApprovalError); ok {
			return map[string]any{"error": err.Error(), "needs_device_approval": true}, nil
		}
		return map[string]any{"error": shortError(err)}, nil
	}
	defer tab.Close()
	return fn(tab)
}

func shortError(err error) string {
	typeName := fmt.Sprintf("%T", err)
	if i := strings.LastIndex(typeName, "."); i >= 0 {
		typeName = typeName[i+1:]
	}
	return strings.TrimPrefix(typeName, "*") + ": " + err.Error()
}

func sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// Item is one reminder row as the app renders it.
type Item struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Leaves        []string `json:"leaves"`
	DueText       string   `json:"dueText"`
	Completed     bool     `json:"completed"`
	PriorityText  string   `json:"priorityText"`
	PriorityLevel string   `json:"priorityLevel"`
	Flagged       bool     `json:"flagged"`
}

func (c *Client) items(tab *browser.Tab) ([]map[string]any, error) {
	raw, err := tab.Eval(rowsJS, nil)
	if err != nil {
		return nil, err
	}
	var rows []Item
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	var out []map[string]any
	seen := map[string]bool{}
	for _, row := range rows {
		title := strings.TrimSpace(row.Title)
		if title == "" {
			continue
		}
		// Dedup by Apple's row id where there is one: by title alone, a
		// weekly reminder ticked off twenty times collapses.
		key := row.ID
		if key == "" {
			key = title
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		// The span repeats an overdue date on a visually hidden second
		// line: take the first.
		due := strings.TrimSpace(strings.Split(row.DueText, "\n")[0])
		if due == "" {
			for _, leaf := range row.Leaves {
				text := strings.TrimSpace(leaf)
				if text == "" || text == title || strings.ToLower(text) == "notes" {
					continue
				}
				if dueHintRe.MatchString(text) {
					due = text
					break
				}
			}
		}
		level := strings.TrimSpace(row.PriorityLevel)
		priority := map[string]string{"1": "high", "5": "medium", "9": "low"}[level]
		if priority == "" && row.PriorityText != "" {
			priority = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(row.PriorityText, "Priority", "")))
			if priority == "" {
				priority = ""
			}
		}
		var priorityOut, idOut any
		if priority != "" {
			priorityOut = priority
		}
		id := row.ID
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = id[i+1:]
		}
		if id != "" {
			idOut = id
		}
		var dueOut any
		if due != "" {
			dueOut = due
		}
		out = append(out, map[string]any{
			"id": idOut, "title": title, "due": dueOut,
			"completed": row.Completed, "priority": priorityOut, "flagged": row.Flagged,
		})
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, nil
}

// ckKeys are the record fields the sync pulls. The date filter happens
// here, over the cache: the Reminder type is not marked indexable, so no
// server-side query can filter it.
var ckKeys = []string{"Completed", "CompletionDate", "DueDate", "AllDay", "CreationDate", "List", "TitleDocument", "Deleted", "Flagged", "Priority", "Name"}

func (c *Client) ckCachePath() string {
	return filepath.Join(c.state.StateDir(), "reminders-ck.json")
}

// ckSync brings the local record cache up to date. The first pass walks
// Apple's whole store and takes minutes; later passes are one round trip
// per zone on the sync token. A refused token falls back to a full pass,
// and the cache file is written atomically with mode 0600.
func (c *Client) ckSync(tab *browser.Tab) (map[string]any, error) {
	return c.ckSyncEval(tab.EvalMain)
}

func (c *Client) ckSyncEval(eval func(string, any) (json.RawMessage, error)) (map[string]any, error) {
	cache := map[string]any{"tokens": map[string]any{}, "records": map[string]any{}}
	if raw, err := os.ReadFile(c.ckCachePath()); err == nil {
		var parsed map[string]any
		if jerr := json.Unmarshal(raw, &parsed); jerr == nil {
			if _, ok := parsed["tokens"].(map[string]any); ok {
				if _, ok := parsed["records"].(map[string]any); ok {
					cache = parsed
				}
			}
		}
	}
	tokens := cache["tokens"].(map[string]any)
	records := cache["records"].(map[string]any)
	zonesRaw, err := eval(ckZonesJS, nil)
	if err != nil {
		return nil, err
	}
	var zones []map[string]any
	if err := json.Unmarshal(zonesRaw, &zones); err != nil {
		return nil, err
	}
	var report []map[string]any
	for _, zone := range zones {
		scope, _ := zone["scope"].(string)
		owner, _ := zone["owner"].(string)
		zoneName, _ := zone["zoneName"].(string)
		if scope == "" || zoneName == "" {
			report = append(report, map[string]any{"zone": "malformed", "error": "zone entry missing scope or name"})
			continue
		}
		key := scope + ":" + owner
		token, _ := tokens[key]
		syncOnce := func(tok any) (map[string]any, error) {
			raw, err := eval(ckSyncJS, []any{zone, tok, ckKeys})
			if err != nil {
				return nil, err
			}
			var result map[string]any
			if err := json.Unmarshal(raw, &result); err != nil {
				return nil, err
			}
			return result, nil
		}
		result, err := syncOnce(token)
		if err != nil {
			return nil, err
		}
		if _, hasErr := result["error"]; hasErr && token != nil {
			result, err = syncOnce(nil)
			if err != nil {
				return nil, err
			}
			token = nil
		}
		if msg, hasErr := result["error"]; hasErr {
			report = append(report, map[string]any{"zone": key, "error": msg})
			continue
		}
		if token == nil {
			// Full pass: anything cached for this zone that did not come
			// back is gone.
			for name, rec := range records {
				if rm, ok := rec.(map[string]any); ok && rm["zone"] == key {
					delete(records, name)
				}
			}
		}
		if gone, ok := result["gone"].([]any); ok {
			for _, g := range gone {
				if name, ok := g.(string); ok {
					delete(records, name)
				}
			}
		}
		changed := 0
		if list, ok := result["changed"].([]any); ok {
			changed = len(list)
			for _, item := range list {
				rec, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := rec["t"].(string); t != "Reminder" && t != "List" {
					continue
				}
				if n, _ := rec["n"].(string); n != "" {
					rec["zone"] = key
					records[n] = rec
				}
			}
		}
		removed := 0
		if gone, ok := result["gone"].([]any); ok {
			removed = len(gone)
		}
		tokens[key] = result["token"]
		pass := "full"
		if token != nil {
			pass = "incremental"
		}
		ms, _ := result["ms"].(float64)
		pages, _ := result["pages"].(float64)
		_ = pages
		report = append(report, map[string]any{"zone": key, "pass": pass,
			"changed": changed, "removed": removed, "seconds": round1(ms / 1000)})
	}
	_ = os.MkdirAll(filepath.Dir(c.ckCachePath()), 0o700)
	tmp := c.ckCachePath() + ".tmp"
	raw, err := json.Marshal(cache)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return nil, err
	}
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, c.ckCachePath()); err != nil {
		return nil, err
	}
	cache["sync"] = report
	return cache, nil
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}

// localDay renders a CloudKit millisecond timestamp. An all-day due date
// is stored as midnight UTC, which renders as a wrong early-morning time
// east of it: show the day alone when Apple says the reminder has no time.
func localDay(ms float64, owner *time.Location, allDay bool) any {
	if ms == 0 {
		return nil
	}
	stamp := time.UnixMilli(int64(ms)).In(owner)
	if allDay {
		return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02")
	}
	return stamp.Format("2006-01-02 15:04")
}

func parseDay(text string, owner *time.Location, end bool) (time.Time, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, false, nil
	}
	d, err := time.Parse("2006-01-02", text)
	if err != nil {
		return time.Time{}, false, err
	}
	at := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, owner)
	if end {
		at = at.AddDate(0, 0, 1)
	}
	return at, true, nil
}

// Completed returns what the owner ticked off, with the exact tick time,
// from Apple's records. Deleted reminders are excluded even when they
// were completed first.
func (c *Client) Completed(ctx context.Context, listName, since, until string, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	lo, hasLo, err := parseDay(since, c.owner, false)
	if err != nil {
		return map[string]any{"error": "since and until must be YYYY-MM-DD: " + err.Error()}, nil
	}
	hi, hasHi, err := parseDay(until, c.owner, true)
	if err != nil {
		return map[string]any{"error": "since and until must be YYYY-MM-DD: " + err.Error()}, nil
	}
	return c.withApp(ctx, func(tab *browser.Tab) (map[string]any, error) {
		cache, err := c.ckSync(tab)
		if err != nil {
			return nil, err
		}
		records, _ := cache["records"].(map[string]any)
		syncReport, _ := cache["sync"].([]map[string]any)
		lists := map[string]string{}
		for n, item := range records {
			r, ok := item.(map[string]any)
			if !ok || r["t"] != "List" || r["del"] == true {
				continue
			}
			if name, _ := r["name"].(string); name != "" {
				lists[n] = name
			}
		}
		wanted := strings.ToLower(strings.TrimSpace(listName))
		chosen := map[string]string{}
		for n, name := range lists {
			if wanted == "" || strings.Contains(strings.ToLower(name), wanted) {
				chosen[n] = name
			}
		}
		if len(chosen) == 0 {
			names := []string{}
			for _, name := range lists {
				names = append(names, name)
			}
			sort.Strings(names)
			return map[string]any{"error": fmt.Sprintf("no list matching %s; available: %v", listName, names),
				"sync": syncReport}, nil
		}
		var loMS, hiMS float64
		if hasLo {
			loMS = float64(lo.UnixMilli())
		}
		if hasHi {
			hiMS = float64(hi.UnixMilli())
		}
		perList := map[string][]map[string]any{}
		for _, name := range chosen {
			perList[name] = []map[string]any{}
		}
		for _, item := range records {
			r, ok := item.(map[string]any)
			if !ok || r["t"] != "Reminder" || r["del"] == true || r["c"] != true {
				continue
			}
			cd, _ := r["cd"].(float64)
			if cd == 0 {
				continue
			}
			listID, _ := r["list"].(string)
			name, ok := chosen[listID]
			if !ok {
				continue
			}
			if (hasLo && cd < loMS) || (hasHi && cd >= hiMS) {
				continue
			}
			perList[name] = append(perList[name], r)
		}
		var out []map[string]any
		sortedNames := []string{}
		for name := range perList {
			sortedNames = append(sortedNames, name)
		}
		sort.Strings(sortedNames)
		for _, name := range sortedNames {
			rows := perList[name]
			sort.Slice(rows, func(i, j int) bool {
				ci, _ := rows[i]["cd"].(float64)
				cj, _ := rows[j]["cd"].(float64)
				return ci > cj
			})
			completed := []map[string]any{}
			for _, r := range rows[:min(limit, len(rows))] {
				id, _ := r["n"].(string)
				if i := strings.LastIndex(id, "/"); i >= 0 {
					id = id[i+1:]
				}
				dd, _ := r["dd"].(float64)
				allday, _ := r["allday"].(bool)
				pri, _ := r["pri"].(float64)
				var priority any
				switch int(pri) {
				case 1:
					priority = "high"
				case 5:
					priority = "medium"
				case 9:
					priority = "low"
				}
				title, _ := r["title"].(string)
				cd, _ := r["cd"].(float64)
				completed = append(completed, map[string]any{
					"id": id, "title": ckTitle(title),
					"completed_at": localDay(cd, c.owner, false),
					"due":          localDay(dd, c.owner, allday),
					"flagged":      r["flag"] == true,
					"priority":     priority,
				})
			}
			if completed == nil {
				completed = []map[string]any{}
			}
			out = append(out, map[string]any{"list": name,
				"completed_total": len(rows), "returned": min(limit, len(rows)),
				"completed": completed})
		}
		if out == nil {
			out = []map[string]any{}
		}
		total := 0
		for _, v := range perList {
			total += len(v)
		}
		var sinceOut, untilOut any
		if since != "" {
			sinceOut = since
		}
		if until != "" {
			untilOut = until
		}
		return map[string]any{
			"window":          map[string]any{"since": sinceOut, "until": untilOut, "timezone": c.owner.String()},
			"total_in_window": total, "lists": out, "sync": syncReport,
			"note": "completed_at is when the owner ticked it, in the owner's local time; due is what it was set for. Deleted reminders are excluded."}, nil
	})
}

func (c *Client) lists(tab *browser.Tab) ([]string, error) {
	names, err := tab.Collect(readyKind)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

// openList selects a list, returning an error string on failure. It clears
// a blocking iCloud alert first: a click under one does nothing while the
// DOM looks perfectly healthy.
func (c *Client) openList(tab *browser.Tab, listName string) string {
	if title := tab.DismissAlert(); title != "" {
		sleep(1200)
	}
	names, err := c.lists(tab)
	if err != nil {
		return shortError(err)
	}
	wanted := strings.ToLower(strings.TrimSpace(listName))
	match := -1
	for i, n := range names {
		if strings.Contains(strings.ToLower(n), wanted) {
			match = i
			break
		}
	}
	if match < 0 {
		return fmt.Sprintf("no list matching %s; available: %v", listName, names)
	}
	if _, err := tab.Click(readyKind, match); err != nil {
		return shortError(err)
	}
	sleep(4000)
	return ""
}

// Lists returns the reminder lists as they appear on the owner's devices.
func (c *Client) Lists(ctx context.Context) (map[string]any, error) {
	return c.withApp(ctx, func(tab *browser.Tab) (map[string]any, error) {
		names, err := c.lists(tab)
		if err != nil {
			return nil, err
		}
		return map[string]any{"count": len(names), "lists": names}, nil
	})
}

// ListReminders reads the open reminders in one list.
func (c *Client) ListReminders(ctx context.Context, listName string) (map[string]any, error) {
	return c.withApp(ctx, func(tab *browser.Tab) (map[string]any, error) {
		if problem := c.openList(tab, listName); problem != "" {
			return map[string]any{"error": problem}, nil
		}
		all, err := c.items(tab)
		if err != nil {
			return nil, err
		}
		var items []map[string]any
		for _, r := range all {
			if completed, _ := r["completed"].(bool); !completed {
				items = append(items, r)
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		dated, prioritised := 0, 0
		for _, r := range items {
			if r["due"] != nil {
				dated++
			}
			if r["priority"] != nil {
				prioritised++
			}
		}
		return map[string]any{"list": listName, "count": len(items),
			"with_due_date": dated, "with_priority": prioritised, "reminders": items,
			"note": "due values are exactly what the app displays, so they are relative: " +
				"'Today, 4:00 PM' means today in the owner's zone. priority is high, medium or " +
				"low as set on the reminder; flagged is Apple's flag. Neither can be set from here yet."}, nil
	})
}

// ckTitle decodes a CloudKit TitleDocument: a zlib or gzip stream holding
// a protobuf document with the text at field 2 > 3 > 2. Falls back to the
// first printable run rather than to nothing.
func ckTitle(blob string) string {
	if blob == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return ""
	}
	if inflated, err := tryInflate(raw); err == nil {
		raw = inflated
	}
	if title, ok := ckFieldPath(raw); ok {
		return title
	}
	var run []byte
	flush := func() string {
		if len(run) >= 2 {
			return string(run)
		}
		return ""
	}
	for i := 0; i < len(raw); {
		c := raw[i]
		switch {
		case c >= 0x20 && c < 0x7f:
			run = append(run, c)
			i++
		case c >= 0xc2 && c <= 0xf4 && i+1 < len(raw) && raw[i+1] >= 0x80 && raw[i+1] < 0xc0:
			n := 2
			if c >= 0xe0 {
				n = 3
			}
			if c >= 0xf0 {
				n = 4
			}
			if i+n > len(raw) {
				i++
				continue
			}
			ok := true
			for k := 1; k < n; k++ {
				if raw[i+k] < 0x80 || raw[i+k] >= 0xc0 {
					ok = false
				}
			}
			if !ok {
				if s := flush(); s != "" {
					return s
				}
				run = nil
				i++
				continue
			}
			run = append(run, raw[i:i+n]...)
			i += n
		default:
			if s := flush(); s != "" {
				return s
			}
			run = nil
			i++
		}
	}
	return flush()
}

func tryInflate(raw []byte) ([]byte, error) {
	if out, err := readAllFlate(raw); err == nil {
		return out, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

func readAllFlate(raw []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// ckFields walks protobuf fields: (number, value) with bytes for
// length-delimited and uint64 for varints. Unknown wire types stop the
// walk rather than misreading the rest.
func ckFields(b []byte) []ckField {
	var out []ckField
	for i := 0; i < len(b); {
		key, n := ckVarint(b, i)
		if n < 0 {
			return out
		}
		i = n
		num, wire := key>>3, key&7
		switch wire {
		case 0:
			v, n := ckVarint(b, i)
			if n < 0 {
				return out
			}
			out = append(out, ckField{num: num, num64: v})
			i = n
		case 2:
			ln, n := ckVarint(b, i)
			if n < 0 || n+int(ln) > len(b) {
				return out
			}
			out = append(out, ckField{num: num, bytes: b[n : n+int(ln)]})
			i = n + int(ln)
		case 1:
			if i+8 > len(b) {
				return out
			}
			i += 8
		case 5:
			if i+4 > len(b) {
				return out
			}
			i += 4
		default:
			return out
		}
	}
	return out
}

type ckField struct {
	num   uint64
	num64 uint64
	bytes []byte
}

func ckVarint(b []byte, i int) (uint64, int) {
	var n uint64
	var shift uint
	for i < len(b) {
		byte_ := b[i]
		i++
		n |= uint64(byte_&0x7F) << shift
		shift += 7
		if byte_ < 0x80 {
			return n, i
		}
		if shift > 63 {
			return 0, -1
		}
	}
	return 0, -1
}

func ckFieldPath(raw []byte) (string, bool) {
	for _, f := range ckFields(raw) {
		if f.num != 2 || f.bytes == nil {
			continue
		}
		for _, a := range ckFields(f.bytes) {
			if a.num != 3 || a.bytes == nil {
				continue
			}
			for _, t := range ckFields(a.bytes) {
				if t.num == 2 && t.bytes != nil {
					return string(t.bytes), true
				}
			}
		}
	}
	return "", false
}

// dayLabel renders Apple's canonical day-cell label: "Thursday, August
// 20, 2026", no leading zero on the day.
func dayLabel(d time.Time) string {
	return fmt.Sprintf("%s, %s %d, %d", d.Weekday(), d.Month(), d.Day(), d.Year())
}

// rowDate extracts the numeric date a row displays, or nil when the row
// shows a relative word instead. Numeric forms only, deliberately:
// resolving "Today" needs a clock, and which clock is exactly the question
// this path must not have an opinion about.
var rowDateRe = regexp.MustCompile(`(\d{1,2})/(\d{1,2})/(\d{4})`)

func rowDate(landed string) (time.Time, bool) {
	m := rowDateRe.FindStringSubmatch(landed)
	if m == nil {
		return time.Time{}, false
	}
	month, _ := strconv.Atoi(m[1])
	day, _ := strconv.Atoi(m[2])
	year, _ := strconv.Atoi(m[3])
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

// checkDay reports whether the row's date matches the date asked for, or
// "" when it does. Relative words resolve against today in the owner's
// zone rather than the box's, which runs UTC and can be a day ahead for
// hours every evening. An unrecognised form is not evidence of a wrong day.
func (c *Client) checkDay(landed string, when time.Time) string {
	now := c.now().In(c.owner)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.owner)
	var got time.Time
	switch {
	case strings.Contains(landed, "Today"):
		got = today
	case strings.Contains(landed, "Tomorrow"):
		got = today.AddDate(0, 0, 1)
	default:
		var ok bool
		got, ok = rowDate(landed)
		if !ok {
			return ""
		}
		got = time.Date(got.Year(), got.Month(), got.Day(), 0, 0, 0, 0, today.Location())
	}
	want := time.Date(when.Year(), when.Month(), when.Day(), 0, 0, 0, 0, today.Location())
	if got.Equal(want) {
		return ""
	}
	return fmt.Sprintf("the row reads %q, which is %s, but %s was asked for",
		landed, got.Format("2006-01-02"), want.Format("2006-01-02"))
}

func evalJSONMap(tab *browser.Tab, expr string, arg any) (map[string]any, error) {
	raw, err := tab.Eval(expr, arg)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func evalText(tab *browser.Tab, expr string, arg any) (string, error) {
	return tab.EvalText(expr, arg)
}

func evalBool(tab *browser.Tab, expr string, arg any) bool {
	raw, err := tab.Eval(expr, arg)
	if err != nil {
		return false
	}
	var ok bool
	if err := json.Unmarshal(raw, &ok); err != nil {
		return false
	}
	return ok
}

type point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

func evalPoint(tab *browser.Tab, expr string, arg any) (point, string) {
	var out struct {
		point
		Error string `json:"error"`
	}
	if err := tab.EvalJSON(expr, arg, &out); err != nil {
		return point{}, shortError(err)
	}
	if out.Error != "" {
		return point{}, out.Error
	}
	return out.point, ""
}

// setTime ticks the Time checkbox and fills the hour/minute/AM-PM
// segments. The typed value is read in the page's zone: the zone is fixed
// one layer down at page load, so no conversion belongs here.
func (c *Client) setTime(tab *browser.Tab, hour, minute int, when time.Time) string {
	_ = when
	box := map[string]any{}
	if err := tab.EvalJSON(timeCheckbox, nil, &box); err != nil {
		return shortError(err)
	}
	if msg, bad := box["error"].(string); bad {
		return msg
	}
	if checked, _ := box["checked"].(bool); !checked {
		pt, msg := evalPoint(tab, timeCheckbox, nil)
		if msg != "" {
			return msg
		}
		if err := tab.MouseClick(pt.X, pt.Y); err != nil {
			return shortError(err)
		}
		sleep(1500)
	}
	seg, msg := evalPoint(tab, segment, "hour")
	if msg != "" {
		return msg
	}
	// Re-fetch the text: evalPoint only returns coordinates.
	hourText := segmentText(tab, "hour")
	_ = hourText
	if err := tab.MouseClick(seg.X, seg.Y); err != nil {
		return shortError(err)
	}
	sleep(600)
	h12 := hour % 12
	if h12 == 0 {
		h12 = 12
	}
	if err := tab.TypeText(fmt.Sprintf("%02d%02d", h12, minute)); err != nil {
		return shortError(err)
	}
	sleep(700)
	wantAMPM := "AM"
	if hour >= 12 {
		wantAMPM = "PM"
	}
	ampm, msg := evalPoint(tab, segment, "AM/PM")
	if msg == "" {
		if err := tab.MouseClick(ampm.X, ampm.Y); err == nil {
			sleep(500)
			_ = tab.TypeText(string(wantAMPM[0]))
			sleep(700)
			for i := 0; i < 2; i++ {
				segs := readTimeSegments(tab)
				if segs["AM/PM"] == wantAMPM {
					break
				}
				_ = tab.Key("ArrowUp", 0)
				sleep(600)
			}
		}
	}
	sleep(500)
	got := readTimeSegments(tab)
	hourOK := got["hour"] == strconv.Itoa(h12) || got["hour"] == fmt.Sprintf("%02d", h12)
	minuteOK := got["minute"] == strconv.Itoa(minute) || got["minute"] == fmt.Sprintf("%02d", minute)
	if !hourOK || !minuteOK || got["AM/PM"] != wantAMPM {
		return fmt.Sprintf("the time picker did not take %02d:%02d, it reads %v", hour, minute, got)
	}
	return ""
}

func segmentText(tab *browser.Tab, which string) string {
	var out struct {
		Text string `json:"text"`
	}
	if err := tab.EvalJSON(segment, which, &out); err != nil {
		return ""
	}
	return out.Text
}

func readTimeSegments(tab *browser.Tab) map[string]string {
	var out map[string]string
	if err := tab.EvalJSON(timeSegments, nil, &out); err != nil {
		return map[string]string{}
	}
	return out
}

// clearTime unticks the Time checkbox, so a dateless request lands as an
// all-day reminder rather than a midnight alarm.
func (c *Client) clearTime(tab *browser.Tab) string {
	box := map[string]any{}
	if err := tab.EvalJSON(timeCheckbox, nil, &box); err != nil {
		return shortError(err)
	}
	if msg, bad := box["error"].(string); bad {
		return msg
	}
	if checked, _ := box["checked"].(bool); !checked {
		return ""
	}
	pt, msg := evalPoint(tab, timeCheckbox, nil)
	if msg != "" {
		return msg
	}
	if err := tab.MouseClick(pt.X, pt.Y); err != nil {
		return shortError(err)
	}
	sleep(1500)
	after := map[string]any{}
	if err := tab.EvalJSON(timeCheckbox, nil, &after); err != nil {
		return shortError(err)
	}
	if checked, _ := after["checked"].(bool); checked {
		return "the time could not be cleared, so this would land as a midnight alarm"
	}
	return ""
}

var monthNames = []string{"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December"}

// setDue sets one reminder's due date, and its time when given.
func (c *Client) setDue(tab *browser.Tab, title string, when time.Time, at *dueTime) string {
	_ = tab.Key("Escape", 0) // a popover left open would be toggled shut instead
	sleep(1200)
	_, _ = tab.Eval(scrollEnd, nil) // the row may be below the fold, and so not rendered
	sleep(1200)

	var geo struct {
		Row   point  `json:"row"`
		Info  point  `json:"info"`
		Error string `json:"error"`
	}
	if err := tab.EvalJSON(rowGeo, title, &geo); err != nil {
		return shortError(err)
	}
	if geo.Error != "" {
		return geo.Error
	}
	if err := tab.MouseMove(geo.Row.X, geo.Row.Y); err != nil {
		return shortError(err)
	}
	sleep(700)
	if err := tab.MouseClick(geo.Info.X, geo.Info.Y); err != nil {
		return shortError(err)
	}
	sleep(2500)

	state, err := tab.EvalText(toggleDay, nil)
	if err != nil {
		return shortError(err)
	}
	if state != `"on"` && state != `"already on"` && state != "on" && state != "already on" {
		return fmt.Sprintf("could not enable the due date: %s", state)
	}
	sleep(2500)

	// Order matters: clearing after choosing re-derives the date from the
	// instant in another zone and moves it back a day. Clear first.
	if at == nil {
		if problem := c.clearTime(tab); problem != "" {
			return problem
		}
	}

	// Navigate by reading what is on screen, never by counting hops from
	// today: the picker opens where it was left, and the box clock is UTC.
	for i := 0; i < 30; i++ {
		var shown struct {
			Month string `json:"month"`
			Year  int    `json:"year"`
			Error string `json:"error"`
		}
		if err := tab.EvalJSON(calMonth, nil, &shown); err != nil {
			return shortError(err)
		}
		if shown.Error != "" {
			return fmt.Sprintf("could not read the calendar month: %s", shown.Error)
		}
		monthIdx := -1
		for m, name := range monthNames {
			if name == shown.Month {
				monthIdx = m + 1
			}
		}
		curYear, curMonth := shown.Year, monthIdx
		if curYear == when.Year() && curMonth == int(when.Month()) {
			break
		}
		step := nextMonth
		want := "advanced"
		if curYear > when.Year() || (curYear == when.Year() && curMonth > int(when.Month())) {
			step = prevMonth
			want = "moved"
		}
		moved, err := tab.EvalText(step, nil)
		if err != nil || (moved != want && moved != `"`+want+`"`) {
			return "could not move the calendar month"
		}
		sleep(900)
		if i == 29 {
			return fmt.Sprintf("the calendar would not reach %d-%02d", when.Year(), int(when.Month()))
		}
	}

	clicked, err := tab.EvalText(clickDay, dayLabel(when))
	if err != nil {
		return shortError(err)
	}
	if clicked != "selected" && clicked != `"selected"` {
		return clicked
	}
	sleep(1200)

	var seg map[string]string
	if err := tab.EvalJSON(segments, nil, &seg); err != nil {
		return shortError(err)
	}
	dayOK := seg["day"] == strconv.Itoa(when.Day()) || seg["day"] == fmt.Sprintf("%02d", when.Day())
	if !dayOK {
		return fmt.Sprintf("the picker did not take the date, it reads %v", seg)
	}

	if at != nil {
		if problem := c.setTime(tab, at.Hour, at.Minute, when); problem != "" {
			return problem
		}
	}

	var saved struct {
		point
		Error string `json:"error"`
	}
	if err := tab.EvalJSON(saveBtn, nil, &saved); err != nil {
		return shortError(err)
	}
	if saved.Error != "" {
		return "the date was selected but not committed: Save was not found. Dismissing the popover does not commit, so nothing was changed."
	}
	// A pointer event, not a synthetic click: Save is a ui-button custom
	// element and a synthetic click reported success while leaving the
	// popover open.
	if err := tab.MouseClick(saved.X, saved.Y); err != nil {
		return shortError(err)
	}
	sleep(3500)

	for i := 0; i < 6; i++ {
		if !evalBool(tab, popoverOpen, nil) {
			return ""
		}
		_ = tab.Key("Escape", 0)
		sleep(1000)
	}
	return "the date was saved but the popover would not close, so the app was left mid-edit"
}

type dueTime struct {
	Hour   int
	Minute int
}

// parseDue parses "YYYY-MM-DD" or "YYYY-MM-DD HH:MM" on a 24-hour clock.
func parseDue(due string) (time.Time, *dueTime, map[string]any) {
	raw := strings.ReplaceAll(strings.TrimSpace(due), "T", " ")
	parts := strings.Fields(raw)
	when, err := time.Parse("2006-01-02", parts[0])
	if err != nil {
		return time.Time{}, nil, map[string]any{"error": fmt.Sprintf("due must start with a date as YYYY-MM-DD, got '%s'", due)}
	}
	if len(parts) == 1 {
		return when, nil, nil
	}
	hm := strings.Split(parts[1], ":")
	if len(hm) < 2 {
		return time.Time{}, nil, map[string]any{"error": fmt.Sprintf("the time in due must be HH:MM on a 24-hour clock, got '%s'", due)}
	}
	hh, err1 := strconv.Atoi(hm[0])
	mm, err2 := strconv.Atoi(hm[1])
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return time.Time{}, nil, map[string]any{"error": fmt.Sprintf("the time in due must be HH:MM on a 24-hour clock, got '%s'", due)}
	}
	return when, &dueTime{Hour: hh, Minute: mm}, nil
}

// Complete ticks a reminder off. The match must be unique; the result is
// read back from the list rather than reported from a click. A repeating
// reminder rolls to its next occurrence instead of disappearing.
func (c *Client) Complete(ctx context.Context, title, listName string) (map[string]any, error) {
	return c.withApp(ctx, func(tab *browser.Tab) (map[string]any, error) {
		if problem := c.openList(tab, listName); problem != "" {
			return map[string]any{"error": problem}, nil
		}
		before, err := c.openItems(tab)
		if err != nil {
			return nil, err
		}
		var spot struct {
			Title      string   `json:"title"`
			X          int      `json:"x"`
			Y          int      `json:"y"`
			Error      string   `json:"error"`
			Candidates []string `json:"candidates"`
		}
		if err := tab.EvalJSON(completeGeo, title, &spot); err != nil {
			return nil, err
		}
		if spot.Error != "" {
			out := map[string]any{"error": spot.Error, "list": listName}
			if len(spot.Candidates) > 0 {
				out["candidates"] = spot.Candidates
			} else if len(before) == 0 {
				out["open_reminders"] = []string{}
			} else {
				names := []string{}
				for t := range before {
					names = append(names, t)
				}
				sort.Strings(names)
				if len(names) > 20 {
					names = names[:20]
				}
				out["open_reminders"] = names
			}
			return out, nil
		}
		target := spot.Title
		wasDue := ""
		if r, ok := before[target]; ok {
			if d, _ := r["due"].(string); d != "" {
				wasDue = d
			}
		}
		if err := tab.MouseClick(spot.X, spot.Y); err != nil {
			return nil, err
		}
		sleep(3000)
		after, err := c.openItems(tab)
		if err != nil {
			return nil, err
		}
		var wasDueOut any
		if wasDue != "" {
			wasDueOut = wasDue
		}
		if _, still := after[target]; !still {
			return map[string]any{"completed": true, "list": listName, "title": target,
				"was_due": wasDueOut, "repeated": false,
				"open_before": len(before), "open_after": len(after)}, nil
		}
		nowDue, _ := after[target]["due"].(string)
		if wasDue != "" && nowDue != "" && nowDue != wasDue {
			return map[string]any{"completed": true, "list": listName, "title": target,
				"was_due": wasDueOut, "repeated": true, "next_due": nowDue,
				"note": "this reminder repeats, so it stays on the list with a new date"}, nil
		}
		return map[string]any{"completed": false, "list": listName, "title": target,
			"error":   "the reminder is still open after clicking its completion control",
			"was_due": wasDueOut}, nil
	})
}

func (c *Client) openItems(tab *browser.Tab) (map[string]map[string]any, error) {
	all, err := c.items(tab)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	for _, r := range all {
		if completed, _ := r["completed"].(bool); !completed {
			if t, _ := r["title"].(string); t != "" {
				out[t] = r
			}
		}
	}
	return out, nil
}

// Create adds a reminder, optionally with a due date and time. The title
// is verified in the field before committing, and the row is read back
// from the list afterwards; a write that lands nowhere is never claimed.
func (c *Client) Create(ctx context.Context, title, listName, due string) (map[string]any, error) {
	if strings.TrimSpace(title) == "" {
		return map[string]any{"error": "a reminder needs a title"}, nil
	}
	var when time.Time
	var at *dueTime
	if strings.TrimSpace(due) != "" {
		var errMap map[string]any
		when, at, errMap = parseDue(due)
		if errMap != nil {
			return errMap, nil
		}
		return c.createWithDue(ctx, title, listName, due, when, at)
	}
	return c.createWithDue(ctx, title, listName, due, time.Time{}, nil)
}

func (c *Client) createWithDue(ctx context.Context, title, listName, due string, when time.Time, at *dueTime) (map[string]any, error) {
	_ = due
	return c.withApp(ctx, func(tab *browser.Tab) (map[string]any, error) {
		if problem := c.openList(tab, listName); problem != "" {
			return map[string]any{"error": problem}, nil
		}
		for i := 0; i < 6; i++ {
			if !evalBool(tab, popoverOpen, nil) {
				break
			}
			_ = tab.Key("Escape", 0)
			sleep(1000)
		}
		var after []string
		typed := false
		for attempt := 0; attempt < 4; attempt++ {
			ok, err := tab.Click("rm-new-reminder", 0)
			if err != nil {
				return nil, err
			}
			if !ok {
				return map[string]any{"error": "could not find the new-reminder control"}, nil
			}
			sleep(1800)
			_, _ = tab.Eval(scrollEnd, nil)
			sleep(1200)
			var spot struct {
				X     int    `json:"x"`
				Y     int    `json:"y"`
				Error string `json:"error"`
			}
			if err := tab.EvalJSON(focusNewRow, nil, &spot); err != nil {
				return nil, err
			}
			if spot.Error != "" {
				if attempt == 0 {
					continue
				}
				return map[string]any{"error": "the new row did not appear: " + spot.Error}, nil
			}
			focused := false
			for i := 0; i < 4; i++ {
				if err := tab.MouseClick(spot.X, spot.Y); err != nil {
					return nil, err
				}
				sleep(900)
				var finfo struct {
					Focused bool `json:"focused"`
				}
				if err := tab.EvalJSON(focusedTextJS, nil, &finfo); err != nil {
					return nil, err
				}
				if finfo.Focused {
					focused = true
					break
				}
				if err := tab.EvalJSON(focusNewRow, nil, &spot); err != nil {
					return nil, err
				}
				if spot.Error != "" {
					break
				}
			}
			if spot.Error != "" {
				// let the outer attempt loop try again from the add control
				var fcheck struct {
					Focused bool `json:"focused"`
				}
				if err := tab.EvalJSON(focusedTextJS, nil, &fcheck); err != nil {
					return nil, err
				}
				_ = fcheck
				continue
			}
			if !focused {
				continue
			}
			if err := tab.TypeText(title); err != nil {
				return nil, err
			}
			sleep(500)
			held := ""
			var heldInfo struct {
				Text string `json:"text"`
			}
			if err := tab.EvalJSON(focusedTextJS, nil, &heldInfo); err == nil {
				held = heldInfo.Text
			}
			if !strings.Contains(held, strings.TrimSpace(title)) {
				_ = tab.Key("Escape", 0)
				sleep(800)
				continue
			}
			// Tab, not Enter: Enter commits the row and opens another,
			// leaving a stray "New Reminder" behind every time.
			_ = tab.Key("Tab", 0)
			sleep(4000)
			_, _ = tab.Eval(scrollEnd, nil)
			sleep(1500)
			all, err := c.items(tab)
			if err != nil {
				return nil, err
			}
			after = nil
			for _, r := range all {
				if t, _ := r["title"].(string); t != "" {
					after = append(after, t)
				}
			}
			found := false
			for _, t := range after {
				if t == strings.TrimSpace(title) {
					found = true
				}
			}
			if found {
				typed = true
				break
			}
			_ = tab.DismissAlert()
			_ = tab.Key("Escape", 0)
			sleep(1000)
		}
		if !typed {
			ten := after
			if len(ten) > 10 {
				ten = ten[:10]
			}
			return map[string]any{"error": "the reminder was typed but did not appear in the list; nothing is claimed as created",
				"list_now": ten}, nil
		}
		result := map[string]any{"created": true, "list": listName, "title": strings.TrimSpace(title)}
		if !when.IsZero() {
			problem := c.setDue(tab, strings.TrimSpace(title), when, at)
			landed := ""
			if all, err := c.items(tab); err == nil {
				for _, r := range all {
					if t, _ := r["title"].(string); t == strings.TrimSpace(title) {
						landed, _ = r["due"].(string)
					}
				}
			}
			// Apple's first save of a date-only due lands a day early
			// (local midnight rendered from UTC). A second save of the
			// same date is faithful, so save again rather than computing
			// an offset that would be wrong in another zone.
			if problem == "" && landed != "" {
				if rd, ok := rowDate(landed); !ok || !sameDay(rd, when) {
					// rowDate nil means relative/unknown form: only
					// re-save when it parses to a different day.
					if ok {
						problem = c.setDue(tab, strings.TrimSpace(title), when, at)
						if all, err := c.items(tab); err == nil {
							for _, r := range all {
								if t, _ := r["title"].(string); t == strings.TrimSpace(title) {
									landed, _ = r["due"].(string)
								}
							}
						}
					}
				}
			}
			if problem != "" || landed == "" {
				msg := problem
				if msg == "" {
					msg = "the due date did not appear on the row"
				}
				result["due_error"] = msg
				result["due"] = nil
			} else {
				result["due"] = landed
				result["note"] = "due is the row's own text, so report it exactly as it reads rather than the date that was requested. A time in it means an alarm at that minute; no time means all day."
				if at != nil {
					h12 := at.Hour % 12
					if h12 == 0 {
						h12 = 12
					}
					mer := "AM"
					if at.Hour >= 12 {
						mer = "PM"
					}
					want := map[string]bool{
						fmt.Sprintf("%d:%02d %s", h12, at.Minute, mer):   true,
						fmt.Sprintf("%02d:%02d %s", h12, at.Minute, mer): true,
					}
					if at.Hour >= 13 {
						want[fmt.Sprintf("%d:%02d", at.Hour, at.Minute)] = true
					}
					matched := false
					for w := range want {
						if strings.Contains(landed, w) {
							matched = true
						}
					}
					if !matched {
						result["due_error"] = fmt.Sprintf("the row reads %q but %02d:%02d %s was asked for, which the row should show as %d:%02d %s",
							landed, at.Hour, at.Minute, c.owner.String(), h12, at.Minute, mer)
					}
				}
				if dayProblem := c.checkDay(landed, when); dayProblem != "" {
					if _, exists := result["due_error"]; !exists {
						result["due_error"] = dayProblem
					}
				}
			}
		}
		return result, nil
	})
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

// queueIfLatched records a blocked write for the drain when the latch is
// set, returning nil when there is nothing to queue.
func (c *Client) queueIfLatched(kind string, params map[string]any) map[string]any {
	if !c.state.Blocked() {
		return nil
	}
	item := c.queue.Enqueue(kind, params, kind, 0, 0, "icloud-approval")
	if item == "" {
		return map[string]any{"completed": false, "created": false, "queued": false,
			"error":                 "iCloud is waiting for your approval and I could not even record this to run later, so it is not saved. Tell me again once you have approved.",
			"needs_device_approval": true}
	}
	return map[string]any{"completed": false, "created": false, "queued": true, "queue_id": item,
		"waiting_on":          "your approval of iCloud web access",
		"will_run":            "by itself, within ten minutes of you approving",
		"expires_after_hours": queue.MaxAgeHours}
}

// Handlers wires the five Reminders tools.
func Handlers(cfg *config.Config, ask func(ctx context.Context, question string) string) map[string]server.ToolHandlerFunc {
	_ = ask
	return map[string]server.ToolHandlerFunc{
		"reminder_lists": func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return runClient(cfg, func(c *Client) (map[string]any, error) {
				return c.Lists(ctx)
			})
		},
		"list_reminders": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			name, err := args.Str("list_name")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			return runClient(cfg, func(c *Client) (map[string]any, error) {
				return c.ListReminders(ctx, name)
			})
		},
		"completed_reminders": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			limit, err := args.Int("limit", 50)
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			opt, err := args.OptAll("list_name", "since", "until")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			listName, since, until := "", "", ""
			if opt[0] != nil {
				listName = *opt[0]
			}
			if opt[1] != nil {
				since = *opt[1]
			}
			if opt[2] != nil {
				until = *opt[2]
			}
			return runClient(cfg, func(c *Client) (map[string]any, error) {
				return c.Completed(ctx, listName, since, until, limit)
			})
		},
		"complete_reminder": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			title, err := args.Str("title")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			name, err := args.Str("list_name")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			return runClient(cfg, func(c *Client) (map[string]any, error) {
				out, err := c.Complete(ctx, title, name)
				if err != nil {
					return nil, err
				}
				if need, _ := out["needs_device_approval"].(bool); need {
					if queued := c.queueIfLatched("complete_reminder",
						map[string]any{"title": title, "list_name": name}); queued != nil {
						// queueIfLatched reports both flags; complete's
						// shape carries completed:false.
						queued["completed"] = false
						delete(queued, "created")
						return queued, nil
					}
					out["needs_device_approval"] = false
					out["detail"] = "not queued: this is a deferral rather than a block."
				}
				return out, nil
			})
		},
		"create_reminder": func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := mcpserver.RequestArgs(req)
			title, err := args.Str("title")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			name, err := args.Str("list_name")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			due, err := args.StrOr("due", "")
			if err != nil {
				return mcpserver.ErrorResult(err.Error())
			}
			return runClient(cfg, func(c *Client) (map[string]any, error) {
				out, err := c.Create(ctx, title, name, due)
				if err != nil {
					return nil, err
				}
				if need, _ := out["needs_device_approval"].(bool); need {
					if queued := c.queueIfLatched("create_reminder",
						map[string]any{"title": title, "list_name": name, "due": due}); queued != nil {
						queued["created"] = false
						delete(queued, "completed")
						return queued, nil
					}
					out["created"] = false
					out["queued"] = false
					out["needs_device_approval"] = false
					out["detail"] = "not queued: this is a deferral rather than a block."
				}
				return out, nil
			})
		},
	}
}

func runClient(cfg *config.Config, fn func(*Client) (map[string]any, error)) (*mcp.CallToolResult, error) {
	c, err := Dial(cfg)
	if err != nil {
		return mcpserver.ErrorResult(err.Error())
	}
	out, err := fn(c)
	if err != nil {
		if _, ok := err.(*browser.NeedsApprovalError); ok {
			return mcpserver.ResultJSON(map[string]any{"error": err.Error(), "needs_device_approval": true})
		}
		return mcp.NewToolResultError(shortError(err)), nil
	}
	return mcpserver.ResultJSON(out)
}

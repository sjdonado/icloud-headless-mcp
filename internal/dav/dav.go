// Package dav is calendar and contacts over CalDAV and CardDAV, and the
// confirmed calendar delete.
//
// Reminders are deliberately absent: the CalDAV Reminders store is a legacy
// dead end (writes succeed and are invisible everywhere), so reminders stay
// browser-backed. Never restore them here.
//
// This package takes no lock. DAV needs none, which is why a Notes call
// cannot stall a calendar read even though both run in one process.
package dav

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
)

const (
	caldavEndpoint  = "https://caldav.icloud.com"
	carddavEndpoint = "https://contacts.icloud.com"
)

// Client speaks CalDAV and CardDAV for one account. A Client is built per
// call, mirroring the Python module, which dials and discovers on every
// tool call. Ask is the elicitation seam: production wires it to the MCP
// approval gate, tests script it.
type Client struct {
	cfg   *config.Config
	owner *time.Location
	http  *http.Client
	now   func() time.Time
	Ask   func(ctx context.Context, question string) string
	// Endpoints default to iCloud; tests point them at a fake server.
	caldavEndpoint  string
	carddavEndpoint string
}

// Dial validates the environment contract and resolves the owner's zone.
// It performs no network I/O; discovery happens per call.
func Dial(cfg *config.Config) (*Client, error) {
	tzName, err := cfg.LocalTimezone()
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %v", tzName, err)
	}
	return &Client{
		cfg:             cfg,
		owner:           loc,
		http:            &http.Client{Timeout: 60 * time.Second},
		now:             time.Now,
		caldavEndpoint:  caldavEndpoint,
		carddavEndpoint: carddavEndpoint,
	}, nil
}

func (c *Client) authed() webdav.HTTPClient {
	return webdav.HTTPClientWithBasicAuth(c.http, c.cfg.AppleID, c.cfg.AppPassword)
}

// calendars splits collections by what they hold, like the Python module:
// iCloud keeps events and todos apart, and only VEVENT calendars are kept.
// A calendar whose component set cannot be read is skipped, never fatal.
func (c *Client) calendars(ctx context.Context) (map[string]caldav.Calendar, error) {
	authed := c.authed()
	root, err := webdav.NewClient(authed, c.caldavEndpoint)
	if err != nil {
		return nil, err
	}
	principal, err := root.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	calc, err := caldav.NewClient(authed, c.caldavEndpoint)
	if err != nil {
		return nil, err
	}
	home, err := calc.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, err
	}
	all, err := calc.FindCalendars(ctx, home)
	if err != nil {
		return nil, err
	}
	events := make(map[string]caldav.Calendar, len(all))
	for _, cal := range all {
		for _, comp := range cal.SupportedComponentSet {
			if comp == "VEVENT" {
				events[cal.Name] = cal
				break
			}
		}
	}
	return events, nil
}

func (c *Client) calClient() (*caldav.Client, error) {
	return caldav.NewClient(c.authed(), c.caldavEndpoint)
}

// zone is the named zone to store an event in, defaulting to the owner's.
func (c *Client) zone(name string) (*time.Location, error) {
	if name == "" {
		return c.owner, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %v", name, err)
	}
	return loc, nil
}

var isoLayouts = []string{
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseTime accepts an ISO timestamp, naive or not, and returns it in a
// named zone. An offset-carrying value is converted, not passed through:
// asking for 22:00+02:00 must not store 22:00Z.
func (c *Client) parseTime(when, tz string) (time.Time, error) {
	zone, err := c.zone(tz)
	if err != nil {
		return time.Time{}, err
	}
	for _, layout := range isoLayouts {
		if strings.Contains(layout, "Z07:00") {
			if t, err := time.Parse(layout, when); err == nil {
				return t.In(zone), nil
			}
			continue
		}
		if t, err := time.ParseInLocation(layout, when, zone); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse ISO timestamp %q", when)
}

// parseDay is a YYYY-MM-DD string, or a clear error naming which argument
// was wrong.
func parseDay(value, field string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be YYYY-MM-DD, got %q: %v", field, value, err)
	}
	return t, nil
}

// defaultCalendar is the configured calendar, and only then whichever one
// sorts first. Callers guarantee events is non-empty; the empty case
// returns "" rather than panicking, and resolves to unknown-calendar.
func defaultCalendar(events map[string]caldav.Calendar, configured string) string {
	if _, ok := events[configured]; ok {
		return configured
	}
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// foundEvent is an event object with its calendar, like _find_event.
type foundEvent struct {
	calName string
	path    string
	data    *ical.Calendar
	event   *ical.Event
}

func firstEventUID(data *ical.Calendar, uid string) *ical.Event {
	evs := data.Events()
	for i := range evs {
		if eventProp(&evs[i], "UID") == uid {
			return &evs[i]
		}
	}
	return nil
}

// A window wide enough to mean "every event", used by the fallback lookup.
// CalDAV has no "give me everything" query, and an open-ended one is a
// per-server guess, so the range is stated.
func (c *Client) allTime() (time.Time, time.Time) {
	return time.Date(2000, 1, 1, 0, 0, 0, 0, c.owner),
		time.Date(2040, 1, 1, 0, 0, 0, 0, c.owner)
}

// findEvent mirrors _find_event, third attempt included. iCloud rejects the
// library's event_by_uid lookup, but stores this server's events at
// <calendar>/<uid>.ics: one request and about half a second. Events created
// elsewhere may live at a different href, so the fallback is a single
// windowed REPORT per calendar. Calendars are visited in sorted order so the
// lookup is deterministic.
func (c *Client) findEvent(ctx context.Context, events map[string]caldav.Calendar, uid string) (*foundEvent, error) {
	calc, err := c.calClient()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := strings.TrimSuffix(events[name].Path, "/") + "/" + uid + ".ics"
		obj, err := calc.GetCalendarObject(ctx, path)
		if err != nil || obj.Data == nil {
			continue
		}
		if ev := firstEventUID(obj.Data, uid); ev != nil {
			return &foundEvent{calName: name, path: obj.Path, data: obj.Data, event: ev}, nil
		}
	}
	lo, hi := c.allTime()
	for _, name := range names {
		objs, err := calc.QueryCalendar(ctx, events[name].Path, calendarQuery(lo, hi, false))
		if err != nil {
			continue
		}
		for i := range objs {
			if objs[i].Data == nil {
				continue
			}
			if ev := firstEventUID(objs[i].Data, uid); ev != nil {
				return &foundEvent{calName: name, path: objs[i].Path, data: objs[i].Data, event: ev}, nil
			}
		}
	}
	return nil, nil
}

func eventProp(ev *ical.Event, name string) string {
	if ev == nil {
		return ""
	}
	if p := ev.Props.Get(name); p != nil {
		return p.Value
	}
	return ""
}

func eventTime(ev *ical.Event, owner *time.Location, name string) (time.Time, bool) {
	if ev == nil {
		return time.Time{}, false
	}
	p := ev.Props.Get(name)
	if p == nil {
		return time.Time{}, false
	}
	t, err := p.DateTime(owner)
	if err != nil || t.IsZero() {
		return time.Time{}, false
	}
	return t, true
}

// fmtTime answers "when is this for the owner", the right default for
// reading a calendar.
func (c *Client) fmtTime(t time.Time) string {
	return t.In(c.owner).Format("2006-01-02T15:04Z07:00")
}

// localTime is the wall clock and zone the event was actually stored in.
// Converting everything to the owner's zone would make a foreign departure
// read the same whichever zone it was stored in, hiding the one mistake the
// stored_local field exists to catch.
func localTime(t time.Time, ok bool) any {
	if !ok {
		return nil
	}
	return fmt.Sprintf("%s (%s)", t.Format("2006-01-02T15:04Z07:00"), t.Location().String())
}

func calendarQuery(lo, hi time.Time, expand bool) *caldav.CalendarQuery {
	compReq := caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true}
	if expand {
		compReq.Expand = &caldav.CalendarExpandRequest{Start: lo, End: hi}
	}
	return &caldav.CalendarQuery{
		CompRequest: compReq,
		CompFilter: caldav.CompFilter{
			Name:  "VCALENDAR",
			Comps: []caldav.CompFilter{{Name: "VEVENT", Start: lo, End: hi}},
		},
	}
}

func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func newUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func setText(ev *ical.Event, name, value string) {
	ev.Props.Del(name)
	p := ical.NewProp(name)
	p.Value = value
	ev.Props.Set(p)
}

func buildEvent(uid, summary, location, description string, start, end time.Time, sequence int, now time.Time) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText("VERSION", "2.0")
	cal.Props.SetText("PRODID", "-//icloud-headless-mcp//EN")
	ev := ical.NewEvent()
	cal.Children = append(cal.Children, ev.Component)
	setText(ev, "UID", uid)
	setText(ev, "SUMMARY", summary)
	if location != "" {
		setText(ev, "LOCATION", location)
	}
	if description != "" {
		setText(ev, "DESCRIPTION", description)
	}
	ev.Props.Del("DTSTART")
	dtstart := ical.NewProp("DTSTART")
	dtstart.SetDateTime(start)
	ev.Props.Set(dtstart)
	ev.Props.Del("DTEND")
	dtend := ical.NewProp("DTEND")
	dtend.SetDateTime(end)
	ev.Props.Set(dtend)
	setText(ev, "SEQUENCE", strconv.Itoa(sequence))
	setText(ev, "DTSTAMP", now.UTC().Format("20060102T150405Z"))
	setText(ev, "LAST-MODIFIED", now.UTC().Format("20060102T150405Z"))
	return cal
}

// eventRow renders one event the way list_events rows read.
func (c *Client) eventRow(calName string, ev *ical.Event) map[string]any {
	start, end := "", ""
	if t, ok := eventTime(ev, c.owner, "DTSTART"); ok {
		start = c.fmtTime(t)
	}
	if t, ok := eventTime(ev, c.owner, "DTEND"); ok {
		end = c.fmtTime(t)
	}
	var location any
	if loc := eventProp(ev, "LOCATION"); loc != "" {
		location = loc
	}
	return map[string]any{
		"calendar": calName,
		"summary":  eventProp(ev, "SUMMARY"),
		"start":    start,
		"end":      end,
		"location": location,
		"uid":      eventProp(ev, "UID"),
	}
}

// ListCalendars lists the calendars this account exposes over CalDAV.
func (c *Client) ListCalendars(ctx context.Context) (map[string]any, error) {
	events, err := c.calendars(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	return map[string]any{
		"calendars": names,
		"timezone":  c.owner.String(),
		"note": "reminders are not here; use the reminder tools, which reach the " +
			"store the owner's devices actually use",
	}, nil
}

// ListEvents reads calendar events in a window, relative to today or
// between two inclusive YYYY-MM-DD dates. A date window is anchored to the
// owner's local midnight. The window is reported rather than assumed.
func (c *Client) ListEvents(ctx context.Context, daysAhead, daysBack int, calendar, start, end *string) (map[string]any, error) {
	events, err := c.calendars(ctx)
	if err != nil {
		return nil, err
	}
	now := c.now().In(c.owner)
	var lo, hi time.Time
	absolute := start != nil || end != nil
	if absolute {
		first := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.owner)
		if start != nil {
			if first, err = parseDay(*start, "start"); err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			first = time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, c.owner)
		}
		last := first
		if end != nil {
			var lastDay time.Time
			if lastDay, err = parseDay(*end, "end"); err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			last = time.Date(lastDay.Year(), lastDay.Month(), lastDay.Day(), 0, 0, 0, 0, c.owner)
		}
		if last.Before(first) {
			return map[string]any{"error": fmt.Sprintf("end %s is before start %s",
				last.Format("2006-01-02"), first.Format("2006-01-02"))}, nil
		}
		lo = first
		hi = last.AddDate(0, 0, 1)
	} else {
		lo, hi = now.AddDate(0, 0, -daysBack), now.AddDate(0, 0, daysAhead)
	}
	calc, err := c.calClient()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(events))
	for name := range events {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []map[string]any
	for _, name := range names {
		if calendar != nil && *calendar != name {
			continue
		}
		// expand matters: without it a recurring event returns its master
		// occurrence, so a weekly meeting reports the date it was first
		// created rather than the instance in this window.
		objs, err := calc.QueryCalendar(ctx, events[name].Path, calendarQuery(lo, hi, true))
		if err != nil {
			return nil, err
		}
		for i := range objs {
			if objs[i].Data == nil {
				continue
			}
			for _, ev := range objs[i].Data.Events() {
				ev := ev
				out = append(out, c.eventRow(name, &ev))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["start"].(string) < out[j]["start"].(string) })
	if out == nil {
		out = []map[string]any{}
	}
	return map[string]any{
		"count": len(out),
		"window": map[string]any{
			"start":    lo.Format("2006-01-02T15:04Z07:00"),
			"end":      hi.Format("2006-01-02T15:04Z07:00"),
			"absolute": absolute,
		},
		"events": out,
	}, nil
}

// CreateEvent creates a calendar event. Times are ISO 8601; naive values
// are read in the event's zone. The stored start is read back from the
// server rather than echoed.
func (c *Client) CreateEvent(ctx context.Context, summary, start string, end, calendar, location, description, timezone *string) (map[string]any, error) {
	events, err := c.calendars(ctx)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return map[string]any{"error": "no writable calendars"}, nil
	}
	name := defaultCalendar(events, c.cfg.DefaultCal)
	if calendar != nil {
		name = *calendar
	}
	cal, ok := events[name]
	if !ok {
		names := make([]string, 0, len(events))
		for n := range events {
			names = append(names, n)
		}
		sort.Strings(names)
		return map[string]any{"error": fmt.Sprintf("unknown calendar %s", name), "available": names}, nil
	}
	var tz string
	if timezone != nil {
		tz = *timezone
	}
	dtstart, err := c.parseTime(start, tz)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	dtend := dtstart.Add(time.Hour)
	if end != nil {
		if dtend, err = c.parseTime(*end, tz); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
	}
	var loc, desc string
	if location != nil {
		loc = *location
	}
	if description != nil {
		desc = *description
	}
	uid := newUID()
	data := buildEvent(uid, summary, loc, desc, dtstart, dtend, 0, c.now())
	calc, err := c.calClient()
	if err != nil {
		return nil, err
	}
	path := strings.TrimSuffix(cal.Path, "/") + "/" + uid + ".ics"
	if _, err := calc.PutCalendarObject(ctx, path, data); err != nil {
		return nil, err
	}
	stored, err := calc.GetCalendarObject(ctx, path)
	if err != nil {
		return nil, err
	}
	storedEv := firstEventUID(stored.Data, uid)
	var storedEnd, storedLocal any
	if t, ok := eventTime(storedEv, c.owner, "DTEND"); ok {
		storedEnd = c.fmtTime(t)
	}
	if t, ok := eventTime(storedEv, c.owner, "DTSTART"); ok {
		storedLocal = localTime(t, true)
	} else {
		storedLocal = nil
	}
	storedStart := ""
	if t, ok := eventTime(storedEv, c.owner, "DTSTART"); ok {
		storedStart = c.fmtTime(t)
	}
	return map[string]any{
		"created":      true,
		"calendar":     name,
		"uid":          eventProp(storedEv, "UID"),
		"summary":      eventProp(storedEv, "SUMMARY"),
		"stored_start": storedStart,
		"stored_end":   storedEnd,
		"stored_local": storedLocal,
	}, nil
}

// UpdateEvent changes an existing event in place: move it, rename it, or
// correct its details. Silent for an event only the owner is on; an event
// with other attendees asks first.
func (c *Client) UpdateEvent(ctx context.Context, uid string, summary, start, end, location, description, timezone *string) (map[string]any, error) {
	if summary == nil && start == nil && end == nil && location == nil && description == nil {
		return map[string]any{"error": "nothing to change: give at least one of summary, start, end, location or description"}, nil
	}
	events, err := c.calendars(ctx)
	if err != nil {
		return nil, err
	}
	found, err := c.findEvent(ctx, events, uid)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return map[string]any{"error": fmt.Sprintf("no event with uid %s", uid)}, nil
	}
	if found.event.Props == nil {
		return map[string]any{"error": fmt.Sprintf("event %s has no readable component", uid)}, nil
	}
	wasStart, wasEnd := "", ""
	if t, ok := eventTime(found.event, c.owner, "DTSTART"); ok {
		wasStart = c.fmtTime(t)
	}
	if t, ok := eventTime(found.event, c.owner, "DTEND"); ok {
		wasEnd = c.fmtTime(t)
	}
	was := map[string]any{"summary": eventProp(found.event, "SUMMARY"), "start": wasStart, "end": wasEnd}
	if attendees := found.event.Props.Values("ATTENDEE"); len(attendees) > 0 {
		noun := "attendee"
		if len(attendees) != 1 {
			noun = "attendees"
		}
		question := fmt.Sprintf("Change \u201c%s\u201d on your %s calendar? It has %d other %s, so they will see the change.",
			was["summary"], found.calName, len(attendees), noun)
		if c.Ask == nil {
			return map[string]any{"updated": false, "reason": "nobody was present to approve it, so nothing was done"}, nil
		}
		if refused := c.Ask(ctx, question); refused != "" {
			return map[string]any{"updated": false, "reason": refused}, nil
		}
	}
	var tz string
	if timezone != nil {
		tz = *timezone
	}
	if start != nil {
		t, err := c.parseTime(*start, tz)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		found.event.Props.Del("DTSTART")
		p := ical.NewProp("DTSTART")
		p.SetDateTime(t)
		found.event.Props.Set(p)
	}
	if end != nil {
		t, err := c.parseTime(*end, tz)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		found.event.Props.Del("DTEND")
		p := ical.NewProp("DTEND")
		p.SetDateTime(t)
		found.event.Props.Set(p)
	}
	for _, field := range []struct {
		prop string
		val  *string
	}{{"SUMMARY", summary}, {"LOCATION", location}, {"DESCRIPTION", description}} {
		if field.val != nil {
			setText(found.event, field.prop, *field.val)
		}
	}
	// A changed event that keeps its SEQUENCE is one other clients are
	// entitled to ignore: without bumping it a write can succeed on the
	// server and never reach the owner's phone.
	seq := 0
	if p := found.event.Props.Get("SEQUENCE"); p != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(p.Value)); err == nil {
			seq = n
		}
	}
	setText(found.event, "SEQUENCE", strconv.Itoa(seq+1))
	setText(found.event, "LAST-MODIFIED", c.now().UTC().Format("20060102T150405Z"))
	calc, err := c.calClient()
	if err != nil {
		return nil, err
	}
	if _, err := calc.PutCalendarObject(ctx, found.path, found.data); err != nil {
		return nil, err
	}
	storedEv := found.event
	if fresh, err := c.findEvent(ctx, events, uid); err == nil && fresh != nil {
		storedEv = fresh.event
	}
	storedStart, storedEnd := "", ""
	if t, ok := eventTime(storedEv, c.owner, "DTSTART"); ok {
		storedStart = c.fmtTime(t)
	}
	if t, ok := eventTime(storedEv, c.owner, "DTEND"); ok {
		storedEnd = c.fmtTime(t)
	}
	var storedLocal any
	if t, ok := eventTime(storedEv, c.owner, "DTSTART"); ok {
		storedLocal = localTime(t, true)
	}
	return map[string]any{
		"updated":      true,
		"calendar":     found.calName,
		"uid":          uid,
		"was":          was,
		"summary":      eventProp(storedEv, "SUMMARY"),
		"stored_start": storedStart,
		"stored_end":   storedEnd,
		"stored_local": storedLocal,
	}, nil
}

// DeleteEvent deletes a calendar event by uid. It asks the owner first and
// waits for the answer.
func (c *Client) DeleteEvent(ctx context.Context, uid string) (map[string]any, error) {
	events, err := c.calendars(ctx)
	if err != nil {
		return nil, err
	}
	found, err := c.findEvent(ctx, events, uid)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return map[string]any{"error": fmt.Sprintf("no event with uid %s", uid)}, nil
	}
	summary := eventProp(found.event, "SUMMARY")
	when := "at an unknown time"
	if t, ok := eventTime(found.event, c.owner, "DTSTART"); ok {
		when = c.fmtTime(t)
	}
	question := fmt.Sprintf("Delete \u201c%s\u201d from your %s calendar, starting %s?", summary, found.calName, when)
	if c.Ask == nil {
		return map[string]any{"deleted": false, "reason": "nobody was present to approve it, so nothing was done"}, nil
	}
	if refused := c.Ask(ctx, question); refused != "" {
		return map[string]any{"deleted": false, "reason": refused}, nil
	}
	calc, err := c.calClient()
	if err != nil {
		return nil, err
	}
	if err := calc.RemoveAll(ctx, found.path); err != nil {
		return nil, err
	}
	// The confirmation is the result: this server sends the owner nothing of
	// its own, so what the owner needs to know about a completed write is
	// here for the client to relay.
	return map[string]any{
		"deleted":        true,
		"calendar":       found.calName,
		"uid":            uid,
		"summary":        summary,
		"tell_the_owner": fmt.Sprintf("Deleted the event: %s", summary),
	}, nil
}

// SearchContacts finds contacts by name, email, or phone. Case-insensitive
// substring match over every address book.
func (c *Client) SearchContacts(ctx context.Context, query string, limit int) (map[string]any, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return map[string]any{"error": "empty query"}, nil
	}
	authed := c.authed()
	root, err := webdav.NewClient(authed, c.carddavEndpoint)
	if err != nil {
		return nil, err
	}
	principal, err := root.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	cardc, err := carddav.NewClient(authed, c.carddavEndpoint)
	if err != nil {
		return nil, err
	}
	home, err := cardc.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return nil, err
	}
	books, err := cardc.FindAddressBooks(ctx, home)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, book := range books {
		// No filter: the whole address book. Every card is matched locally
		// with headers decoded, the same posture as mail search.
		objs, err := cardc.QueryAddressBook(ctx, book.Path, &carddav.AddressBookQuery{
			DataRequest: carddav.AddressDataRequest{AllProp: true},
		})
		if err != nil {
			return nil, err
		}
		for i := range objs {
			card := objs[i].Card
			name := strings.TrimSpace(card.Value("FN"))
			emails := card.Values("EMAIL")
			phones := card.Values("TEL")
			for i := range emails {
				emails[i] = strings.TrimSpace(emails[i])
			}
			for i := range phones {
				phones[i] = strings.TrimSpace(phones[i])
			}
			haystack := strings.ToLower(strings.Join(append([]string{name}, append(emails, phones...)...), " "))
			if strings.Contains(haystack, q) {
				out = append(out, map[string]any{"name": name, "emails": emails, "phones": phones})
			}
			if len(out) >= limit {
				break
			}
		}
		if len(out) >= limit {
			break
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	return map[string]any{"count": len(out), "contacts": out}, nil
}

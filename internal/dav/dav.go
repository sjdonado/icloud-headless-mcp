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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
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
// call, dialing and discovering on every tool call. Ask is the elicitation
// seam: production wires it to the MCP approval gate, tests script it.
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

// calendars splits collections by what they hold: iCloud keeps events and
// todos apart, and only VEVENT calendars are kept. A calendar whose
// component set cannot be read is skipped, never fatal.
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
			if firstEventUID(objs[i].Data, uid) == nil {
				continue
			}
			// The query's copy carries only eventProps; an update written
			// from it would drop everything else, so fetch the whole object.
			full, err := calc.GetCalendarObject(ctx, objs[i].Path)
			if err != nil || full.Data == nil {
				return nil, fmt.Errorf("event %s was found but could not be read in full: %v", uid, err)
			}
			if ev := firstEventUID(full.Data, uid); ev != nil {
				return &foundEvent{calName: name, path: full.Path, data: full.Data, event: ev}, nil
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
	return config.ISOTime(t, c.owner)
}

// localTime is the wall clock and zone the event was actually stored in.
// Converting everything to the owner's zone would make a foreign departure
// read the same whichever zone it was stored in, hiding the one mistake the
// stored_local field exists to catch.
func localTime(t time.Time, ok bool) any {
	if !ok {
		return nil
	}
	return fmt.Sprintf("%s (%s)", t.Format(time.RFC3339), t.Location().String())
}

// eventProps are the VEVENT properties a calendar query asks for by name.
// iCloud answers allprop/allcomp with empty calendar data (verified live
// 2026-09-25: the object matched, its VCALENDAR had no children), so a
// listing asks for exactly what eventRow reads. Anything that rewrites an
// event must GET the whole object instead of trusting a query's copy.
var eventProps = []string{"UID", "SUMMARY", "DTSTART", "DTEND", "DURATION", "LOCATION",
	"DESCRIPTION", "RRULE", "RECURRENCE-ID", "STATUS", "ORGANIZER", "ATTENDEE",
	"URL", "CREATED", "LAST-MODIFIED"}

func calendarQuery(lo, hi time.Time, expand bool) *caldav.CalendarQuery {
	alarms := caldav.CalendarCompRequest{Name: "VALARM", Props: []string{"ACTION", "TRIGGER"}}
	compReq := caldav.CalendarCompRequest{Name: "VCALENDAR", Props: []string{"VERSION"},
		Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", Props: eventProps,
			Comps: []caldav.CalendarCompRequest{alarms}}}}
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

// eventRow renders one event the way list_events rows read. Every field
// comes from the props the query already asked for; a field the event does
// not carry is omitted, never guessed. rules maps a series uid to its RRULE
// for expanded instances, which carry RECURRENCE-ID but no rule.
func (c *Client) eventRow(calName string, ev *ical.Event, rules map[string]string) map[string]any {
	row := map[string]any{
		"uid":       eventProp(ev, "UID"),
		"calendars": []string{calName},
		"summary":   eventText(ev, "SUMMARY"),
		"start":     "",
		"end":       "",
		"all_day":   false,
	}
	allDay := isDate(ev.Props.Get("DTSTART"))
	if t, ok := eventTime(ev, c.owner, "DTSTART"); ok {
		if allDay {
			row["all_day"] = true
			row["start"] = config.ISODate(t)
		} else {
			row["start"] = c.fmtTime(t)
		}
	}
	if t, err := ev.DateTimeEnd(c.owner); err == nil && !t.IsZero() {
		if allDay && ev.Props.Get("DTEND") == nil {
			// DURATION on a date counts days, not 24h spans: across a DST
			// change start.Add(72h) lands on the previous evening.
			if st, ok := eventTime(ev, c.owner, "DTSTART"); ok {
				// Neither DTEND nor DURATION is a one-day event (RFC 5545);
				// go-ical's start+24h already says so, and a nil DURATION
				// would panic here.
				if p := ev.Props.Get("DURATION"); p != nil {
					if d, err := p.Duration(); err == nil {
						t = st.AddDate(0, 0, int(math.Round(d.Hours()/24)))
					}
				}
			}
		}
		if allDay {
			// iCalendar's DTEND is exclusive; the last day is what a
			// reader means by the end of an all-day event.
			end := config.ISODate(t.AddDate(0, 0, -1))
			if start, _ := row["start"].(string); end < start {
				end = start
			}
			row["end"] = end
		} else {
			row["end"] = c.fmtTime(t)
		}
	} else if allDay {
		row["end"] = row["start"]
	}
	for key, prop := range map[string]string{"location": "LOCATION", "description": "DESCRIPTION", "url": "URL"} {
		if v := eventText(ev, prop); v != "" {
			row[key] = v
		}
	}
	if v := eventProp(ev, "STATUS"); v != "" {
		row["status"] = strings.ToLower(v)
	}
	if p := ev.Props.Get("ORGANIZER"); p != nil {
		row["organizer"] = person(p)
	}
	if props := ev.Props.Values("ATTENDEE"); len(props) > 0 {
		attendees := make([]map[string]any, 0, len(props))
		for i := range props {
			a := person(&props[i])
			a["role"] = strings.ToLower(paramOr(&props[i], "ROLE", "REQ-PARTICIPANT"))
			a["participation"] = strings.ToLower(paramOr(&props[i], "PARTSTAT", "NEEDS-ACTION"))
			attendees = append(attendees, a)
		}
		row["attendees"] = attendees
	}
	rule := eventProp(ev, "RRULE")
	if ev.Props.Get("RECURRENCE-ID") != nil {
		row["recurring_instance"] = true
		if rule == "" {
			rule = rules[eventProp(ev, "UID")]
		}
	}
	if rule != "" {
		row["recurrence_rule"] = rule
	}
	if alarms := c.alarms(ev); len(alarms) > 0 {
		row["alarms"] = alarms
	}
	for key, prop := range map[string]string{"created": "CREATED", "last_modified": "LAST-MODIFIED"} {
		if t, ok := eventTime(ev, c.owner, prop); ok {
			row[key] = c.fmtTime(t)
		}
	}
	return row
}

// eventText is a TEXT property unescaped (newlines, commas and semicolons decoded), which
// eventProp's raw value is not.
func eventText(ev *ical.Event, name string) string {
	p := ev.Props.Get(name)
	if p == nil {
		return ""
	}
	// Text() keeps only the first comma-separated item, which cuts an
	// unescaped "snacks, drinks" to "snacks"; rejoin the list instead.
	if l, err := p.TextList(); err == nil {
		return strings.TrimSpace(strings.Join(l, ","))
	}
	return strings.TrimSpace(p.Value)
}

func isDate(p *ical.Prop) bool {
	return p != nil && p.ValueType() == ical.ValueDate
}

func paramOr(p *ical.Prop, name, def string) string {
	if v := p.Params.Get(name); v != "" {
		return v
	}
	return def
}

// person is an ORGANIZER or ATTENDEE as {name, address}.
func person(p *ical.Prop) map[string]any {
	addr := p.Value
	if len(addr) >= 7 && strings.EqualFold(addr[:7], "mailto:") {
		addr = addr[7:]
	}
	out := map[string]any{"address": addr}
	if cn := p.Params.Get(ical.ParamCommonName); cn != "" {
		out["name"] = cn
	}
	return out
}

// alarms lists the event's VALARMs: a relative trigger as its ISO 8601
// duration from the start (negative is before; related: end when anchored
// to the end), an absolute one as a time.
func (c *Client) alarms(ev *ical.Event) []map[string]any {
	var out []map[string]any
	for _, child := range ev.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		trig := child.Props.Get("TRIGGER")
		if trig == nil {
			continue
		}
		a := map[string]any{}
		if action := child.Props.Get("ACTION"); action != nil {
			a["action"] = strings.ToLower(action.Value)
		}
		if trig.ValueType() == ical.ValueDateTime {
			if t, err := trig.DateTime(c.owner); err == nil {
				a["at"] = c.fmtTime(t)
			}
		} else {
			a["offset"] = trig.Value
			if strings.EqualFold(trig.Params.Get("RELATED"), "END") {
				a["related"] = "end"
			}
		}
		out = append(out, a)
	}
	return out
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
	seen := map[string]int{}
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
		var evs []ical.Event
		recurring := false
		for i := range objs {
			if objs[i].Data == nil {
				continue
			}
			for _, ev := range objs[i].Data.Events() {
				evs = append(evs, ev)
				if ev.Props.Get("RECURRENCE-ID") != nil && ev.Props.Get("RRULE") == nil {
					recurring = true
				}
			}
		}
		// Expanded instances drop RRULE (RFC 4791 9.6.5), so the series
		// rule comes from one unexpanded query of the same window, only
		// when an instance needs it.
		rules := map[string]string{}
		if recurring {
			masters, err := calc.QueryCalendar(ctx, events[name].Path, calendarQuery(lo, hi, false))
			if err != nil {
				return nil, err
			}
			for i := range masters {
				if masters[i].Data == nil {
					continue
				}
				for _, ev := range masters[i].Data.Events() {
					if rule := eventProp(&ev, "RRULE"); rule != "" {
						rules[eventProp(&ev, "UID")] = rule
					}
				}
			}
		}
		for i := range evs {
			row := c.eventRow(name, &evs[i], rules)
			// One entry per event: the same uid and occurrence under a
			// shared and an own calendar is one meeting.
			key := row["uid"].(string) + "|" + eventProp(&evs[i], "RECURRENCE-ID")
			if j, ok := seen[key]; ok && row["uid"] != "" {
				out[j]["calendars"] = append(out[j]["calendars"].([]string), name)
				continue
			}
			seen[key] = len(out)
			out = append(out, row)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["start"].(string) < out[j]["start"].(string) })
	if out == nil {
		out = []map[string]any{}
	}
	return map[string]any{
		"count": len(out),
		"window": map[string]any{
			"start":    c.fmtTime(lo),
			"end":      c.fmtTime(hi),
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
			return map[string]any{"updated": false, "reason": "nobody was present to approve it, so nothing was done", "error": "nobody was present to approve it, so nothing was done"}, nil
		}
		if refused := c.Ask(ctx, question); refused != "" {
			return map[string]any{"updated": false, "reason": refused, "error": refused}, nil
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
		return map[string]any{"deleted": false, "reason": "nobody was present to approve it, so nothing was done", "error": "nobody was present to approve it, so nothing was done"}, nil
	}
	if refused := c.Ask(ctx, question); refused != "" {
		return map[string]any{"deleted": false, "reason": refused, "error": refused}, nil
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
			// go-webdav fails the whole REPORT on one malformed card,
			// so fall back to per-card fetches that skip what does not
			// parse. The fallback fails transport errors itself, so the
			// original failure is reported, never masked as empty.
			qerr := err
			objs, err = queryCardsIndividually(ctx, c, cardc, book.Path)
			if err != nil {
				return nil, fmt.Errorf("search_contacts: address-book query failed: %v; fallback failed: %v", qerr, err)
			}
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
				out = append(out, contactRow(card))
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

// contactRow is one card with every field an agent acts on. Empty fields
// are omitted, never guessed.
func contactRow(card vcard.Card) map[string]any {
	row := map[string]any{
		"name":   strings.TrimSpace(card.Value(vcard.FieldFormattedName)),
		"emails": typedValues(card, vcard.FieldEmail),
		"phones": typedValues(card, vcard.FieldTelephone),
	}
	for key, field := range map[string]string{"nickname": vcard.FieldNickname, "job_title": vcard.FieldTitle, "notes": vcard.FieldNote} {
		if v := strings.TrimSpace(card.Value(field)); v != "" {
			row[key] = v
		}
	}
	// ORG is "Company;Department"; the company is what a reader means.
	if org, _, _ := strings.Cut(card.Value(vcard.FieldOrganization), ";"); strings.TrimSpace(org) != "" {
		row["organization"] = strings.TrimSpace(org)
	}
	if b := birthday(card.Value(vcard.FieldBirthday)); b != "" {
		row["birthday"] = b
	}
	if urls := card.Values(vcard.FieldURL); len(urls) > 0 {
		row["urls"] = urls
	}
	var addrs []map[string]any
	for _, a := range card.Addresses() {
		addr := map[string]any{"type": fieldType(card, a.Field)}
		for key, v := range map[string]string{"street": a.StreetAddress, "extended": a.ExtendedAddress,
			"po_box": a.PostOfficeBox, "city": a.Locality, "region": a.Region,
			"postal_code": a.PostalCode, "country": a.Country} {
			if v = strings.TrimSpace(v); v != "" {
				addr[key] = v
			}
		}
		addrs = append(addrs, addr)
	}
	if len(addrs) > 0 {
		row["addresses"] = addrs
	}
	return row
}

// typedValues is every value of a field as {value, type}.
func typedValues(card vcard.Card, key string) []map[string]any {
	out := []map[string]any{}
	for _, f := range card[key] {
		if v := strings.TrimSpace(f.Value); v != "" {
			out = append(out, map[string]any{"value": v, "type": fieldType(card, f)})
		}
	}
	return out
}

// fieldType is a field's label: Apple's custom X-ABLabel for its group when
// set, else the TYPE parameters without the ones that carry no meaning to a
// reader (internet, pref, voice). "" when there is none.
func fieldType(card vcard.Card, f *vcard.Field) string {
	if f.Group != "" {
		for _, label := range card["X-ABLABEL"] {
			if label.Group == f.Group {
				return strings.ToLower(strings.Trim(strings.TrimSuffix(strings.TrimPrefix(label.Value, "_$!<"), ">!$_"), " "))
			}
		}
	}
	var types []string
	for _, t := range f.Params.Types() {
		switch t = strings.ToLower(t); t {
		case "internet", "pref", "voice", "x400":
		default:
			types = append(types, t)
		}
	}
	return strings.Join(types, ",")
}

// birthday normalizes BDAY to YYYY-MM-DD, or --MM-DD when the card holds
// no year (vCard 4's form, and Apple's 1604 placeholder year).
func birthday(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if day, _, ok := strings.Cut(v, "T"); ok {
		v = day
	}
	digits := strings.ReplaceAll(v, "-", "")
	switch {
	case strings.HasPrefix(v, "--") && len(digits) == 4:
		return "--" + digits[:2] + "-" + digits[2:]
	case len(digits) == 8 && strings.HasPrefix(digits, "1604"):
		return "--" + digits[4:6] + "-" + digits[6:]
	case len(digits) == 8:
		return digits[:4] + "-" + digits[4:6] + "-" + digits[6:]
	}
	return v
}

// queryCardsIndividually lists a book's cards and fetches each one alone,
// skipping what does not parse. Fallback for QueryAddressBook, which fails
// the whole book on one malformed card. Only unparseable cards are
// skipped; anything else failing is returned.
func queryCardsIndividually(ctx context.Context, c *Client, cardc *carddav.Client, book string) ([]carddav.AddressObject, error) {
	var hrefs []string
	token := ""
	for {
		syncResp, err := cardc.SyncCollection(ctx, book, &carddav.SyncQuery{SyncToken: token})
		if err != nil {
			return nil, err
		}
		if token != "" && syncResp.SyncToken == token {
			break // no progress: the same page again
		}
		for _, upd := range syncResp.Updated {
			if strings.HasSuffix(upd.Path, ".vcf") {
				hrefs = append(hrefs, upd.Path)
			}
		}
		if syncResp.SyncToken == "" || len(syncResp.Updated) == 0 {
			break
		}
		token = syncResp.SyncToken
	}
	var objs []carddav.AddressObject
	for _, href := range hrefs {
		ao, skip, err := getCard(ctx, c, href)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		objs = append(objs, *ao)
	}
	return objs, nil
}

// getCard fetches one card by href. A card that does not parse reports
// skip; anything else failing, including a cancelled context, reports the
// error. The body is decoded here rather than through GetAddressObject so
// a MIME quirk or a metadata error never drops a readable card.
func getCard(ctx context.Context, c *Client, href string) (ao *carddav.AddressObject, skip bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.carddavEndpoint+href, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.authed().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("GET %s: %s", href, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	card, err := vcard.NewDecoder(bytes.NewReader(raw)).Decode()
	if err != nil {
		return nil, true, nil
	}
	return &carddav.AddressObject{Path: href, Card: card}, false, nil
}

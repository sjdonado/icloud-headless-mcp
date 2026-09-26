package dav

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
)

var fixedNow = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.FixedZone("CET", 3600))

func testClient(t *testing.T, fake *fakeDAV, srv string, ask func(ctx context.Context, question string) string) *Client {
	t.Helper()
	owner, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	fixedNow = fixedNow.In(owner)
	c, err := Dial(&config.Config{
		AppleID:     "test",
		AppPassword: "test",
		AgentTZ:     "Europe/Amsterdam",
		TZFile:      "/nonexistent-timezone-file",
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	c.caldavEndpoint = srv
	c.carddavEndpoint = srv
	c.now = func() time.Time { return fixedNow }
	c.Ask = ask
	return c
}

func approveAll(_ context.Context, _ string) string { return "" }

func failOnAsk(t *testing.T) func(context.Context, string) string {
	t.Helper()
	return func(_ context.Context, q string) string {
		t.Fatalf("unexpected approval prompt: %s", q)
		return "must not be called"
	}
}

func strptr(s string) *string { return &s }

func TestListCalendars(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, fake, srv.URL, failOnAsk(t))
	out, err := c.ListCalendars(context.Background())
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	cals, _ := out["calendars"].([]string)
	if len(cals) != 1 || cals[0] != "Personal" {
		t.Fatalf("calendars = %v, want [Personal] (VTODO-only store excluded)", cals)
	}
	if out["timezone"] != "Europe/Amsterdam" {
		t.Fatalf("timezone = %v", out["timezone"])
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "reminders are not here") {
		t.Fatalf("note = %q", note)
	}
}

func TestListEventsInstanceDate(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, fake, srv.URL, failOnAsk(t))
	out, err := c.ListEvents(context.Background(), 0, 0, nil, strptr("2026-03-09"), strptr("2026-03-09"))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	events, _ := out["events"].([]map[string]any)
	// Fake returns every stored event regardless of window; the point is the
	// recurring row carries the instance date, not the master's.
	var standup string
	starts := map[string]bool{}
	for _, e := range events {
		starts[e["start"].(string)] = true
		if e["summary"] == "Standup" {
			standup = e["start"].(string)
		}
	}
	if standup != "2026-03-09T09:00:00+01:00" {
		t.Fatalf("Standup start = %q, want the 03-09 instance, not the master", standup)
	}
	if len(events) != 4 {
		t.Fatalf("count = %d, want 4 (solo, attended, stray, instance)", len(events))
	}
	win, _ := out["window"].(map[string]any)
	if win["start"] != "2026-03-09T00:00:00+01:00" || win["absolute"] != true {
		t.Fatalf("window = %v", win)
	}
	// The client asked for expansion: the REPORT body carries <C:expand>.
	var sawExpand bool
	for _, body := range fake.reports[fakePersonal] {
		if strings.Contains(body, "expand") {
			sawExpand = true
		}
	}
	if !sawExpand {
		t.Fatal("no REPORT to Personal asked for expansion")
	}
}

func TestListEventsBadDate(t *testing.T) {
	_, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, nil, srv.URL, failOnAsk(t))
	out, err := c.ListEvents(context.Background(), 7, 0, nil, strptr("03/09/2026"), nil)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "start") || !strings.Contains(msg, "YYYY-MM-DD") {
		t.Fatalf("error = %q, want it to name start and the format", msg)
	}
}

func TestListEventsEndBeforeStart(t *testing.T) {
	_, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, nil, srv.URL, failOnAsk(t))
	out, err := c.ListEvents(context.Background(), 7, 0, nil, strptr("2026-03-10"), strptr("2026-03-09"))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "is before start") {
		t.Fatalf("error = %q", msg)
	}
}

func TestCreateEventConvertsOffset(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, fake, srv.URL, failOnAsk(t))
	// 22:00+02:00 is 21:00 in Amsterdam; storing the wall clock as UTC would
	// read 22:00Z instead.
	out, err := c.CreateEvent(context.Background(), "Call", "2026-03-10T22:00+02:00", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if out["stored_start"] != "2026-03-10T21:00:00+01:00" {
		t.Fatalf("stored_start = %v", out["stored_start"])
	}
	if out["stored_end"] != "2026-03-10T22:00:00+01:00" {
		t.Fatalf("stored_end = %v (want default one hour)", out["stored_end"])
	}
	if out["calendar"] != "Personal" || out["created"] != true {
		t.Fatalf("result = %v", out)
	}
	if local, _ := out["stored_local"].(string); !strings.Contains(local, "Europe/Amsterdam") {
		t.Fatalf("stored_local = %v, want the stored zone", out["stored_local"])
	}
	uid, _ := out["uid"].(string)
	put, ok := fake.puts[fakePersonal+uid+".ics"]
	if !ok {
		t.Fatalf("no PUT recorded for uid %s: %v", uid, fake.puts)
	}
	if !strings.Contains(put, "TZID=Europe/Amsterdam") {
		t.Fatalf("PUT body has no real TZID:\n%s", put)
	}
}

func TestCreateEventUnknownCalendar(t *testing.T) {
	_, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, nil, srv.URL, failOnAsk(t))
	out, err := c.CreateEvent(context.Background(), "X", "2026-03-10T10:00", nil, strptr("Nope"), nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "unknown calendar Nope") {
		t.Fatalf("error = %q", msg)
	}
	if avail, _ := out["available"].([]string); len(avail) != 1 || avail[0] != "Personal" {
		t.Fatalf("available = %v", out["available"])
	}
}

func TestUpdateSoloIsSilentAndInPlace(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, fake, srv.URL, failOnAsk(t))
	out, err := c.UpdateEvent(context.Background(), "solo-1", strptr("Dentist moved"), strptr("2026-03-10T15:30"), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if out["updated"] != true {
		t.Fatalf("result = %v", out)
	}
	was, _ := out["was"].(map[string]any)
	if was["summary"] != "Dentist" {
		t.Fatalf("was = %v", was)
	}
	put, ok := fake.puts[fakePersonal+"solo-1.ics"]
	if !ok {
		t.Fatalf("write did not land in place at solo-1.ics: %v", fake.puts)
	}
	if !strings.Contains(put, "SEQUENCE:3") {
		t.Fatalf("SEQUENCE not bumped from 2:\n%s", put)
	}
	if !strings.Contains(put, "SUMMARY:Dentist moved") {
		t.Fatalf("summary not updated:\n%s", put)
	}
}

func TestUpdateAttendedAsksWithCount(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	var question string
	c := testClient(t, fake, srv.URL, func(_ context.Context, q string) string {
		question = q
		return ""
	})
	out, err := c.UpdateEvent(context.Background(), "attend-1", strptr("Planning v2"), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if out["updated"] != true {
		t.Fatalf("result = %v", out)
	}
	if !strings.Contains(question, "2 other attendees") {
		t.Fatalf("question = %q, want the attendee count", question)
	}
}

func TestUpdateAttendedDeclineWritesNothing(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, fake, srv.URL, func(_ context.Context, _ string) string {
		return "not approved (decline)"
	})
	out, err := c.UpdateEvent(context.Background(), "attend-1", strptr("Planning v2"), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if out["updated"] != false {
		t.Fatalf("result = %v", out)
	}
	if _, wrote := fake.puts[fakePersonal+"attend-1.ics"]; wrote {
		t.Fatal("declined update still wrote")
	}
}

func TestUpdateNothingToChange(t *testing.T) {
	_, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, nil, srv.URL, failOnAsk(t))
	out, err := c.UpdateEvent(context.Background(), "solo-1", nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "nothing to change") {
		t.Fatalf("error = %q", msg)
	}
}

func TestUpdateUnknownUID(t *testing.T) {
	_, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, nil, srv.URL, failOnAsk(t))
	out, err := c.UpdateEvent(context.Background(), "missing-9", strptr("X"), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "no event with uid missing-9") {
		t.Fatalf("error = %q", msg)
	}
}

func TestFindFallbackStrayHref(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	// stray-1 lives at oddball.ics: the direct <uid>.ics GET 404s and only
	// the windowed search finds it.
	c := testClient(t, fake, srv.URL, failOnAsk(t))
	out, err := c.UpdateEvent(context.Background(), "stray-1", strptr("Found"), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if out["updated"] != true || out["calendar"] != "Personal" {
		t.Fatalf("result = %v", out)
	}
	if _, ok := fake.puts[fakePersonal+"oddball.ics"]; !ok {
		t.Fatalf("write did not land at the discovered href: %v", fake.puts)
	}
}

func TestDeleteFlow(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	var question string
	c := testClient(t, fake, srv.URL, func(_ context.Context, q string) string {
		question = q
		return ""
	})
	out, err := c.DeleteEvent(context.Background(), "solo-1")
	if err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	if out["deleted"] != true || out["tell_the_owner"] != "Deleted the event: Dentist" {
		t.Fatalf("result = %v", out)
	}
	if !strings.Contains(question, "Dentist") || !strings.Contains(question, "Personal") {
		t.Fatalf("question = %q", question)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != fakePersonal+"solo-1.ics" {
		t.Fatalf("deleted = %v", fake.deleted)
	}
	again, err := c.DeleteEvent(context.Background(), "solo-1")
	if err != nil {
		t.Fatalf("second DeleteEvent: %v", err)
	}
	if msg, _ := again["error"].(string); !strings.Contains(msg, "no event with uid solo-1") {
		t.Fatalf("second delete = %v", again)
	}
}

func TestDeleteDeclineWritesNothing(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, fake, srv.URL, func(_ context.Context, _ string) string {
		return "not approved (the answer was no)"
	})
	out, err := c.DeleteEvent(context.Background(), "solo-1")
	if err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	if out["deleted"] != false || out["error"] == nil {
		t.Fatalf("result = %v; a refusal must carry error so it reads as isError", out)
	}
	if len(fake.deleted) != 0 {
		t.Fatalf("declined delete still deleted: %v", fake.deleted)
	}
}

func TestSearchContacts(t *testing.T) {
	_, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, nil, srv.URL, failOnAsk(t))
	ctx := context.Background()
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"ada", []string{"Ada Lovelace"}},
		{"ADA", []string{"Ada Lovelace"}},
		{"navy", []string{"Grace Hopper"}},
		{"+3101", []string{"Ada Lovelace"}},
		{"hopper", []string{"Grace Hopper"}},
	} {
		out, err := c.SearchContacts(ctx, tc.query, 10)
		if err != nil {
			t.Fatalf("SearchContacts(%q): %v", tc.query, err)
		}
		contacts, _ := out["contacts"].([]map[string]any)
		if len(contacts) != len(tc.want) || contacts[0]["name"] != tc.want[0] {
			t.Fatalf("SearchContacts(%q) = %v, want %v", tc.query, contacts, tc.want)
		}
	}
	out, err := c.SearchContacts(ctx, "e", 1)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if out["count"] != 1 {
		t.Fatalf("limited search = %v", out)
	}
	out, err = c.SearchContacts(ctx, "zzz", 10)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if out["count"] != 0 {
		t.Fatalf("empty search = %v", out)
	}
	out, err = c.SearchContacts(ctx, "   ", 10)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if msg, _ := out["error"].(string); msg != "empty query" {
		t.Fatalf("blank query = %v", out)
	}
}

func TestSearchContactsSkipsBrokenCard(t *testing.T) {
	f, srv := newFakeDAV(t)
	defer srv.Close()
	f.mu.Lock()
	f.objects[fakeBook+"broken.vcf"] = fxBroken
	f.mu.Unlock()
	c := testClient(t, nil, srv.URL, failOnAsk(t))
	ctx := context.Background()
	// The broken card's own words match nothing: it is skipped, not fatal.
	out, err := c.SearchContacts(ctx, "sight", 10)
	if err != nil {
		t.Fatalf("SearchContacts with a broken card in the book: %v", err)
	}
	if out["count"] != 0 {
		t.Fatalf("broken card matched = %v", out)
	}
	// The readable cards still match through the per-card fallback.
	out, err = c.SearchContacts(ctx, "ada", 10)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	contacts, _ := out["contacts"].([]map[string]any)
	if len(contacts) != 1 || contacts[0]["name"] != "Ada Lovelace" {
		t.Fatalf("SearchContacts(ada) with a broken card present = %v", out)
	}
	// The fallback enumerates with sync-collection rather than re-issuing
	// the poisoning whole-book REPORT.
	synced := false
	for _, body := range f.reports[fakeBook] {
		if strings.Contains(body, "sync-collection") {
			synced = true
		}
	}
	if !synced {
		t.Fatalf("fallback never issued sync-collection, reports = %v", f.reports[fakeBook])
	}
}

func TestSearchContactsGetFailureSurfaces(t *testing.T) {
	f, srv := newFakeDAV(t)
	defer srv.Close()
	f.mu.Lock()
	f.objects[fakeBook+"broken.vcf"] = fxBroken
	f.failGET = map[string]int{fakeBook + "grace.vcf": http.StatusInternalServerError}
	f.mu.Unlock()
	c := testClient(t, nil, srv.URL, failOnAsk(t))
	// A card whose fetch fails is a transport failure, not a broken card:
	// it errors instead of reading as empty.
	if _, err := c.SearchContacts(context.Background(), "grace", 10); err == nil {
		t.Fatalf("SearchContacts with a 500 card fetch returned no error")
	}
}

func TestParseTime(t *testing.T) {
	_, srv := newFakeDAV(t)
	defer srv.Close()
	c := testClient(t, nil, srv.URL, failOnAsk(t))
	naive, err := c.parseTime("2026-03-10T14:30", "")
	if err != nil {
		t.Fatalf("parseTime naive: %v", err)
	}
	if got := naive.Format("2006-01-02T15:04Z07:00"); got != "2026-03-10T14:30+01:00" {
		t.Fatalf("naive parsed as %s, want owner-zone wall time", got)
	}
	offset, err := c.parseTime("2026-03-10T22:00+02:00", "")
	if err != nil {
		t.Fatalf("parseTime offset: %v", err)
	}
	if got := offset.Format("2006-01-02T15:04Z07:00"); got != "2026-03-10T21:00+01:00" {
		t.Fatalf("offset parsed as %s, want conversion to owner zone", got)
	}
	flight, err := c.parseTime("2027-01-07T00:55", "America/Bogota")
	if err != nil {
		t.Fatalf("parseTime zoned: %v", err)
	}
	if got := flight.Format("2006-01-02T15:04Z07:00"); got != "2027-01-07T00:55-05:00" {
		t.Fatalf("zoned parsed as %s", got)
	}
	if _, err := c.parseTime("2026-03-10T14:30", "Mars/Olympus"); err == nil {
		t.Fatal("unknown timezone accepted")
	} else if msg := err.Error(); !strings.Contains(msg, "unknown timezone") {
		t.Fatalf("error = %q", msg)
	}
}

// TestCalendarQueryNamesProps guards the iCloud empty-data regression:
// allprop/allcomp comes back as a VCALENDAR with no children there.
func TestCalendarQueryNamesProps(t *testing.T) {
	q := calendarQuery(time.Now(), time.Now().Add(time.Hour), true)
	if q.CompRequest.AllProps || q.CompRequest.AllComps {
		t.Fatal("calendar query uses allprop/allcomp, which iCloud answers with empty calendar data")
	}
	if len(q.CompRequest.Comps) != 1 || q.CompRequest.Comps[0].Name != "VEVENT" || len(q.CompRequest.Comps[0].Props) == 0 {
		t.Fatalf("calendar query must name VEVENT props, got %+v", q.CompRequest.Comps)
	}
	if q.CompRequest.Expand == nil {
		t.Fatal("listing queries must expand recurrences")
	}
}

const (
	fxMeeting = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:meet-1\r\nDTSTAMP:20260301T120000Z\r\n" +
		"DTSTART;TZID=Europe/Amsterdam:20260313T100000\r\nDURATION:PT45M\r\n" +
		"SUMMARY:Budget review\r\nLOCATION:Room 4\\, floor 2\r\n" +
		"DESCRIPTION:Agenda:\\n1. numbers\r\nSTATUS:CONFIRMED\r\nURL:https://example.com/m/1\r\n" +
		"CREATED:20260201T080000Z\r\nLAST-MODIFIED:20260228T160000Z\r\n" +
		"ORGANIZER;CN=Carol:mailto:carol@example.com\r\n" +
		"ATTENDEE;CN=Alice;ROLE=REQ-PARTICIPANT;PARTSTAT=ACCEPTED:mailto:alice@example.com\r\n" +
		"ATTENDEE;CN=Bob:mailto:bob@example.com\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\n" +
		"BEGIN:VALARM\r\nACTION:AUDIO\r\nTRIGGER;VALUE=DATE-TIME:20260313T080000Z\r\nEND:VALARM\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"

	fxHoliday = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:holiday-1\r\nDTSTAMP:20260301T120000Z\r\n" +
		"DTSTART;VALUE=DATE:20260314\r\nDTEND;VALUE=DATE:20260316\r\n" +
		"SUMMARY:Weekend away\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

	// Three days over the October DST change, as DURATION, with an
	// unescaped comma and an end-relative alarm.
	fxTrip = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:trip-1\r\nDTSTAMP:20260301T120000Z\r\n" +
		"DTSTART;VALUE=DATE:20261024\r\nDURATION:P3D\r\n" +
		"SUMMARY:Trip\r\nDESCRIPTION:Bring snacks, drinks and chairs\r\n" +
		"ORGANIZER:Mailto:dana@example.com\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER;RELATED=END:PT0S\r\nEND:VALARM\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"

	fxWeeklyMaster = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:weekly-1\r\nDTSTAMP:20260301T120000Z\r\n" +
		"DTSTART;TZID=Europe/Amsterdam:20260105T090000\r\n" +
		"DTEND;TZID=Europe/Amsterdam:20260105T093000\r\n" +
		"RRULE:FREQ=WEEKLY;BYDAY=MO\r\nSUMMARY:Standup\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
)

func eventsByUID(t *testing.T, out map[string]any) map[string]map[string]any {
	t.Helper()
	byUID := map[string]map[string]any{}
	events, _ := out["events"].([]map[string]any)
	for _, e := range events {
		if _, dup := byUID[e["uid"].(string)]; dup {
			t.Fatalf("uid %v listed twice", e["uid"])
		}
		byUID[e["uid"].(string)] = e
	}
	return byUID
}

func TestListEventsRichFields(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	fake.objects[fakePersonal+"meet-1.ics"] = fxMeeting
	fake.objects[fakePersonal+"holiday-1.ics"] = fxHoliday
	fake.objects[fakePersonal+"trip-1.ics"] = fxTrip
	fake.objects[fakePersonal+"day-1.ics"] = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:day-1\r\nDTSTAMP:20260301T120000Z\r\nDTSTART;VALUE=DATE:20260312\r\n" +
		"SUMMARY:No end\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	fake.unexpanded[fakePersonal+"weekly-9.ics"] = fxWeeklyMaster
	c := testClient(t, fake, srv.URL, failOnAsk(t))
	out, err := c.ListEvents(context.Background(), 0, 0, nil, strptr("2026-03-09"), strptr("2026-03-15"))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if err := mcpserver.CheckOutput("list_events", out); err != nil {
		t.Fatal(err)
	}
	byUID := eventsByUID(t, out)

	m := byUID["meet-1"]
	for key, want := range map[string]any{
		"start": "2026-03-13T10:00:00+01:00", "end": "2026-03-13T10:45:00+01:00", "all_day": false,
		"location": "Room 4, floor 2", "description": "Agenda:\n1. numbers", "status": "confirmed",
		"url": "https://example.com/m/1", "created": "2026-02-01T09:00:00+01:00",
		"last_modified": "2026-02-28T17:00:00+01:00",
	} {
		if m[key] != want {
			t.Errorf("meeting %s = %#v, want %#v", key, m[key], want)
		}
	}
	if org, _ := m["organizer"].(map[string]any); org["name"] != "Carol" || org["address"] != "carol@example.com" {
		t.Errorf("organizer = %v", m["organizer"])
	}
	att, _ := m["attendees"].([]map[string]any)
	if len(att) != 2 || att[0]["participation"] != "accepted" || att[1]["participation"] != "needs-action" ||
		att[1]["name"] != "Bob" || att[1]["role"] != "req-participant" {
		t.Errorf("attendees = %v", att)
	}
	alarms, _ := m["alarms"].([]map[string]any)
	if len(alarms) != 2 || alarms[0]["offset"] != "-PT15M" || alarms[0]["action"] != "display" ||
		alarms[1]["at"] != "2026-03-13T09:00:00+01:00" {
		t.Errorf("alarms = %v", alarms)
	}

	h := byUID["holiday-1"]
	if h["all_day"] != true || h["start"] != "2026-03-14" || h["end"] != "2026-03-15" {
		t.Errorf("all-day = start %v end %v all_day %v, want 03-14..03-15 inclusive", h["start"], h["end"], h["all_day"])
	}

	if d := byUID["day-1"]; d["start"] != "2026-03-12" || d["end"] != "2026-03-12" || d["all_day"] != true {
		t.Errorf("date event with no end = %v, want the one day", d)
	}
	trip := byUID["trip-1"]
	if trip["start"] != "2026-10-24" || trip["end"] != "2026-10-26" {
		t.Errorf("DST all-day = %v..%v, want 2026-10-24..2026-10-26", trip["start"], trip["end"])
	}
	if trip["description"] != "Bring snacks, drinks and chairs" {
		t.Errorf("unescaped comma cut the text: %q", trip["description"])
	}
	if org, _ := trip["organizer"].(map[string]any); org["address"] != "dana@example.com" {
		t.Errorf("organizer = %v", trip["organizer"])
	}
	if a, _ := trip["alarms"].([]map[string]any); len(a) != 1 || a[0]["related"] != "end" {
		t.Errorf("end-relative alarm = %v", trip["alarms"])
	}

	w := byUID["weekly-1"]
	if w["recurring_instance"] != true || w["recurrence_rule"] != "FREQ=WEEKLY;BYDAY=MO" {
		t.Errorf("weekly = %v, want the instance flagged with the series rule", w)
	}
	if _, ok := byUID["solo-1"]["recurring_instance"]; ok {
		t.Error("a one-off event is flagged as a recurring instance")
	}
	if _, ok := byUID["solo-1"]["attendees"]; ok {
		t.Error("an event without attendees lists attendees")
	}
}

func TestListEventsMergesSharedUID(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	fake.shared = true
	fake.objects[fakeShared+"copy.ics"] = fxSolo
	c := testClient(t, fake, srv.URL, failOnAsk(t))
	out, err := c.ListEvents(context.Background(), 0, 0, nil, strptr("2026-03-09"), strptr("2026-03-15"))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	cals, _ := eventsByUID(t, out)["solo-1"]["calendars"].([]string)
	if strings.Join(cals, ",") != "Personal,Shared" {
		t.Fatalf("calendars = %v, want both", cals)
	}
	if out["count"] != 4 {
		t.Fatalf("count = %v, want 4 (the shared copy merged)", out["count"])
	}
}

func TestSearchContactsRichFields(t *testing.T) {
	fake, srv := newFakeDAV(t)
	defer srv.Close()
	fake.objects[fakeBook+"lin.vcf"] = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Lin Chen\r\nN:Chen;Lin;;;\r\n" +
		"NICKNAME:Linny\r\nORG:Acme Corp;R&D\r\nTITLE:Staff Engineer\r\nBDAY:1990-04-02\r\n" +
		"EMAIL;TYPE=INTERNET,WORK,pref:lin@acme.test\r\nTEL;TYPE=CELL,VOICE:+15550001111\r\n" +
		"item1.TEL:+15550002222\r\nitem1.X-ABLabel:_$!<Assistant>!$_\r\n" +
		"ADR;TYPE=WORK:;Suite 5;1 Main St;Springfield;IL;62701;USA\r\n" +
		"URL:https://acme.test/lin\r\nNOTE:Met at the conference\r\nEND:VCARD\r\n"
	fake.objects[fakeBook+"noyear.vcf"] = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Sam Noyear\r\n" +
		"BDAY;X-APPLE-OMIT-YEAR=1604:1604-11-05\r\nEND:VCARD\r\n"
	c := testClient(t, fake, srv.URL, failOnAsk(t))
	out, err := c.SearchContacts(context.Background(), "lin chen", 10)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if err := mcpserver.CheckOutput("search_contacts", out); err != nil {
		t.Fatal(err)
	}
	contacts, _ := out["contacts"].([]map[string]any)
	if len(contacts) != 1 {
		t.Fatalf("contacts = %v", contacts)
	}
	got := contacts[0]
	for key, want := range map[string]any{"nickname": "Linny", "organization": "Acme Corp",
		"job_title": "Staff Engineer", "birthday": "1990-04-02", "notes": "Met at the conference"} {
		if got[key] != want {
			t.Errorf("%s = %#v, want %#v", key, got[key], want)
		}
	}
	emails, _ := got["emails"].([]map[string]any)
	if len(emails) != 1 || emails[0]["value"] != "lin@acme.test" || emails[0]["type"] != "work" {
		t.Errorf("emails = %v", emails)
	}
	phones, _ := got["phones"].([]map[string]any)
	if len(phones) != 2 || phones[0]["type"] != "cell" || phones[1]["type"] != "assistant" {
		t.Errorf("phones = %v", phones)
	}
	addrs, _ := got["addresses"].([]map[string]any)
	if len(addrs) != 1 || addrs[0]["city"] != "Springfield" || addrs[0]["street"] != "1 Main St" || addrs[0]["type"] != "work" {
		t.Errorf("addresses = %v", addrs)
	}
	if urls, _ := got["urls"].([]string); len(urls) != 1 {
		t.Errorf("urls = %v", got["urls"])
	}
	out, _ = c.SearchContacts(context.Background(), "noyear", 10)
	if contacts, _ := out["contacts"].([]map[string]any); len(contacts) != 1 || contacts[0]["birthday"] != "--11-05" {
		t.Errorf("year-less birthday = %v", out["contacts"])
	}
}

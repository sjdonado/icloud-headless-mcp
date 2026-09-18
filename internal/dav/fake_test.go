package dav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDAV is an in-process CalDAV/CardDAV server. It implements exactly the
// discovery chain and object operations the Client uses, with fixed fixtures
// chosen to exercise the behaviors pinned in
// openspec/changes/go-rewrite/specs/: recurring expansion, attendee
// approval, the non-<uid>.ics fallback lookup, and contact matching.
type fakeDAV struct {
	t *testing.T

	mu sync.Mutex
	// href path -> raw ICS or VCF body.
	objects map[string]string
	// REPORT request bodies by path, for asserting the client asked for
	// expansion and windowed filters.
	reports map[string][]string
	// GET paths to fail with the given status, for proving transport
	// errors surface instead of reading as empty.
	failGET map[string]int
	// PUT bodies by path, for asserting SEQUENCE bumps and in-place writes.
	puts    map[string]string
	deleted []string
}

const (
	fakePersonal = "/calendars/Personal/"
	fakeLegacy   = "/calendars/LegacyReminders/"
	fakeBook     = "/books/Home/"
)

const (
	fxSolo = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:solo-1\r\nDTSTAMP:20260301T120000Z\r\n" +
		"DTSTART;TZID=Europe/Amsterdam:20260310T143000\r\n" +
		"DTEND;TZID=Europe/Amsterdam:20260310T153000\r\n" +
		"SUMMARY:Dentist\r\nLOCATION:Main St\r\nSEQUENCE:2\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"

	fxWeeklyInstance = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:weekly-1\r\nDTSTAMP:20260301T120000Z\r\n" +
		"DTSTART;TZID=Europe/Amsterdam:20260309T090000\r\n" +
		"DTEND;TZID=Europe/Amsterdam:20260309T093000\r\n" +
		"RECURRENCE-ID;TZID=Europe/Amsterdam:20260309T090000\r\n" +
		"SUMMARY:Standup\r\nSEQUENCE:0\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"

	fxAttended = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:attend-1\r\nDTSTAMP:20260301T120000Z\r\n" +
		"DTSTART:20260311T100000Z\r\nDTEND:20260311T110000Z\r\n" +
		"SUMMARY:Planning\r\nSEQUENCE:3\r\n" +
		"ATTENDEE;CN=Alice:mailto:alice@example.com\r\n" +
		"ATTENDEE;CN=Bob:mailto:bob@example.com\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"

	// fxStray lives at a href no uid lookup would guess, so only the
	// windowed-search fallback can find it.
	fxStray = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:stray-1\r\nDTSTAMP:20260301T120000Z\r\n" +
		"DTSTART:20260312T120000Z\r\nDTEND:20260312T130000Z\r\n" +
		"SUMMARY:Stray\r\nSEQUENCE:0\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"

	fxAda = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada Lovelace\r\n" +
		"EMAIL:ada@example.com\r\nTEL:+3101234567\r\nEND:VCARD\r\n"

	fxGrace = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Grace Hopper\r\n" +
		"EMAIL:grace@navy.mil\r\nTEL:+15551234567\r\nEND:VCARD\r\n"

	// fxBroken is a card no parser accepts. It lives in the book only in
	// the test that pins skip-broken behavior; the shared book stays clean
	// so the match table exercises the single-REPORT fast path.
	fxBroken = "BEGIN:VCARD\r\nFN:No End In Sight\r\n"
)

func newFakeDAV(t *testing.T) (*fakeDAV, *httptest.Server) {
	t.Helper()
	f := &fakeDAV{
		t: t,
		objects: map[string]string{
			fakePersonal + "solo-1.ics":   fxSolo,
			fakePersonal + "attend-1.ics": fxAttended,
			fakePersonal + "oddball.ics":  fxStray,
			fakeBook + "ada.vcf":          fxAda,
			fakeBook + "grace.vcf":        fxGrace,
			fakePersonal + "weekly-9.ics": fxWeeklyInstance,
		},
		reports: map[string][]string{},
		puts:    map[string]string{},
	}
	return f, httptest.NewServer(f)
}

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func (f *fakeDAV) multistatus(w http.ResponseWriter, responses ...string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`+
		`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="urn:ietf:params:xml:ns:carddav">`+
		strings.Join(responses, "")+`</D:multistatus>`)
}

func calResponse(href, name, comp string) string {
	return `<D:response><D:href>` + href + `</D:href><D:propstat><D:prop>` +
		`<D:resourcetype><D:collection/><C:calendar/></D:resourcetype>` +
		`<D:displayname>` + name + `</D:displayname>` +
		`<C:supported-calendar-component-set><C:comp name="` + comp + `"/></C:supported-calendar-component-set>` +
		`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	switch r.Method {
	case "PROPFIND":
		switch path {
		case "/", "":
			f.multistatus(w, `<D:response><D:href>/</D:href><D:propstat><D:prop>`+
				`<D:current-user-principal><D:href>/principal/</D:href></D:current-user-principal>`+
				`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
		case "/principal/":
			f.multistatus(w, `<D:response><D:href>/principal/</D:href><D:propstat><D:prop>`+
				`<C:calendar-home-set><D:href>/calendars/</D:href></C:calendar-home-set>`+
				`<A:addressbook-home-set><D:href>/books/</D:href></A:addressbook-home-set>`+
				`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
		case "/calendars/":
			f.multistatus(w,
				calResponse(fakePersonal, "Personal", "VEVENT"),
				calResponse(fakeLegacy, "LegacyReminders", "VTODO"))
		case "/books/":
			f.multistatus(w, `<D:response><D:href>`+fakeBook+`</D:href><D:propstat><D:prop>`+
				`<D:resourcetype><D:collection/><A:addressbook/></D:resourcetype>`+
				`<D:displayname>Home</D:displayname>`+
				`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
		default:
			http.NotFound(w, r)
		}
	case "REPORT":
		body, _ := io.ReadAll(r.Body)
		f.reports[path] = append(f.reports[path], string(body))
		var responses []string
		switch {
		case strings.HasPrefix(path, "/calendars/"):
			for href, ics := range f.objects {
				if !strings.HasPrefix(href, path) || !strings.HasSuffix(href, ".ics") {
					continue
				}
				responses = append(responses,
					`<D:response><D:href>`+href+`</D:href><D:propstat><D:prop>`+
						`<C:calendar-data>`+esc(ics)+`</C:calendar-data>`+
						`<D:getetag>"v1"</D:getetag>`+
						`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
			}
		case strings.HasPrefix(path, "/books/"):
			if strings.Contains(string(body), "sync-collection") {
				// A sync with no data request: hrefs and etags only, no
				// bodies. What the per-card fallback enumerates.
				for href := range f.objects {
					if !strings.HasPrefix(href, path) || !strings.HasSuffix(href, ".vcf") {
						continue
					}
					responses = append(responses,
						`<D:response><D:href>`+href+`</D:href><D:propstat><D:prop>`+
							`<D:getetag>"v1"</D:getetag>`+
							`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
				}
				responses = append(responses, `<D:sync-token>fake-token</D:sync-token>`)
				break
			}
			for href, vcf := range f.objects {
				if !strings.HasPrefix(href, path) || !strings.HasSuffix(href, ".vcf") {
					continue
				}
				responses = append(responses,
					`<D:response><D:href>`+href+`</D:href><D:propstat><D:prop>`+
						`<A:address-data>`+esc(vcf)+`</A:address-data>`+
						`<D:getetag>"v1"</D:getetag>`+
						`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
			}
		default:
			http.NotFound(w, r)
			return
		}
		f.multistatus(w, responses...)
	case "GET":
		if code, ok := f.failGET[path]; ok {
			w.WriteHeader(code)
			return
		}
		body, ok := f.objects[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(path, ".ics") {
			w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = io.WriteString(w, body)
	case "PUT":
		body, _ := io.ReadAll(r.Body)
		f.objects[path] = string(body)
		f.puts[path] = string(body)
		w.Header().Set("ETag", `"v2"`)
		w.WriteHeader(http.StatusCreated)
	case "DELETE":
		if _, ok := f.objects[path]; !ok {
			http.NotFound(w, r)
			return
		}
		delete(f.objects, path)
		f.deleted = append(f.deleted, path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

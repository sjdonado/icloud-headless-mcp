package mail

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// fakeMsg is one mailbox message. from/subject are raw header values;
// internalDate drives SINCE/BEFORE matching.
type fakeMsg struct {
	uid          uint32
	seen         bool
	answered     bool
	flagged      bool
	internalDate time.Time
	from         string
	subject      string
	raw          string
}

func d(y int, m time.Month, day int) time.Time {
	return time.Date(y, m, day, 12, 0, 0, 0, time.UTC)
}

const (
	fxPlainHeaders = "From: boss@example.com\r\nSubject: Q3 planning\r\nDate: Tue, 01 Sep 2026 09:00:00 +0200\r\n"
	fxPlainBody    = "Hi,\r\n\r\nSee you Thursday.\r\n"
	fxPlain        = fxPlainHeaders + "Content-Type: text/plain; charset=utf-8\r\n\r\n" + fxPlainBody

	fxEncHeaders = "From: sklep@example.com\r\n" +
		"Subject: =?UTF-8?B?RmFrdHVyYSB6YSB3cnplc2llxYQ=?=\r\n" +
		"Date: Sun, 20 Sep 2026 10:00:00 +0200\r\n"
	fxEnc = fxEncHeaders + "Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"Twoja faktura jest gotowa.\r\n"

	fxHTMLHeaders = "From: airline@example.com\r\nSubject: Your boarding pass\r\n" +
		"Date: Fri, 25 Sep 2026 18:00:00 +0200\r\n"
	fxHTML = fxHTMLHeaders +
		"Content-Type: multipart/alternative; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><head><title>Boarding</title><style>.x{color:red}</style></head>" +
		"<body><h1>Boarding pass</h1><p>Gate <b>42</b></p><script>alert(1)</script></body></html>\r\n" +
		"--b1--\r\n"

	fxAttachHeaders = "From: bank@example.com\r\nSubject: September statement\r\n" +
		"Date: Wed, 30 Sep 2026 08:00:00 +0200\r\n"
	fxAttach = fxAttachHeaders +
		"Content-Type: multipart/mixed; boundary=\"b2\"\r\n\r\n" +
		"--b2\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSee attached.\r\n" +
		"--b2\r\nContent-Type: application/pdf; name=\"statement.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"statement.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"JVBERi0xLjQKJeLjz9MKMSAwIG9iago=\r\n" +
		"--b2\r\nContent-Type: application/x-msdownload; name=\"evil.exe\"\r\n" +
		"Content-Disposition: attachment; filename=\"evil.exe\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"TVoAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\r\n" +
		"--b2--\r\n"

	fxNestedHeaders = "From: nested@example.com\r\nSubject: Nested parts\r\n" +
		"Date: Thu, 10 Sep 2026 08:00:00 +0200\r\n"
	fxNested = fxNestedHeaders +
		"Content-Type: multipart/mixed; boundary=\"outer\"\r\n\r\n" +
		"--outer\r\nContent-Type: multipart/alternative; boundary=\"inner\"\r\n\r\n" +
		"--inner\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nInner body text.\r\n" +
		"--inner\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><body><p>Inner body text.</p></body></html>\r\n" +
		"--inner--\r\n" +
		"--outer\r\nContent-Type: text/plain; name=\"n.txt\"\r\n" +
		"Content-Disposition: attachment; filename=\"n.txt\"\r\n\r\n" +
		"note\r\n" +
		"--outer--\r\n"
)

// fakeIMAP is a hand-rolled IMAP server speaking exactly the commands the
// Client sends. SEARCH matches raw headers like iCloud (no encoded-word
// decoding), and returns uids ascending regardless of age, so client-side
// sorting and the decoded local pass are genuinely exercised.
type fakeIMAP struct {
	t *testing.T

	mu       sync.Mutex
	boxes    map[string][]*fakeMsg
	searches []string
	// fetches records each UID FETCH's arguments, for counting round trips.
	fetches []string
	// failAllSearch answers a bare ALL search with NO, as a flaky server
	// would, to exercise the local pass's failure path.
	failAllSearch bool
}

func newFakeIMAP(t *testing.T) (*fakeIMAP, string) {
	t.Helper()
	f := &fakeIMAP{
		t: t,
		boxes: map[string][]*fakeMsg{
			"INBOX": {
				{uid: 1, seen: true, internalDate: d(2026, 9, 1), from: "boss@example.com", subject: "Q3 planning", raw: fxPlain},
				{uid: 2, internalDate: d(2026, 9, 20), from: "sklep@example.com", subject: "=?UTF-8?B?RmFrdHVyYSB6YSB3cnplc2llxYQ=?=", raw: fxEnc},
				{uid: 3, seen: true, answered: true, internalDate: d(2026, 9, 25), from: "airline@example.com", subject: "Your boarding pass", raw: fxHTML},
				{uid: 4, internalDate: d(2026, 9, 30), from: "bank@example.com", subject: "September statement", raw: fxAttach},
				{uid: 5, internalDate: d(2026, 9, 10), from: "nested@example.com", subject: "Nested parts", raw: fxNested},
			},
			"Archive": {},
		},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f, ln.Addr().String()
}

// readCommandLine reads one command line, splicing synchronizing literals:
// a trailing {n} means n bytes plus CRLF follow, and they belong to the
// command (the client sends non-ASCII search criteria this way). A sync
// literal (no trailing +) needs a continuation response first, or the
// client waits forever.
func readCommandLine(rd *bufio.Reader, conn net.Conn) (string, error) {
	raw, err := rd.ReadString('\n')
	if err != nil {
		return "", err
	}
	line := strings.TrimRight(raw, "\r\n")
	for strings.HasSuffix(line, "}") {
		open := strings.LastIndex(line, "{")
		if open < 0 {
			break
		}
		marker := line[open+1 : len(line)-1]
		n, err := strconv.Atoi(strings.TrimSuffix(marker, "+"))
		if err != nil {
			break
		}
		if !strings.HasSuffix(marker, "+") {
			if _, err := fmt.Fprintf(conn, "+ OK\r\n"); err != nil {
				return "", err
			}
		}
		lb := make([]byte, n+2)
		if _, err := io.ReadFull(rd, lb); err != nil {
			return "", err
		}
		line = line[:open] + strconv.Quote(string(lb[:n]))
	}
	return line, nil
}

func (f *fakeIMAP) serve(conn net.Conn) {
	defer conn.Close()
	fmt.Fprintf(conn, "* OK fake ready\r\n")
	rd := bufio.NewReader(conn)
	selected := ""
	for {
		line, err := readCommandLine(rd, conn)
		if err != nil {
			return
		}
		tag, rest := splitTag(line)
		upper := strings.ToUpper(rest)
		switch {
		case strings.HasPrefix(upper, "LOGIN "):
			fmt.Fprintf(conn, "%s OK LOGIN completed\r\n", tag)
		case strings.HasPrefix(upper, "LIST "):
			names := []string{}
			for name := range f.boxes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				fmt.Fprintf(conn, "* LIST (\\HasNoChildren) \"/\" \"%s\"\r\n", name)
			}
			fmt.Fprintf(conn, "%s OK LIST completed\r\n", tag)
		case strings.HasPrefix(upper, "SELECT ") || strings.HasPrefix(upper, "EXAMINE "):
			name := unquote(strings.TrimSpace(rest[strings.Index(rest, " ")+1:]))
			msgs, ok := f.boxes[name]
			if !ok {
				fmt.Fprintf(conn, "%s NO no such mailbox\r\n", tag)
				continue
			}
			selected = name
			fmt.Fprintf(conn, "* FLAGS (\\Answered \\Seen)\r\n")
			fmt.Fprintf(conn, "* %d EXISTS\r\n* 0 RECENT\r\n", len(msgs))
			fmt.Fprintf(conn, "* OK [UIDVALIDITY 1] UIDs valid\r\n")
			fmt.Fprintf(conn, "%s OK [READ-ONLY] EXAMINE completed\r\n", tag)
		case strings.HasPrefix(upper, "UID SEARCH"):
			crit := strings.TrimSpace(rest[len("UID SEARCH"):])
			f.mu.Lock()
			f.searches = append(f.searches, crit)
			failAll := f.failAllSearch && strings.EqualFold(strings.TrimSpace(crit), "ALL")
			f.mu.Unlock()
			if failAll {
				fmt.Fprintf(conn, "%s NO search failed\r\n", tag)
				continue
			}
			uids := f.search(selected, crit)
			fmt.Fprintf(conn, "* SEARCH %s\r\n", strings.Join(uids, " "))
			fmt.Fprintf(conn, "%s OK SEARCH completed\r\n", tag)
		case strings.HasPrefix(upper, "UID FETCH"):
			rest := strings.TrimSpace(rest[len("UID FETCH"):])
			set, items := splitSetItems(rest)
			wanted := parseUIDSet(set)
			f.mu.Lock()
			f.fetches = append(f.fetches, rest)
			f.mu.Unlock()
			for i, m := range f.boxes[selected] {
				if !wanted[m.uid] {
					continue
				}
				f.writeFetch(conn, i+1, m, items)
			}
			fmt.Fprintf(conn, "%s OK FETCH completed\r\n", tag)
		case strings.HasPrefix(upper, "LOGOUT"):
			fmt.Fprintf(conn, "* BYE bye\r\n%s OK LOGOUT completed\r\n", tag)
			return
		default:
			fmt.Fprintf(conn, "%s BAD unknown command\r\n", tag)
		}
	}
}

// writeFetch answers one message of a UID FETCH: FLAGS, RFC822.SIZE,
// INTERNALDATE, BODYSTRUCTURE, and at most one body section. The section
// is echoed as requested (minus PEEK), because the client matches the
// response section against its request. A non-PEEK body fetch sets \Seen,
// as a real server does, so a read that forgets PEEK shows up in tests.
func (f *fakeIMAP) writeFetch(conn net.Conn, seq int, m *fakeMsg, items string) {
	up := strings.ToUpper(items)
	parts := []string{fmt.Sprintf("UID %d", m.uid)}
	if strings.Contains(up, "BODY[") && !strings.Contains(up, "BODY.PEEK[") {
		m.seen = true
	}
	var flagItem string
	if strings.Contains(up, "FLAGS") {
		var flags []string
		if m.seen {
			flags = append(flags, `\Seen`)
		}
		if m.answered {
			flags = append(flags, `\Answered`)
		}
		if m.flagged {
			flags = append(flags, `\Flagged`)
		}
		// Sent last, after any body literal, as iCloud may: the client
		// must not read flags off the first items of a response.
		flagItem = " FLAGS (" + strings.Join(flags, " ") + ")"
	}
	if strings.Contains(up, "RFC822.SIZE") {
		parts = append(parts, fmt.Sprintf("RFC822.SIZE %d", len(m.raw)))
	}
	if strings.Contains(up, "INTERNALDATE") {
		parts = append(parts, `INTERNALDATE "`+m.internalDate.Format("02-Jan-2006 15:04:05 -0700")+`"`)
	}
	if strings.Contains(up, "BODYSTRUCTURE") {
		parts = append(parts, "BODYSTRUCTURE "+bodyStructureWire(imapserver.ExtractBodyStructure(strings.NewReader(m.raw))))
	}
	var body []byte
	section := ""
	if i := strings.Index(up, "BODY.PEEK["); i >= 0 || strings.Contains(up, "BODY[") {
		if i < 0 {
			i = strings.Index(up, "BODY[") - 5
		}
		tail := items[i+len("BODY.PEEK["):]
		j := strings.Index(tail, "]")
		spec := tail[:j]
		item := &imap.FetchItemBodySection{}
		partial := ""
		if k := strings.Index(tail[j:], "<"); k >= 0 && k == 1 {
			lo, hi, _ := strings.Cut(strings.Trim(tail[j+1:j+strings.Index(tail[j:], ">")], "<>"), ".")
			off, _ := strconv.ParseInt(lo, 10, 64)
			size, _ := strconv.ParseInt(hi, 10, 64)
			item.Partial = &imap.SectionPartial{Offset: off, Size: size}
			partial = fmt.Sprintf("<%d>", off)
		}
		switch {
		case strings.Contains(strings.ToUpper(spec), "HEADER.FIELDS"):
			body = headerBlock(m.raw)
		case spec == "":
			body = []byte(m.raw)
		default:
			for _, n := range strings.Split(spec, ".") {
				v, _ := strconv.Atoi(n)
				item.Part = append(item.Part, v)
			}
			body = imapserver.ExtractBodySection(strings.NewReader(m.raw), item)
		}
		if item.Partial != nil && spec == "" {
			body = body[:min(len(body), int(item.Partial.Size))]
		}
		section = fmt.Sprintf("BODY[%s]%s {%d}\r\n", spec, partial, len(body))
	}
	// No CRLF between the literal bytes and ')': the literal's framing
	// CRLF comes before its bytes, not after.
	fmt.Fprintf(conn, "* %d FETCH (%s", seq, strings.Join(parts, " "))
	if section != "" {
		fmt.Fprintf(conn, " %s", section)
		conn.Write(body)
	}
	fmt.Fprintf(conn, "%s)\r\n", flagItem)
}

// bodyStructureWire serializes an extended BODYSTRUCTURE (RFC 3501 7.4.2)
// from go-imap's own extractor, so the client parses real structure.
func bodyStructureWire(bs imap.BodyStructure) string {
	q := func(v string) string {
		if v == "" {
			return "NIL"
		}
		return strconv.Quote(v)
	}
	params := func(m map[string]string) string {
		if len(m) == 0 {
			return "NIL"
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []string
		for _, k := range keys {
			out = append(out, strconv.Quote(k)+" "+strconv.Quote(m[k]))
		}
		return "(" + strings.Join(out, " ") + ")"
	}
	disp := func(d *imap.BodyStructureDisposition) string {
		if d == nil {
			return "NIL"
		}
		return "(" + strconv.Quote(d.Value) + " " + params(d.Params) + ")"
	}
	switch bs := bs.(type) {
	case *imap.BodyStructureMultiPart:
		var b strings.Builder
		b.WriteString("(")
		for _, child := range bs.Children {
			b.WriteString(bodyStructureWire(child))
		}
		b.WriteString(" " + strconv.Quote(bs.Subtype) + " " + params(bs.Extended.Params) + " " + disp(bs.Extended.Disposition) + " NIL NIL)")
		return b.String()
	case *imap.BodyStructureSinglePart:
		enc := bs.Encoding
		if enc == "" {
			enc = "7BIT"
		}
		out := fmt.Sprintf("(%s %s %s %s %s %s %d", strconv.Quote(bs.Type), strconv.Quote(bs.Subtype),
			params(bs.Params), q(bs.ID), q(bs.Description), strconv.Quote(enc), bs.Size)
		if bs.Text != nil {
			out += fmt.Sprintf(" %d", bs.Text.NumLines)
		}
		return out + " NIL " + disp(bs.Extended.Disposition) + " NIL NIL)"
	}
	return "NIL"
}

// search matches against raw headers (no encoded-word decoding) with AND
// semantics over ALL/UNSEEN/ANSWERED, TEXT/SUBJECT/FROM, SINCE/BEFORE.
func (f *fakeIMAP) search(box, crit string) []string {
	toks := tokenize(crit)
	var out []string
	for _, m := range f.boxes[box] {
		if matchMsg(m, toks) {
			out = append(out, strconv.FormatUint(uint64(m.uid), 10))
		}
	}
	return out
}

func matchMsg(m *fakeMsg, toks []string) bool {
	raw := m.raw
	for i := 0; i < len(toks); i++ {
		switch strings.ToUpper(toks[i]) {
		case "ALL":
		case "UNSEEN":
			if m.seen {
				return false
			}
		case "ANSWERED":
			if !m.answered {
				return false
			}
		case "TEXT":
			i++
			if i >= len(toks) || !strings.Contains(raw, toks[i]) {
				return false
			}
		case "SUBJECT":
			i++
			if i >= len(toks) || !strings.Contains(m.subject, toks[i]) {
				return false
			}
		case "FROM":
			i++
			if i >= len(toks) || !strings.Contains(m.from, toks[i]) {
				return false
			}
		case "SINCE":
			i++
			day, err := time.Parse("02-Jan-2006", toks[i])
			if err != nil || m.internalDate.Before(day) {
				return false
			}
		case "BEFORE":
			i++
			day, err := time.Parse("02-Jan-2006", toks[i])
			if err != nil || !m.internalDate.Before(day) {
				return false
			}
		}
	}
	return true
}

func splitTag(line string) (string, string) {
	if i := strings.Index(line, " "); i > 0 {
		return line[:i], line[i+1:]
	}
	return line, ""
}

func unquote(s string) string {
	return strings.Trim(s, `"`)
}

// tokenize splits criteria keeping quoted strings whole (quotes stripped).
func tokenize(s string) []string {
	var toks []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks
}

func splitSetItems(s string) (string, string) {
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
			if depth == 1 {
				return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
			}
		}
	}
	return strings.TrimSpace(s), ""
}

func parseUIDSet(set string) map[uint32]bool {
	out := map[uint32]bool{}
	for _, part := range strings.Split(set, ",") {
		part = strings.TrimSpace(part)
		if lo, hi, ok := strings.Cut(part, ":"); ok {
			a, err1 := strconv.ParseUint(strings.TrimSpace(lo), 10, 32)
			b, err2 := strconv.ParseUint(strings.TrimSpace(hi), 10, 32)
			if err1 != nil || err2 != nil {
				continue
			}
			for u := a; u <= b; u++ {
				out[uint32(u)] = true
			}
			continue
		}
		if u, err := strconv.ParseUint(part, 10, 32); err == nil {
			out[uint32(u)] = true
		}
	}
	return out
}

// headerBlock returns the raw header block: everything before the first
// blank line. Header fetch responses carry raw headers; decoding is the
// client's job.
func headerBlock(raw string) []byte {
	if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		return []byte(raw[:i])
	}
	return []byte(raw)
}

// fakeSMTP is a plaintext sink recording one message. AUTH PLAIN always
// succeeds; the point is the send sequence, not the handshake.
type fakeSMTP struct {
	t *testing.T

	mu   sync.Mutex
	from string
	to   []string
	data string
}

func newFakeSMTP(t *testing.T) (*fakeSMTP, string) {
	t.Helper()
	f := &fakeSMTP{t: t}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f, ln.Addr().String()
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	fmt.Fprintf(conn, "220 fake ready\r\n")
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	inData := false
	var data strings.Builder
	upper := func(s string) string { return strings.ToUpper(s) }
	for sc.Scan() {
		line := sc.Text()
		if inData {
			if line == "." {
				inData = false
				f.mu.Lock()
				f.data = data.String()
				f.mu.Unlock()
				data.Reset()
				fmt.Fprintf(conn, "250 OK queued\r\n")
				continue
			}
			data.WriteString(line + "\r\n")
			continue
		}
		switch {
		case strings.HasPrefix(upper(line), "EHLO") || strings.HasPrefix(upper(line), "HELO"):
			fmt.Fprintf(conn, "250-fake\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(upper(line), "AUTH "):
			fmt.Fprintf(conn, "235 authenticated\r\n")
		case strings.HasPrefix(upper(line), "MAIL FROM:"):
			f.mu.Lock()
			f.from = strings.Trim(line[len("MAIL FROM:"):], " <>")
			f.to = nil
			f.mu.Unlock()
			fmt.Fprintf(conn, "250 OK\r\n")
		case strings.HasPrefix(upper(line), "RCPT TO:"):
			f.mu.Lock()
			f.to = append(f.to, strings.Trim(line[len("RCPT TO:"):], " <>"))
			f.mu.Unlock()
			fmt.Fprintf(conn, "250 OK\r\n")
		case strings.HasPrefix(upper(line), "DATA"):
			inData = true
			fmt.Fprintf(conn, "354 end with .\r\n")
		case strings.HasPrefix(upper(line), "QUIT"):
			fmt.Fprintf(conn, "221 bye\r\n")
			return
		case strings.HasPrefix(upper(line), "RSET"):
			fmt.Fprintf(conn, "250 OK\r\n")
		default:
			fmt.Fprintf(conn, "502 unimplemented\r\n")
		}
	}
}

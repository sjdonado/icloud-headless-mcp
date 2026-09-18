package mail

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	smtpclient "github.com/emersion/go-smtp"

	"github.com/sjdonado/icloud-headless-mcp/internal/config"
)

func testClient(t *testing.T, imapAddr string) *Client {
	t.Helper()
	dir := t.TempDir()
	c := &Client{
		cfg:       &config.Config{AppleID: "test@example.com", AppPassword: "test", AttachmentsDir: dir},
		imapAddr:  imapAddr,
		attachDir: dir,
		now:       time.Now,
		dialIMAP: func(ctx context.Context, addr string) (*imapclient.Client, error) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				return nil, err
			}
			cl := imapclient.New(conn, nil)
			if err := cl.WaitGreeting(); err != nil {
				conn.Close()
				return nil, err
			}
			if err := cl.Login("test", "test").Wait(); err != nil {
				conn.Close()
				return nil, err
			}
			return cl, nil
		},
	}
	return c
}

func strptr(s string) *string { return &s }

func TestListMailboxes(t *testing.T) {
	fake, addr := newFakeIMAP(t)
	_ = fake
	c := testClient(t, addr)
	out, err := c.ListMailboxes(context.Background())
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	boxes, _ := out["mailboxes"].([]string)
	if len(boxes) != 2 || boxes[0] != "Archive" || boxes[1] != "INBOX" {
		t.Fatalf("mailboxes = %v", boxes)
	}
	if out["count"] != 2 {
		t.Fatalf("result = %v", out)
	}
}

func TestListMailNewestFirst(t *testing.T) {
	fake, addr := newFakeIMAP(t)
	_ = fake
	c := testClient(t, addr)
	// The fake returns SEARCH uids ascending; the client must still list
	// newest first with flags from the dedicated UID searches.
	out, err := c.ListMail(context.Background(), "INBOX", 10, false, nil, nil)
	if err != nil {
		t.Fatalf("ListMail: %v", err)
	}
	msgs, _ := out["messages"].([]map[string]any)
	var uids []string
	for _, m := range msgs {
		uids = append(uids, m["uid"].(string))
	}
	want := []string{"5", "4", "3", "2", "1"}
	if strings.Join(uids, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", uids, want)
	}
	byUID := map[string]map[string]any{}
	for _, m := range msgs {
		byUID[m["uid"].(string)] = m
	}
	if byUID["2"]["unread"] != true || byUID["4"]["unread"] != true {
		t.Fatalf("unread flags wrong: %v", byUID)
	}
	if byUID["1"]["unread"] != false || byUID["3"]["unread"] != false {
		t.Fatalf("seen flags wrong: %v", byUID)
	}
	if byUID["3"]["answered"] != true || byUID["1"]["answered"] != false {
		t.Fatalf("answered flags wrong: %v", byUID)
	}
	if out["matched"] != 5 || out["count"] != 5 {
		t.Fatalf("result = %v", out)
	}
}

func TestListMailUnreadOnlyAndLimit(t *testing.T) {
	_, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	out, err := c.ListMail(context.Background(), "INBOX", 10, true, nil, nil)
	if err != nil {
		t.Fatalf("ListMail: %v", err)
	}
	msgs, _ := out["messages"].([]map[string]any)
	if len(msgs) != 3 || msgs[0]["uid"] != "5" || msgs[1]["uid"] != "4" || msgs[2]["uid"] != "2" {
		t.Fatalf("unread_only = %v", msgs)
	}
	out, err = c.ListMail(context.Background(), "INBOX", 2, false, nil, nil)
	if err != nil {
		t.Fatalf("ListMail: %v", err)
	}
	msgs, _ = out["messages"].([]map[string]any)
	if out["matched"] != 5 || len(msgs) != 2 || msgs[0]["uid"] != "5" {
		t.Fatalf("limited = %v", out)
	}
}

func TestListMailUnknownMailbox(t *testing.T) {
	fake, addr := newFakeIMAP(t)
	_ = fake
	c := testClient(t, addr)
	out, err := c.ListMail(context.Background(), "Guessed", 10, false, nil, nil)
	if err != nil {
		t.Fatalf("ListMail: %v", err)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, `"Guessed"`) || !strings.Contains(msg, "INBOX") {
		t.Fatalf("error = %q, want the name plus available mailboxes", msg)
	}
}

func TestListMailBoundsExclusiveBefore(t *testing.T) {
	fake, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	out, err := c.ListMail(context.Background(), "INBOX", 10, false, strptr("2026-09-20"), strptr("2026-09-29"))
	if err != nil {
		t.Fatalf("ListMail: %v", err)
	}
	msgs, _ := out["messages"].([]map[string]any)
	var uids []string
	for _, m := range msgs {
		uids = append(uids, m["uid"].(string))
	}
	if strings.Join(uids, ",") != "3,2" {
		t.Fatalf("bounded = %v, want [3 2] (msg4 on 09-30 excluded)", uids)
	}
	// The inclusive until must reach the server as BEFORE the next day.
	var sawSince, sawBefore bool
	for _, crit := range fake.searches {
		if strings.Contains(crit, `SINCE "20-Sep-2026"`) {
			sawSince = true
		}
		if strings.Contains(crit, `BEFORE "30-Sep-2026"`) {
			sawBefore = true
		}
	}
	if !sawSince || !sawBefore {
		t.Fatalf("server never saw the date terms: %v", fake.searches)
	}
	bounds, _ := out["bounds"].(map[string]any)
	if bounds["since"] != "2026-09-20" || bounds["until"] != "2026-09-29" {
		t.Fatalf("bounds = %v", bounds)
	}
}

func TestSearchServerHit(t *testing.T) {
	_, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	out, err := c.SearchMail(context.Background(), "boarding", "INBOX", 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchMail: %v", err)
	}
	msgs, _ := out["messages"].([]map[string]any)
	if out["matched"] != 1 || msgs[0]["uid"] != "3" {
		t.Fatalf("result = %v", out)
	}
}

func TestSearchEncodedSubjectNeedsLocalPass(t *testing.T) {
	fake, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	// The subject is MIME-encoded on the wire, so no server-side key can
	// match the plain word; only the decoded local pass finds uid 2.
	out, err := c.SearchMail(context.Background(), "wrzesień", "INBOX", 10, nil, nil)
	if err != nil {
		t.Fatalf("SearchMail: %v", err)
	}
	msgs, _ := out["messages"].([]map[string]any)
	if out["matched"] != 1 || len(msgs) != 1 || msgs[0]["uid"] != "2" {
		t.Fatalf("result = %v", out)
	}
	if msgs[0]["subject"] != "Faktura za wrzesień" {
		t.Fatalf("subject not decoded: %q", msgs[0]["subject"])
	}
	// The union still asked the server all three keys.
	var text, subject, from bool
	for _, crit := range fake.searches {
		upper := strings.ToUpper(crit)
		if strings.Contains(upper, "TEXT") {
			text = true
		}
		if strings.Contains(upper, "SUBJECT") {
			subject = true
		}
		if strings.Contains(upper, "FROM") {
			from = true
		}
	}
	if !text || !subject || !from {
		t.Fatalf("server union incomplete: %v", fake.searches)
	}
	if out["scanned_recent"] != 5 || out["messages_in_range"] != 5 {
		t.Fatalf("completeness counters = %v", out)
	}
}

func TestSearchSortSliceAndCounters(t *testing.T) {
	_, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	out, err := c.SearchMail(context.Background(), "e", "INBOX", 2, nil, nil)
	if err != nil {
		t.Fatalf("SearchMail: %v", err)
	}
	msgs, _ := out["messages"].([]map[string]any)
	if out["matched"] != 5 || len(msgs) != 2 || msgs[0]["uid"] != "5" || msgs[1]["uid"] != "4" {
		t.Fatalf("result = %v (want newest 2 of 5 matched)", out)
	}
}

func TestReadMailPlain(t *testing.T) {
	_, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	out, err := c.ReadMail(context.Background(), "1", "INBOX", false)
	if err != nil {
		t.Fatalf("ReadMail: %v", err)
	}
	if out["from"] != "boss@example.com" || out["subject"] != "Q3 planning" {
		t.Fatalf("headers = %v", out)
	}
	if body, _ := out["body"].(string); !strings.Contains(body, "See you Thursday") {
		t.Fatalf("body = %q", body)
	}
	if out["unread"] != false || out["answered"] != false {
		t.Fatalf("flags = %v", out)
	}
	if out["body_truncated"] != false || out["warning"] == "" {
		t.Fatalf("result = %v", out)
	}
	if names, _ := out["attachments"].([]string); len(names) != 0 {
		t.Fatalf("attachments = %v", names)
	}
}

func TestReadMailHtmlOnly(t *testing.T) {
	_, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	out, err := c.ReadMail(context.Background(), "3", "INBOX", false)
	if err != nil {
		t.Fatalf("ReadMail: %v", err)
	}
	body, _ := out["body"].(string)
	if !strings.Contains(body, "Boarding pass") || !strings.Contains(body, "Gate 42") {
		t.Fatalf("body = %q", body)
	}
	if strings.Contains(body, "<") || strings.Contains(body, "alert(1)") {
		t.Fatalf("body leaks markup or script: %q", body)
	}
	if out["answered"] != true {
		t.Fatalf("answered = %v", out["answered"])
	}
}

func TestReadMailAttachmentsSave(t *testing.T) {
	_, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	out, err := c.ReadMail(context.Background(), "4", "INBOX", true)
	if err != nil {
		t.Fatalf("ReadMail: %v", err)
	}
	names, _ := out["attachments"].([]string)
	if len(names) != 2 {
		t.Fatalf("attachments = %v", names)
	}
	saved, _ := out["attachments_saved"].([]map[string]any)
	if len(saved) != 1 || saved[0]["name"] != "statement.pdf" {
		t.Fatalf("saved = %v", saved)
	}
	path, _ := saved[0]["path"].(string)
	if !strings.HasPrefix(path, c.attachDir+string(os.PathSeparator)+"4"+string(os.PathSeparator)) {
		t.Fatalf("path escapes uid dir: %s", path)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("saved file mode = %v, err = %v", info.Mode(), err)
	}
	skipped, _ := out["attachments_skipped"].([]map[string]any)
	if len(skipped) != 1 || skipped[0]["name"] != "evil.exe" {
		t.Fatalf("skipped = %v", skipped)
	}
	if reason, _ := skipped[0]["reason"].(string); !strings.Contains(reason, "denylist") {
		t.Fatalf("reason = %q", reason)
	}
	// Without the flag, names are listed but nothing is written.
	out, err = c.ReadMail(context.Background(), "4", "INBOX", false)
	if err != nil {
		t.Fatalf("ReadMail: %v", err)
	}
	if saved, _ := out["attachments_saved"].([]map[string]any); len(saved) != 0 {
		t.Fatalf("unsolicited save: %v", saved)
	}
}

func TestReadMailNestedMultipart(t *testing.T) {
	_, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	out, err := c.ReadMail(context.Background(), "5", "INBOX", false)
	if err != nil {
		t.Fatalf("ReadMail: %v", err)
	}
	if body, _ := out["body"].(string); !strings.Contains(body, "Inner body text.") {
		t.Fatalf("nested body lost: %q", body)
	}
	if names, _ := out["attachments"].([]string); len(names) != 1 || names[0] != "n.txt" {
		t.Fatalf("nested attachments = %v", names)
	}
}

func TestReadMailUnknownUID(t *testing.T) {
	_, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	out, err := c.ReadMail(context.Background(), "99", "INBOX", false)
	if err != nil {
		t.Fatalf("ReadMail: %v", err)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "no message with uid 99") {
		t.Fatalf("error = %v", out)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":          "passwd",
		`..\\windows\\system.dll`:   "system.dll",
		"  ...  ":                   "attach",
		"statement (final) 2.pdf":   "statement (final) 2.pdf",
		"résumé.pdf":                "rsum.pdf",
		"a/b\\c:d*e?f\"g<h>i|j.pdf": "cdefghij.pdf",
	} {
		if got := safeName(in, "attach"); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 200) + ".pdf"
	if got := safeName(long, "attach"); len(got) != 120 {
		t.Errorf("long name kept %d chars, want 120", len(got))
	}
}

func TestSaveAttachmentsTraversalAndSize(t *testing.T) {
	_, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	parts := []mailPart{
		{isAttachment: true, filename: "../../evil.txt", contentType: "text/plain", body: []byte("x")},
		{isAttachment: true, filename: "big.pdf", contentType: "application/pdf", body: make([]byte, attachMax+1)},
	}
	saved, skipped := c.saveAttachments(parts, "9")
	if len(saved) != 1 || saved[0]["name"] != "evil.txt" {
		t.Fatalf("saved = %v", saved)
	}
	if path, _ := saved[0]["path"].(string); !strings.HasPrefix(path, c.attachDir) || strings.Contains(path, "..") {
		t.Fatalf("path escapes: %s", path)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v", skipped)
	}
	if reason, _ := skipped[0]["reason"].(string); !strings.Contains(reason, "25 MB cap") {
		t.Fatalf("reason = %q", reason)
	}
}

func TestExpireAttachments(t *testing.T) {
	_, addr := newFakeIMAP(t)
	c := testClient(t, addr)
	c.now = func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }
	oldDir := filepath.Join(c.attachDir, "old")
	freshDir := filepath.Join(c.attachDir, "fresh")
	os.MkdirAll(oldDir, 0o755)
	os.MkdirAll(freshDir, 0o755)
	oldFile := filepath.Join(oldDir, "a.pdf")
	freshFile := filepath.Join(freshDir, "b.pdf")
	os.WriteFile(oldFile, []byte("old"), 0o640)
	os.WriteFile(freshFile, []byte("fresh"), 0o640)
	old := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	os.Chtimes(oldFile, old, old)
	os.Chtimes(freshFile, fresh, fresh)
	if got := c.expireAttachments(); got != 1 {
		t.Fatalf("expired %d, want 1", got)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("old file kept")
	}
	if _, err := os.Stat(freshFile); err != nil {
		t.Fatalf("fresh file swept: %v", err)
	}
}

func TestHtmlToText(t *testing.T) {
	got := htmlToText(`<html><head><title>T</title><style>.a{}</style></head><body><h1>Hi</h1><p>Line<br>two</p><script>x()</script></body></html>`)
	if got != "Hi\nLine\ntwo" {
		t.Fatalf("got %q", got)
	}
	if got := htmlToText(`Fish &amp; Chips`); got != "Fish & Chips" {
		t.Fatalf("entities: %q", got)
	}
}

func TestDecodeHeader(t *testing.T) {
	if got := decodeHeader("=?UTF-8?B?RmFrdHVyYSB6YSB3cnplc2llxYQ=?="); got != "Faktura za wrzesień" {
		t.Fatalf("B-encoded: %q", got)
	}
	if got := decodeHeader("=?UTF-8?Q?za=C5=BC=C3=B3=C5=82=C4=87?="); got != "zażółć" {
		t.Fatalf("Q-encoded: %q", got)
	}
	if got := decodeHeader("  plain  "); got != "plain" {
		t.Fatalf("plain: %q", got)
	}
}

func withSMTPSink(t *testing.T, c *Client) *fakeSMTP {
	t.Helper()
	sink, addr := newFakeSMTP(t)
	c.dialSMTP = func(string) (smtpConn, error) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return nil, err
		}
		cl := smtpclient.NewClient(conn)
		if err := cl.Hello("localhost"); err != nil {
			conn.Close()
			return nil, err
		}
		return cl, nil
	}
	return sink
}

func TestSendMailApproved(t *testing.T) {
	fakeIMAP, addr := newFakeIMAP(t)
	_ = fakeIMAP
	c := testClient(t, addr)
	sink := withSMTPSink(t, c)
	var question string
	c.Ask = func(_ context.Context, q string) string {
		question = q
		return ""
	}
	out, err := c.SendMail(context.Background(), "friend@example.com", "Hello", "short body")
	if err != nil {
		t.Fatalf("SendMail: %v", err)
	}
	if out["sent"] != true || out["tell_the_owner"] != "Sent mail to friend@example.com: Hello" {
		t.Fatalf("result = %v", out)
	}
	for _, want := range []string{"To: friend@example.com", "Subject: Hello", "short body"} {
		if !strings.Contains(question, want) {
			t.Fatalf("question lacks %q:\n%s", want, question)
		}
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.from != "test@example.com" || len(sink.to) != 1 || sink.to[0] != "friend@example.com" {
		t.Fatalf("envelope = %q %v", sink.from, sink.to)
	}
	if !strings.Contains(sink.data, "short body") {
		t.Fatalf("data = %q", sink.data)
	}
}

func TestSendMailDeclinedSendsNothing(t *testing.T) {
	fakeIMAP, addr := newFakeIMAP(t)
	_ = fakeIMAP
	c := testClient(t, addr)
	sink := withSMTPSink(t, c)
	c.Ask = func(_ context.Context, _ string) string { return "not approved (decline)" }
	out, err := c.SendMail(context.Background(), "friend@example.com", "Hello", "body")
	if err != nil {
		t.Fatalf("SendMail: %v", err)
	}
	if out["sent"] != false {
		t.Fatalf("result = %v", out)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.data != "" {
		t.Fatalf("declined send reached the sink: %q", sink.data)
	}
}

func TestSendMailLongBodyPreview(t *testing.T) {
	fakeIMAP, addr := newFakeIMAP(t)
	_ = fakeIMAP
	c := testClient(t, addr)
	_ = withSMTPSink(t, c)
	var question string
	c.Ask = func(_ context.Context, q string) string {
		question = q
		return ""
	}
	long := strings.Repeat("x", 900)
	if _, err := c.SendMail(context.Background(), "a@b.c", "S", long); err != nil {
		t.Fatalf("SendMail: %v", err)
	}
	if !strings.Contains(question, "[...truncated in preview]") {
		t.Fatalf("long body not marked truncated")
	}
	if strings.Contains(question, long) {
		t.Fatalf("full long body leaked into the question")
	}
}

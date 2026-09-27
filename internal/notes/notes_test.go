package notes

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
)

// The regression vectors, verbatim: the failure modes this converter
// exists for, executable.
func TestMarkdownVectors(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1. first\n* a detail\n1. second\n* its detail\n",
			"<ol><li>first<ol><li>a detail</li></ol></li>" +
				"<li>second<ol><li>its detail</li></ol></li></ol>"},
		{"1. first\n  - a\n  - b\n2. second\n",
			"<ol><li>first<ol><li>a</li><li>b</li></ol></li>" +
				"<li>second</li></ol>"},
		{"- a\n  - b\n", "<ul><li>a<ul><li>b</li></ul></li></ul>"},
		{"- a\n\n- b\n", "<ul><li>a</li><li>b</li></ul>"},
		{"- a\n\ntext\n", "<ul><li>a</li></ul><p>text</p>"},
		{"- a\n  - b\n    - c\n- d\n",
			"<ul><li>a<ul><li>b<ul><li>c</li></ul></li></ul></li><li>d</li></ul>"},
		{"- a\n- b\n\n1. x\n2. y\n",
			"<ul><li>a</li><li>b</li></ul><ol><li>x</li><li>y</li></ol>"},
		{"1. first\n   and more words\n2. second\n",
			"<ol><li>first and more words</li><li>second</li></ol>"},
		{"- a\n    - b\n  - c\n",
			"<ul><li>a<ul><li>b</li></ul></li><li>c</li></ul>"},
		{"`**x**`\n", "<p><code>**x**</code></p>"},
		{"[x](http://a/_b_c)\n", `<p><a href="http://a/_b_c">x</a></p>`},
		{"# Title\n", "<h1>Title</h1>"},
		{"##### deep\n", "<h3>deep</h3>"},
		{"> quoted\n", "<blockquote>quoted</blockquote>"},
		{"```\nx = 1\n```\n", "<pre>x = 1</pre>"},
		{"**bold** and _it_\n", "<p><b>bold</b> and <i>it</i></p>"},
		{"<script>alert(1)</script>\n",
			"<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>"},
	}
	for _, tc := range cases {
		if got := MarkdownToHTML(tc.in); got != tc.want {
			t.Errorf("MarkdownToHTML(%q)\n got: %s\nwant: %s", tc.in, got, tc.want)
		}
	}
}

func TestMarkdownQuoteInURL(t *testing.T) {
	out := MarkdownToHTML("[t](http://x\"onload=y)\n")
	if !strings.Contains(out, "&quot;") {
		t.Fatalf("quotes not escaped: %s", out)
	}
	if strings.Contains(out, "onload=") && !strings.Contains(out, "&quot;onload") {
		t.Fatalf("href breakout: %s", out)
	}
}

func TestSameTitle(t *testing.T) {
	// Truncated row matches the full first line.
	if !sameTitle("Apple Watch experimental f…", "Apple Watch experimental feature ships today") {
		t.Error("truncated row should match")
	}
	if sameTitle("Groceries", "Dentist at noon") {
		t.Error("different titles must not match")
	}
	if sameTitle("[icloud-mcp verify] 20260925-1125", "[icloud-mcp verify] 20260925-1132-probe2") {
		t.Fatal("a shared prefix is not the same title")
	}
	if !sameTitle("Groceries", "  groceries ") {
		t.Fatal("case and spacing do not change a title")
	}
	if sameTitle("", "Anything") {
		t.Error("empty row must not match")
	}
}

func TestTitleMatches(t *testing.T) {
	if !titleMatches("Apple Watch experimental f…", "Apple Watch experimental feature ships today") {
		t.Error("truncated row should match by containment")
	}
	if titleMatches("Groceries", "Dentist") {
		t.Error("different titles must not match")
	}
	if titleMatches("Hi", "Hello there friend") {
		t.Error("under-six-characters must not match")
	}
}

func TestApprovalOrErrorRoutesByFailure(t *testing.T) {
	approval := approvalOrError(&browser.NeedsApprovalError{Msg: "waiting"})
	if approval["needs_device_approval"] != true {
		t.Fatalf("lapsed grant should carry needs_device_approval: %v", approval)
	}
	login := approvalOrError(&browser.SignedOutError{Msg: "expired"})
	if login["needs_login"] != true {
		t.Fatalf("expired session should carry needs_login: %v", login)
	}
	if _, ok := login["needs_device_approval"]; ok {
		t.Fatalf("expired session must not carry needs_device_approval: %v", login)
	}
}

// fxNoteZlib is a real TextDataEncrypted, probed 2026-09-26 from the
// marker note "[icloud-mcp verify] 20260925-1125" (zlib-framed, as the web
// editor writes it).
const fxNoteZlib = "eJzjYBD6zsjBIMAg9YFRyD06MzknvzRFNze5QKEstSgzrTJWwcjAyMzA0shU19DQyJSrtCAlsSQ1RaEkoyi/ND0DSKcquPj76oJVZwIlChJLMqQEuFhAZgJNBdMajGARRgFLASUpEM2gwSQlBBRhAqqxBItwKDBqMENVRQuoQlWxSIlxcQBN+A8E/EDT4GwlSy5pLoFXmxSSvTLmumjdPO68ZMecTiFmjgZGISYOTi4pLoGvz59LXn16vuGrst1OkwNpfUBxSyBm1GLhUNJg1GLiUAUAR048WQ=="

// pb builds protobuf bytes for fixtures: tag/value pairs where a value is
// a uint64 (varint), a string, or nested []any.
func pb(fields ...any) []byte {
	var out []byte
	varint := func(v uint64) {
		for v >= 0x80 {
			out = append(out, byte(v)|0x80)
			v >>= 7
		}
		out = append(out, byte(v))
	}
	for i := 0; i < len(fields); i += 2 {
		num := uint64(fields[i].(int))
		switch v := fields[i+1].(type) {
		case int:
			varint(num << 3)
			varint(uint64(v))
		case string:
			varint(num<<3 | 2)
			varint(uint64(len(v)))
			out = append(out, v...)
		case []byte:
			varint(num<<3 | 2)
			varint(uint64(len(v)))
			out = append(out, v...)
		}
	}
	return out
}

func wrapNote(t *testing.T, note []byte, gz bool) string {
	t.Helper()
	root := pb(1, 0, 2, pb(1, 0, 2, 0, 3, note))
	var buf bytes.Buffer
	if gz {
		w := gzip.NewWriter(&buf)
		w.Write(root)
		w.Close()
	} else {
		w := zlib.NewWriter(&buf)
		w.Write(root)
		w.Close()
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestDecodeNoteBodyReal(t *testing.T) {
	body, err := decodeNoteBody(fxNoteZlib)
	if err != nil {
		t.Fatal(err)
	}
	if body.text != "[icloud-mcp verify] 20260925-1125\nupdated through the DOM-verified path" {
		t.Fatalf("text = %q", body.text)
	}
	if len(body.checklist) != 0 || len(body.attachments) != 0 {
		t.Fatalf("plain note has checklist %v attachments %v", body.checklist, body.attachments)
	}
}

func TestDecodeNoteBodyChecklistAttachmentGzip(t *testing.T) {
	// Apple's layout: runs (Note field 5) with a length in UTF-16 units,
	// paragraph style (2) whose style_type (1) 103 is a checklist line and
	// whose checklist (5) carries done (2); attachment_info is field 12.
	text := "Trip ✈\npassport\ncharger\nsocks\n￼\n"
	run := func(n, style, done int) []byte {
		ps := pb(1, style)
		if style == styleChecklist {
			ps = pb(1, style, 5, pb(1, "uuid", 2, done))
		}
		return pb(1, n, 2, ps)
	}
	note := pb(2, text,
		5, run(7, 0, 0), // "Trip ✈\n": ✈ is one UTF-16 unit
		5, run(9, styleChecklist, 0),
		5, run(8, styleChecklist, 1),
		5, run(6, styleChecklist, 0),
		5, pb(1, 1, 12, pb(1, "ATT-1", 2, "public.jpeg")),
		5, run(1, 0, 0))
	for _, gz := range []bool{true, false} {
		body, err := decodeNoteBody(wrapNote(t, note, gz))
		if err != nil {
			t.Fatal(err)
		}
		want := []map[string]any{{"text": "passport", "done": false}, {"text": "charger", "done": true}, {"text": "socks", "done": false}}
		if len(body.checklist) != 3 {
			t.Fatalf("gzip=%v checklist = %v", gz, body.checklist)
		}
		for i := range want {
			if body.checklist[i]["text"] != want[i]["text"] || body.checklist[i]["done"] != want[i]["done"] {
				t.Errorf("gzip=%v item %d = %v, want %v", gz, i, body.checklist[i], want[i])
			}
		}
		if len(body.attachments) != 1 || body.attachments[0]["type"] != "public.jpeg" || body.attachments[0]["id"] != "ATT-1" {
			t.Errorf("attachments = %v", body.attachments)
		}
		if strings.ContainsRune(body.text, '\uFFFC') || !strings.Contains(body.text, attachmentMark) {
			t.Error("the attachment placeholder leaked into text")
		}
	}
}

func TestPageText(t *testing.T) {
	long := strings.Repeat("é", textPage+10)
	first := pageText(long, 0)
	if first["truncated"] != true || first["next_offset"] != textPage || first["total_length"] != textPage+10 {
		t.Fatalf("first = %v", first)
	}
	rest := pageText(long, textPage)
	if rest["truncated"] != false || rest["next_offset"] != nil || rest["text"] != strings.Repeat("é", 10) {
		t.Fatalf("rest = %v", rest)
	}
}

func TestNoteRowFromRecord(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	c := &Client{owner: loc}
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	row := c.noteRow(noteRec{N: "N1", Title: enc("Groceries"), Snip: enc("milk, eggs"), Cr: 1790406000000, Md: 1790409600000,
		Folder: "F1"}, map[string]string{"F1": "Home"})
	for k, want := range map[string]any{"id": "N1", "title": "Groceries", "snippet": "milk, eggs", "folder": "Home",
		"created": "2026-09-26T09:00:00+02:00", "modified": "2026-09-26T10:00:00+02:00"} {
		if row[k] != want {
			t.Errorf("%s = %#v, want %#v", k, row[k], want)
		}
	}
}

func TestCheckIDProofRefusesAnotherNote(t *testing.T) {
	c := &Client{idProof: "Plan\nbuy milk\n[attachment]"}
	if refused := c.checkIDProof("Plan\n  buy   milk\n￼"); refused != nil {
		t.Fatalf("the same note with other whitespace refused: %v", refused)
	}
	if refused := c.checkIDProof("Plan B\nsomething else"); refused == nil {
		t.Fatal("a different note sharing the title prefix was accepted")
	}
	if (&Client{}).checkIDProof("anything") != nil {
		t.Fatal("a title-only update must not be gated")
	}
}

func TestPbFieldsRejectsHugeLength(t *testing.T) {
	// Field 1, wire type 2, a length varint of 2^63: must error, not panic.
	b := []byte{0x0a, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}
	if _, err := pbFields(b); err == nil {
		t.Fatal("an overflowing length was accepted")
	}
}

// The page and the record, read from the same real note on the Linux rig
// (2026-09-26): notes_read by title and the decoded TextDataEncrypted must
// satisfy the id proof, or update_note by id could never write.
func TestIDProofRealPageRecordPair(t *testing.T) {
	body, err := decodeNoteBody(fxNoteZlib)
	if err != nil {
		t.Fatal(err)
	}
	page := "[icloud-mcp verify] 20260925-1125\nupdated through the DOM-verified path"
	if refused := (&Client{idProof: body.text}).checkIDProof(page); refused != nil {
		t.Fatalf("the same note refused: %v", refused)
	}
}

func TestJoinIDsOnlyWhenUnique(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	folders := map[string]string{"F1": enc("Work"), "F2": enc("Homework"), trashFolder: enc("Recently Deleted")}
	notes := []noteRec{
		{N: "A", Title: enc("Plan"), Folder: "F1", Md: 1},
		{N: "B", Title: enc("Plan"), Folder: "F2", Md: 1},
		{N: "C", Title: enc("Dup"), Folder: "F1"},
		{N: "D", Title: enc("Dup"), Folder: "F1"},
		{N: "E", Title: enc("Gone"), Folder: trashFolder},
	}
	rows := []map[string]any{
		{"title": "Plan", "folder": "Work"}, {"title": "Plan", "folder": " Homework "},
		{"title": "Dup", "folder": "Work"}, {"title": "Gone", "folder": "Recently Deleted"},
		{"title": "Pla", "folder": "Work"},
	}
	(&Client{owner: time.UTC}).joinIDs(notes, folders, rows)
	want := []any{"A", "B", nil, nil, nil}
	for i, row := range rows {
		if row["id"] != want[i] {
			t.Errorf("row %d (%v) got id %v, want %v", i, row["title"], row["id"], want[i])
		}
	}
}

// Checklist items use the web editor's own clipboard format, the only one
// it pastes as a checklist (paragraph style 103 with a todo). Anything
// else lands as a bulleted list and loses the done state.
func TestChecklistUsesEditorFormat(t *testing.T) {
	html := MarkdownToHTML("- [ ] todo\n  - [x] done\n")
	item := regexp.MustCompile(`<li><p><span data-tt="\{&quot;paragraphStyle&quot;:\{&quot;style&quot;:103,&quot;todo&quot;:\{&quot;todoUUID&quot;:\{(&quot;\d+&quot;:\d+,?){16}\},&quot;done&quot;:(true|false)\}\}\}" style="white-space: pre-wrap;">(todo|done)\n</span></p>`)
	got := item.FindAllStringSubmatch(html, -1)
	if len(got) != 2 || got[0][2] != "false" || got[0][3] != "todo" || got[1][2] != "true" || got[1][3] != "done" {
		t.Fatalf("checklist html = %s", html)
	}
	if !strings.Contains(html, "</p><ul><li><p>") {
		t.Fatalf("the nested item is not inside its parent's item: %s", html)
	}
	if a, b := MarkdownToHTML("- [ ] x\n"), MarkdownToHTML("- [ ] x\n"); a == b {
		t.Fatal("two items share a todoUUID; each needs its own")
	}
}

func TestChecklistBodyKeepsLinesInInternalFormat(t *testing.T) {
	html := MarkdownToHTML("Intro line.\n\n# Heading\n\n- [ ] one\n- bullet\n")
	for _, want := range []string{
		`<p><span data-tt="{}" style="white-space: pre-wrap;">Intro line.\n</span></p>`,
		`&quot;style&quot;:1}}" style="white-space: pre-wrap;">Heading\n</span></p>`,
		`&quot;style&quot;:100}}" style="white-space: pre-wrap;">bullet\n</span></p>`,
	} {
		if want = strings.ReplaceAll(want, `\n`, "\n"); !strings.Contains(html, want) {
			t.Errorf("missing %s in %s", want, html)
		}
	}
	if plain := MarkdownToHTML("Intro line.\n- bullet\n"); strings.Contains(plain, "data-tt") {
		t.Fatalf("a body without a checklist must keep plain HTML: %s", plain)
	}
}

func TestMatchNoteTitlesMirrorsPageRules(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	folders := map[string]string{"F1": "Work", "F2": "Homework", trashFolder: "Recently Deleted"}
	notes := []noteRec{
		{N: "old", Title: enc("Plan A"), Folder: "F1", Md: 1},
		{N: "new", Title: enc("Plan B"), Folder: "F2", Md: 9},
		{N: "trash", Title: enc("Plan C"), Folder: trashFolder, Md: 5},
		{N: "del", Title: enc("Plan D"), Folder: "F1", Md: 6, Del: true},
		{N: "other", Title: enc("Groceries"), Folder: "F1", Md: 3},
	}
	ids := func(rs []noteRec) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.N)
		}
		return strings.Join(out, ",")
	}
	if hits, _ := matchNoteTitles(notes, folders, "plan", nil); ids(hits) != "new,old" {
		t.Errorf("plan = %s, want newest first, trash and deleted excluded", ids(hits))
	}
	work := "work"
	if hits, _ := matchNoteTitles(notes, folders, "plan", &work); ids(hits) != "old" {
		t.Errorf("plan in work = %s, want the exact folder only (not Homework)", ids(hits))
	}
	if hits, ok := matchNoteTitles(notes, folders, "pancakes", nil); len(hits) != 0 || !ok {
		t.Errorf("miss = %v, resolved %v", ids(hits), ok)
	}
	nope := "Nowhere"
	if _, ok := matchNoteTitles(notes, folders, "plan", &nope); ok {
		t.Error("an unknown folder resolved; the page must answer it with the folder list")
	}
}

func TestChecklistContinuationAndFencesAndTitle(t *testing.T) {
	html := MarkdownToHTML("- [ ] buy milk\n  and eggs\n")
	if !strings.Contains(html, "buy milk and eggs\n</span></p>") {
		t.Fatalf("continuation left its item's span: %s", html)
	}
	if strings.Contains(MarkdownToHTML("```\n- [ ] not a checklist\n```\n- a\n  - b\n"), "data-tt") {
		t.Fatal("a checklist line inside a code fence switched the body to the internal format")
	}
	if got := TitleHTML("Plan", "- [ ] x\n"); !strings.Contains(got, "&quot;style&quot;:0}}") || !strings.HasSuffix(got, "Plan\n</span></p>") {
		t.Fatalf("title ahead of a checklist body = %s", got)
	}
	if got := TitleHTML("Plan", "plain"); got != "<h1>Plan</h1>" {
		t.Fatalf("title ahead of a plain body = %s", got)
	}
}

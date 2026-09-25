package notes

import (
	"strings"
	"testing"

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
		{"- [ ] todo\n  - [x] done\n",
			`<ul><li data-checked="false">todo` +
				`<ul><li data-checked="true">done</li></ul></li></ul>`},
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

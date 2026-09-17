package notes

import (
	"regexp"
	"strconv"
	"strings"
)

// Markdown to the narrow HTML subset Apple Notes keeps when pasted in.
// Hand-written, not a dependency: the output must be the subset the
// editor keeps, so a full CommonMark implementation would mostly produce
// tags to strip.

var (
	mdBold1  = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdBold2  = regexp.MustCompile(`__([^_]+)__`)
	mdStrike = regexp.MustCompile(`~~([^~]+)~~`)
	mdCode   = regexp.MustCompile("`([^`]+)`")
	mdLink   = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s)]+)\)`)
	mdStash  = regexp.MustCompile("\x00(\\d+)\x00")

	mdCheck  = regexp.MustCompile(`^(\s*)[-*+]\s+\[([ xX])\]\s+(.*)$`)
	mdBullet = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	mdNumber = regexp.MustCompile(`^(\s*)\d+[.)]\s+(.*)$`)
	mdHead   = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdQuote  = regexp.MustCompile(`^\s*>\s?(.*)$`)
)

func indentWidth(raw string) int {
	expanded := strings.ReplaceAll(raw, "\t", "    ")
	return len(expanded) - len(strings.TrimLeft(expanded, " "))
}

// Escape escapes the five HTML metacharacters, quotes included: a URL is
// interpolated into href="...", and note content is never trusted.
func Escape(text string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;",
		`"`, "&quot;", `'`, "&#39;")
	return r.Replace(text)
}

// inline escapes first, then applies inline markdown, so note content
// cannot inject markup. Code spans and links park behind a placeholder so
// emphasis cannot reach inside either.
func inline(text string) string {
	out := Escape(text)
	var stash []string
	park := func(html string) string {
		stash = append(stash, html)
		return "\x00" + strconv.Itoa(len(stash)-1) + "\x00"
	}
	out = mdCode.ReplaceAllStringFunc(out, func(m string) string {
		return park("<code>" + mdCode.FindStringSubmatch(m)[1] + "</code>")
	})
	out = mdLink.ReplaceAllStringFunc(out, func(m string) string {
		g := mdLink.FindStringSubmatch(m)
		return park(`<a href="` + g[2] + `">` + g[1] + `</a>`)
	})
	for _, rule := range []struct {
		re   *regexp.Regexp
		open string
	}{
		{mdBold1, "b"}, {mdBold2, "b"}, {mdStrike, "s"},
	} {
		open := rule.open
		out = rule.re.ReplaceAllString(out, "<"+open+">$1</"+open+">")
	}
	// Single-star/underscore italics need boundary checks (not after a
	// marker or word char, not before a marker) that RE2 cannot express
	// with lookarounds, so they scan by hand in the same position.
	out = applyItalics(out, '*')
	out = applyItalics(out, '_')
	return mdStash.ReplaceAllStringFunc(out, func(m string) string {
		var i int
		for _, d := range mdStash.FindStringSubmatch(m)[1] {
			i = i*10 + int(d-'0')
		}
		return stash[i]
	})
}

func isWordByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= 0x80
}

// applyItalics marks single-marker italics with the same boundary rules
// as `(?<![marker\w])marker([^marker\n]+)marker(?!marker)`: the opener must
// not follow a marker or word character, the content holds no marker or
// newline, and the closer must not precede another marker.
func applyItalics(s string, marker byte) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != marker {
			out.WriteByte(s[i])
			i++
			continue
		}
		if i > 0 && (s[i-1] == marker || isWordByte(s[i-1])) {
			out.WriteByte(s[i])
			i++
			continue
		}
		j := i + 1
		for j < len(s) && s[j] != marker && s[j] != '\n' {
			j++
		}
		if j >= len(s) || s[j] != marker || (j+1 < len(s) && s[j+1] == marker) || j == i+1 {
			out.WriteByte(s[i])
			i++
			continue
		}
		out.WriteString("<i>" + s[i+1:j] + "</i>")
		i = j + 1
	}
	return out.String()
}

type listFrame struct {
	indent  int
	tag     string
	marker  string
	liOpen  bool
	virtual bool
}

// MarkdownToHTML converts the deliberately small subset Apple Notes can
// represent: headings, paragraphs, nested bullet and numbered lists,
// checklists, block quotes, fenced code, and the inline marks. No tables
// and no images: Notes has its own table control and an image is an
// attachment, so dropping them is honest where faking is not.
func MarkdownToHTML(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var html []string
	var stack []listFrame
	inCode := false
	var code []string
	blankPending := false

	closeLI := func() {
		if len(stack) > 0 && stack[len(stack)-1].liOpen {
			html = append(html, "</li>")
			stack[len(stack)-1].liOpen = false
		}
	}
	closeOne := func() {
		closeLI()
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		html = append(html, "</"+top.tag+">")
	}
	closeLists := func() {
		for len(stack) > 0 {
			closeOne()
		}
	}
	nestedTag := func(tag string) string {
		// Probed on the live app: a <ul> nested inside an <ol> item makes
		// Apple split the numbered list and start again at 1. A nested
		// <ol> keeps the parent counting.
		if len(stack) > 0 && stack[len(stack)-1].tag == "ol" {
			return "ol"
		}
		return tag
	}
	push := func(indent int, marker string, virtual bool) {
		tag := nestedTag(marker)
		html = append(html, "<"+tag+">")
		stack = append(stack, listFrame{indent: indent, tag: tag, marker: marker, virtual: virtual})
	}
	item := func(indent int, tag, inner string, separated bool, checked string) {
		dedented := false
		for len(stack) > 0 && indent < stack[len(stack)-1].indent {
			closeOne()
			dedented = true
		}
		for len(stack) > 0 && indent == stack[len(stack)-1].indent &&
			stack[len(stack)-1].virtual && stack[len(stack)-1].marker != tag {
			closeOne()
		}
		if len(stack) == 0 || (indent > stack[len(stack)-1].indent && !dedented) {
			push(indent, tag, false)
		} else if stack[len(stack)-1].marker != tag {
			// Switching marker at the same indent means a sub-point, not
			// a second list: speech does not indent. A blank line is the
			// one signal that says otherwise.
			if stack[len(stack)-1].liOpen && !separated {
				push(indent, tag, true)
			} else {
				closeOne()
				push(indent, tag, false)
			}
		} else {
			closeLI()
		}
		attr := ""
		if checked != "" {
			attr = ` data-checked="` + checked + `"`
		}
		html = append(html, "<li"+attr+">"+inner)
		stack[len(stack)-1].liOpen = true
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inCode {
				html = append(html, "<pre>"+Escape(strings.Join(code, "\n"))+"</pre>")
				code, inCode = nil, false
			} else {
				closeLists()
				inCode = true
			}
			blankPending = false
			continue
		}
		if inCode {
			code = append(code, raw)
			continue
		}
		if strings.TrimSpace(line) == "" {
			// Deferred: a loose list separates items with blank lines.
			blankPending = true
			continue
		}
		if m := mdCheck.FindStringSubmatch(line); m != nil {
			checked := "false"
			if strings.ToLower(m[2]) == "x" {
				checked = "true"
			}
			item(indentWidth(m[1]), "ul", inline(m[3]), blankPending, checked)
			blankPending = false
			continue
		}
		if m := mdBullet.FindStringSubmatch(line); m != nil {
			item(indentWidth(m[1]), "ul", inline(m[2]), blankPending, "")
			blankPending = false
			continue
		}
		if m := mdNumber.FindStringSubmatch(line); m != nil {
			item(indentWidth(m[1]), "ol", inline(m[2]), blankPending, "")
			blankPending = false
			continue
		}
		// A line indented past the open item continues that item.
		if len(stack) > 0 && stack[len(stack)-1].liOpen && indentWidth(raw) > stack[len(stack)-1].indent {
			html = append(html, " "+inline(strings.TrimSpace(line)))
			blankPending = false
			continue
		}
		blankPending = false
		closeLists()
		if m := mdHead.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			if level > 3 {
				level = 3
			}
			html = append(html, "<h"+strconv.Itoa(level)+">"+inline(m[2])+"</h"+strconv.Itoa(level)+">")
			continue
		}
		if m := mdQuote.FindStringSubmatch(line); m != nil {
			html = append(html, "<blockquote>"+inline(m[1])+"</blockquote>")
			continue
		}
		html = append(html, "<p>"+inline(line)+"</p>")
	}
	if inCode && len(code) > 0 {
		html = append(html, "<pre>"+Escape(strings.Join(code, "\n"))+"</pre>")
	}
	closeLists()
	return strings.Join(html, "")
}

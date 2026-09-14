#!/usr/bin/env python3
"""Markdown to the narrow HTML subset Apple Notes keeps when it is pasted in.

Its own module, and importable with nothing installed, because the browser stack next door needs
Playwright and a converter that cannot be exercised without a logged-in Apple session is a
converter nobody exercises. `python3 markdown.py --self-test` is the whole test suite.

Written by hand rather than pulled in as a dependency: the output has to be the subset the editor
keeps, so a full CommonMark implementation would mostly produce tags to strip.
"""
import re
import sys

# Emphasis only. Code spans and links are lifted out before these run, because running them in
# sequence does not protect anything: `**x**` inside a code span still became bold, and an
# underscore in a URL still became an <i> tag inside the href, which puts mismatched markup on the
# clipboard.
_MD_INLINE = (
    (re.compile(r"\*\*([^*]+)\*\*"), r"<b>\1</b>"),
    (re.compile(r"__([^_]+)__"), r"<b>\1</b>"),
    (re.compile(r"(?<![*\w])\*([^*\n]+)\*(?!\*)"), r"<i>\1</i>"),
    (re.compile(r"(?<![_\w])_([^_\n]+)_(?!_)"), r"<i>\1</i>"),
    (re.compile(r"~~([^~]+)~~"), r"<s>\1</s>"),
)
_CODE_SPAN = re.compile(r"`([^`]+)`")
_LINK = re.compile(r"\[([^\]]+)\]\((https?://[^\s)]+)\)")
_STASHED = re.compile(r"\x00(\d+)\x00")

_CHECK = re.compile(r"^(\s*)[-*+]\s+\[([ xX])\]\s+(.*)$")
_BULLET = re.compile(r"^(\s*)[-*+]\s+(.*)$")
_NUMBER = re.compile(r"^(\s*)\d+[.)]\s+(.*)$")
_HEADING = re.compile(r"^(#{1,6})\s+(.*)$")
_QUOTE = re.compile(r"^\s*>\s?(.*)$")


def _indent(raw: str) -> int:
    """Leading width in columns, with tabs at four, so tabs and spaces nest the same."""
    expanded = raw.expandtabs(4)
    return len(expanded) - len(expanded.lstrip())


def _esc(text: str) -> str:
    """Escape the five, quotes included.

    Quotes matter because a URL is interpolated into `href="..."`: without them
    `[t](http://x"onload=y)` closes the attribute and the rest parses as a real event handler. Note
    content can come from a mail the owner pasted or from a dictation, so it is never trusted.
    """
    return (text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
            .replace('"', "&quot;").replace("'", "&#39;"))


# The same escaping, under a name another module may use: a title typed into HTML needs it
# exactly as a body line does.
escape = _esc


def _inline(text: str) -> str:
    """Escape first, then apply inline markdown, so note content cannot inject markup.

    Code spans and links are converted first and parked behind a placeholder, so emphasis cannot
    reach inside either: a backticked `**x**` stays literal, and an underscore in a URL stays an
    underscore instead of becoming a tag in the href.
    """
    out = _esc(text)
    stash: list[str] = []

    def park(html: str) -> str:
        stash.append(html)
        return f"\x00{len(stash) - 1}\x00"

    out = _CODE_SPAN.sub(lambda m: park(f"<code>{m.group(1)}</code>"), out)
    out = _LINK.sub(lambda m: park(f'<a href="{m.group(2)}">{m.group(1)}</a>'), out)
    for pattern, repl in _MD_INLINE:
        out = pattern.sub(repl, out)
    return _STASHED.sub(lambda m: stash[int(m.group(1))], out)


def markdown_to_html(body: str) -> str:
    """A deliberately small markdown subset, covering what Apple Notes can actually represent.

    Headings, paragraphs, nested bullet and numbered lists, checklists, block quotes, fenced code
    and the inline marks. No tables and no images: Notes has its own table control and an image is
    an attachment rather than markup, so silently dropping them is honest where faking them is not.

    Lists nest by indentation and a sublist lives inside its parent item, which is what makes the
    numbering continue. A flat converter closed the list on every switch between `1.` and `-`, so a
    dictation with sub-points came out of Apple Notes numbered "1. 1. 1." with its detail lines
    promoted to top level. Observed on a voice note on 2026-09-11.
    """
    lines = body.replace("\r\n", "\n").split("\n")
    html: list[str] = []
    stack: list[dict] = []          # one entry per open list: indent, tag, whether its li is open
    in_code = False
    code: list[str] = []
    blank_pending = False           # a blank line inside a list does not end the list

    def close_li() -> None:
        if stack and stack[-1]["li_open"]:
            html.append("</li>")
            stack[-1]["li_open"] = False

    def close_one() -> None:
        close_li()
        html.append(f"</{stack.pop()['tag']}>")

    def close_lists() -> None:
        while stack:
            close_one()

    def push(indent: int, marker: str, virtual: bool) -> None:
        tag = _nested_tag(marker)
        html.append(f"<{tag}>")
        stack.append({"indent": indent, "tag": tag, "marker": marker,
                      "li_open": False, "virtual": virtual})

    def _nested_tag(tag: str) -> str:
        """Which tag a sublist inside the open item must use.

        Probed on the live app on 2026-09-12: a `<ul>` nested inside an `<ol>` item makes Apple
        split the numbered list and start again at 1, which is the exact complaint this module was
        opened for. A nested `<ol>` keeps the parent counting, so a sub-point under a numbered item
        is numbered rather than bulleted. The marker changes; the numbering survives, and the
        numbering is what carries meaning.
        """
        return "ol" if stack and stack[-1]["tag"] == "ol" else tag

    def item(indent: int, tag: str, inner: str, separated: bool,
             checked: str | None = None) -> None:
        dedented = False
        while stack and indent < stack[-1]["indent"]:
            close_one()
            dedented = True
        # A sub-point dictated at the same indent ends when the parent list's own marker returns.
        while (stack and indent == stack[-1]["indent"] and stack[-1]["virtual"]
               and stack[-1]["marker"] != tag):
            close_one()
        if not stack or (indent > stack[-1]["indent"] and not dedented):
            # A deeper list opens inside the item above it, so the parent's <li> stays open and
            # the outer list keeps counting. After a dedent the indent may match no open level at
            # all, and rejoining the level below beats opening a second list inside one item.
            push(indent, tag, False)
        elif stack[-1]["marker"] != tag:
            # Switching marker at the same indent. Speech does not indent, so "1. item" followed by
            # "* detail" means a sub-point, not a second list: nest it inside the open item rather
            # than ending the numbering. That is the whole bug this module was split out for.
            # A blank line is the one signal that says otherwise, because dictation never pauses:
            # written markdown that separates two lists that way gets two lists.
            if stack[-1]["li_open"] and not separated:
                push(indent, tag, True)
            else:
                close_one()
                push(indent, tag, False)
        else:
            close_li()
        attr = f' data-checked="{checked}"' if checked else ""
        html.append(f"<li{attr}>{inner}")
        stack[-1]["li_open"] = True

    for raw in lines:
        line = raw.rstrip()
        if line.strip().startswith("```"):
            if in_code:
                html.append("<pre>" + _esc("\n".join(code)) + "</pre>")
                code, in_code = [], False
            else:
                close_lists()
                in_code = True
            blank_pending = False
            continue
        if in_code:
            code.append(raw)
            continue

        if not line.strip():
            # Deferred: a loose list separates its items with blank lines, and closing here is what
            # made "1." restart. Anything that is not another list item closes the lists below.
            blank_pending = True
            continue

        if (m := _CHECK.match(line)):
            # A markdown checkbox is a checklist item in Notes, which is a real control there.
            item(len(m.group(1).expandtabs(4)), "ul", _inline(m.group(3)), blank_pending,
                 "true" if m.group(2).lower() == "x" else "false")
            blank_pending = False
            continue

        if (m := _BULLET.match(line)):
            item(len(m.group(1).expandtabs(4)), "ul", _inline(m.group(2)), blank_pending)
            blank_pending = False
            continue

        if (m := _NUMBER.match(line)):
            item(len(m.group(1).expandtabs(4)), "ol", _inline(m.group(2)), blank_pending)
            blank_pending = False
            continue

        # A line indented past the open item, and not a marker of its own, continues that item.
        # Closing every list here is what made "1." restart mid-list when a dictation wrapped one
        # point over two lines.
        if stack and stack[-1]["li_open"] and _indent(raw) > stack[-1]["indent"]:
            html.append(" " + _inline(line.strip()))
            blank_pending = False
            continue

        # Everything below ends any open list.
        blank_pending = False
        close_lists()

        if (m := _HEADING.match(line)):
            # Notes has Title, Heading and Subheading, so anything deeper than three collapses.
            level = min(len(m.group(1)), 3)
            html.append(f"<h{level}>{_inline(m.group(2))}</h{level}>")
            continue

        if (m := _QUOTE.match(line)):
            html.append(f"<blockquote>{_inline(m.group(1))}</blockquote>")
            continue

        html.append(f"<p>{_inline(line)}</p>")

    if in_code and code:
        html.append("<pre>" + _esc("\n".join(code)) + "</pre>")
    close_lists()
    return "".join(html)


def self_test() -> None:
    # The failure this module was split out for: a dictation with sub-points under numbered items.
    out = markdown_to_html("1. first\n* a detail\n1. second\n* its detail\n")
    assert out == ("<ol><li>first<ol><li>a detail</li></ol></li>"
                   "<li>second<ol><li>its detail</li></ol></li></ol>"), out

    # Indented sublists, which is how the contracts ask for them.
    out = markdown_to_html("1. first\n  - a\n  - b\n2. second\n")
    assert out == ("<ol><li>first<ol><li>a</li><li>b</li></ol></li>"
                   "<li>second</li></ol>"), out

    # A sub-point under a numbered item nests as a numbered list, because Apple splits the
    # parent list when it is a <ul>. Bullets under a bulleted item stay bullets.
    assert markdown_to_html("- a\n  - b\n") == "<ul><li>a<ul><li>b</li></ul></li></ul>"

    # A blank line between items is a loose list, not two lists.
    assert markdown_to_html("- a\n\n- b\n") == "<ul><li>a</li><li>b</li></ul>"
    # ... but a paragraph after one really does end it.
    assert markdown_to_html("- a\n\ntext\n") == "<ul><li>a</li></ul><p>text</p>"

    # Three levels, and the deepest closes back out in order.
    out = markdown_to_html("- a\n  - b\n    - c\n- d\n")
    assert out == "<ul><li>a<ul><li>b<ul><li>c</li></ul></li></ul></li><li>d</li></ul>", out

    # Checklists keep their control, and their nesting.
    out = markdown_to_html("- [ ] todo\n  - [x] done\n")
    assert out == ('<ul><li data-checked="false">todo'
                   '<ul><li data-checked="true">done</li></ul></li></ul>'), out

    # Two lists separated by a blank line are two lists, not one nested in the other. A dictation
    # never pauses, so the blank line is the only thing that can say so.
    out = markdown_to_html("- a\n- b\n\n1. x\n2. y\n")
    assert out == "<ul><li>a</li><li>b</li></ul><ol><li>x</li><li>y</li></ol>", out

    # A point wrapped over two lines stays one item, and the numbering carries on past it.
    out = markdown_to_html("1. first\n   and more words\n2. second\n")
    assert out == "<ol><li>first and more words</li><li>second</li></ol>", out

    # A dedent to an indent that never opened a list rejoins the level below rather than opening
    # a second list inside one item.
    out = markdown_to_html("- a\n    - b\n  - c\n")
    assert out == "<ul><li>a<ul><li>b</li></ul></li><li>c</li></ul>", out

    # A quote in a URL cannot break out of the href and become an attribute.
    out = markdown_to_html('[t](http://x"onload=y)\n')
    assert 'onload' not in out.replace("&quot;onload", ""), out
    assert "&quot;" in out, out

    # Emphasis never reaches inside a code span or a URL.
    assert markdown_to_html("`**x**`\n") == "<p><code>**x**</code></p>"
    out = markdown_to_html("[x](http://a/_b_c)\n")
    assert out == '<p><a href="http://a/_b_c">x</a></p>', out

    # Headings, quotes, code and the inline marks still behave.
    assert markdown_to_html("# Title\n") == "<h1>Title</h1>"
    assert markdown_to_html("##### deep\n") == "<h3>deep</h3>"
    assert markdown_to_html("> quoted\n") == "<blockquote>quoted</blockquote>"
    assert markdown_to_html("```\nx = 1\n```\n") == "<pre>x = 1</pre>"
    assert markdown_to_html("**bold** and _it_\n") == "<p><b>bold</b> and <i>it</i></p>"

    # Content cannot inject markup: escaping happens before the inline pass.
    assert markdown_to_html("<script>alert(1)</script>\n") == (
        "<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>")

    # A list that ends the body closes everything it opened.
    assert markdown_to_html("- a\n  - b\n").count("</ul>") == 2
    print("markdown self-test ok")


if __name__ == "__main__":
    if "--self-test" in sys.argv:
        self_test()
    else:
        print(markdown_to_html(sys.stdin.read()))

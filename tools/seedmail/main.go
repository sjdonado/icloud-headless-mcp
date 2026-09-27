// seedmail appends synthetic mail to the test account's INBOX, so the mail
// tools can be measured against a realistic mailbox: long reply threads,
// attachments of several types and sizes, HTML-only newsletters, encoded
// subjects, and mixed flags and dates. Every subject starts with "[seed]".
//
//	set -a; . ./.env; set +a; go run ./tools/seedmail          # append
//	set -a; . ./.env; set +a; go run ./tools/seedmail -dry-run # count only
//
// It only appends: it never deletes, moves or flags existing mail.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"mime"
	netmail "net/mail"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type msg struct {
	raw   []byte
	flags []imap.Flag
	at    time.Time
}

var people = []string{"Dana Müller <dana@example.com>", "Ravi Patel <ravi@example.org>", "Lin Chen <lin@example.net>",
	"Olu Adeyemi <olu@example.com>", "Sofía Ramos <sofia@example.org>", "Bank Notices <no-reply@bank.example>"}

func mid(tag string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("<seed-%s-%x@seed.example>", tag, b)
}

func header(from, to, subject, id string, at time.Time, extra ...string) string {
	// Display names go through net/mail, which MIME-encodes non-ASCII
	// ones as a real client does.
	if a, err := netmail.ParseAddress(from); err == nil {
		from = a.String()
	}
	h := []string{
		"From: " + from, "To: " + to, "Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"Date: " + at.Format(time.RFC1123Z), "Message-ID: " + id, "MIME-Version: 1.0",
	}
	return strings.Join(append(h, extra...), "\r\n") + "\r\n"
}

func textBody(s string) string {
	return "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + strings.ReplaceAll(s, "\n", "\r\n") + "\r\n"
}

func b64(data []byte) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.String()
}

type part struct {
	name, ctype string
	data        []byte
}

func mixed(text string, parts []part) string {
	boundary := "seed-mixed-" + fmt.Sprint(time.Now().UnixNano())
	var b strings.Builder
	b.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")
	b.WriteString("--" + boundary + "\r\n" + textBody(text))
	for _, p := range parts {
		b.WriteString("--" + boundary + "\r\nContent-Type: " + p.ctype + "; name=\"" + p.name + "\"\r\n" +
			"Content-Disposition: attachment; filename=\"" + p.name + "\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(p.data))
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

func filler(n int, seed string) []byte {
	var b bytes.Buffer
	for b.Len() < n {
		b.WriteString(seed)
	}
	return b.Bytes()[:n]
}

func build(owner string, now time.Time) []msg {
	var out []msg
	day := func(d int) time.Time { return now.AddDate(0, 0, -d).Add(-time.Duration(d%7) * time.Hour) }

	// Long reply threads: each reply quotes the whole thread so far.
	topics := []string{"Offsite planning for October", "Budget review Q4", "Apartment move checklist",
		"Wedding logistics", "API migration plan", "Book club: next pick"}
	for ti, topic := range topics {
		var ids []string
		quoted := ""
		replies := 6 + ti*2
		for r := 0; r < replies; r++ {
			at := day(40 - ti*5).Add(time.Duration(r) * 3 * time.Hour)
			from := people[(ti+r)%5]
			subject := "[seed] " + topic
			if r > 0 {
				subject = "[seed] Re: " + topic
			}
			id := mid(fmt.Sprintf("t%d-%d", ti, r))
			var extra []string
			if len(ids) > 0 {
				extra = append(extra, "In-Reply-To: "+ids[len(ids)-1], "References: "+strings.Join(ids, " "))
			}
			line := fmt.Sprintf("Reply %d of %d on %q.\nPoint %d: we should settle this by Friday, and I added notes below.\n", r+1, replies, topic, r+1)
			body := line + strings.Repeat("Some more context on the plan, with details that make the message long enough to page. ", 4+r) + "\n"
			if quoted != "" {
				body += "\n" + quoted
			}
			quoted = "> " + strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n> ") + "\n"
			flags := []imap.Flag{imap.FlagSeen}
			if r == replies-1 {
				flags = nil // the newest reply of each thread is unread
			}
			if r%4 == 3 {
				flags = append(flags, imap.FlagAnswered)
			}
			out = append(out, msg{raw: []byte(header(from, owner, subject, id, at, extra...) + textBody(body)), flags: flags, at: at})
			ids = append(ids, id)
		}
	}

	// Attachments: several types, several at once, one large, one on the
	// deny list so read_mail's skip path has something to report.
	atts := []struct {
		subject string
		parts   []part
	}{
		{"Invoice 2026-091", []part{{"invoice-2026-091.pdf", "application/pdf", filler(48_000, "%PDF-1.4 seed ")}}},
		{"Holiday photos", []part{{"beach.jpg", "image/jpeg", filler(350_000, "JFIFseed")}, {"sunset.png", "image/png", filler(220_000, "PNGseed!")}}},
		{"Quarterly numbers", []part{{"q3.csv", "text/csv", filler(12_000, "month,revenue,cost\n")}, {"q3-summary.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", filler(30_000, "PKseedxl")}}},
		{"Signed contract", []part{{"contract.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", filler(64_000, "PKseeddoc")}}},
		{"Project archive", []part{{"project.zip", "application/zip", filler(90_000, "PKzipseed")}}},
		{"Scanned manual (large)", []part{{"manual-scan.pdf", "application/pdf", filler(2_500_000, "%PDF-1.7 large seed ")}}},
		{"Calendar invite attached", []part{{"invite.ics", "text/calendar", []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n")}}},
		{"Voice memo", []part{{"memo.m4a", "audio/mp4", filler(180_000, "ftypM4A seed")}}},
	}
	for i, a := range atts {
		at := day(30 - i*3)
		id := mid(fmt.Sprintf("a%d", i))
		body := fmt.Sprintf("Attached: %d file(s) for %q. Let me know if anything is missing.", len(a.parts), a.subject)
		flags := []imap.Flag{imap.FlagSeen}
		if i%3 == 0 {
			flags = append(flags, imap.FlagFlagged)
		}
		out = append(out, msg{raw: []byte(header(people[i%6], owner, "[seed] "+a.subject, id, at) + mixed(body, a.parts)), flags: flags, at: at})
	}

	// HTML-only newsletters with a heavy head, and an alternative pair.
	style := "<style>" + strings.Repeat(".c{color:#333;margin:0 auto;font-family:Helvetica}", 200) + "</style>"
	for i := 0; i < 6; i++ {
		at := day(20 - i*3)
		html := "<html><head>" + style + "</head><body><h1>Weekly digest " + fmt.Sprint(i+1) + "</h1><p>Top stories this week: " +
			strings.Repeat("an item worth reading, ", 30) + "</p><p><a href=\"https://example.com/unsubscribe\">Unsubscribe</a></p></body></html>"
		out = append(out, msg{raw: []byte(header("Digest <digest@news.example>", owner, fmt.Sprintf("[seed] Weekly digest #%d", i+1), mid(fmt.Sprintf("n%d", i)), at) +
			"Content-Type: text/html; charset=utf-8\r\n\r\n" + html + "\r\n"), flags: nil, at: at})
	}
	boundary := "seed-alt"
	alt := "Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n--" + boundary + "\r\n" +
		textBody("Your order has shipped. Tracking: SEED123.") + "--" + boundary + "\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p>Your order has <b>shipped</b>. Tracking: SEED123.</p>\r\n--" + boundary + "--\r\n"
	out = append(out, msg{raw: []byte(header("Shop <orders@shop.example>", owner, "[seed] Your order has shipped", mid("alt"), day(2)) + alt), at: day(2)})

	// Encoded and non-Latin subjects, for the decoded local search pass.
	for i, s := range []string{"Faktura za wrzesień", "Réunion de lundi", "会议记录 9月", "Счёт за сентябрь"} {
		at := day(10 + i)
		out = append(out, msg{raw: []byte(header(people[(i+2)%5], owner, "[seed] "+s, mid(fmt.Sprintf("u%d", i)), at) + textBody("Seeded message: "+s)),
			flags: []imap.Flag{imap.FlagSeen}, at: at})
	}
	return out
}

func main() {
	dry := flag.Bool("dry-run", false, "build and count the messages without appending")
	mailbox := flag.String("mailbox", "INBOX", "mailbox to append to")
	flag.Parse()
	user, pass := os.Getenv("ICLOUD_APPLE_ID"), os.Getenv("ICLOUD_APP_PASSWORD")
	msgs := build(user, time.Now())
	total := 0
	for _, m := range msgs {
		total += len(m.raw)
	}
	fmt.Printf("%d messages, %.1f MB\n", len(msgs), float64(total)/1e6)
	if *dry {
		return
	}
	if user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "ICLOUD_APPLE_ID and ICLOUD_APP_PASSWORD must be in the environment")
		os.Exit(1)
	}
	cl, err := imapclient.DialTLS("imap.mail.me.com:993", &imapclient.Options{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer cl.Close()
	if err := cl.Login(user, pass).Wait(); err != nil {
		fmt.Fprintln(os.Stderr, "login failed")
		os.Exit(1)
	}
	for i, m := range msgs {
		cmd := cl.Append(*mailbox, int64(len(m.raw)), &imap.AppendOptions{Flags: m.flags, Time: m.at})
		if _, err := cmd.Write(m.raw); err != nil {
			fmt.Fprintln(os.Stderr, "append", i, err)
			os.Exit(1)
		}
		if err := cmd.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "append", i, err)
			os.Exit(1)
		}
		if _, err := cmd.Wait(); err != nil {
			fmt.Fprintln(os.Stderr, "append", i, err)
			os.Exit(1)
		}
	}
	_ = cl.Logout().Wait()
	fmt.Printf("appended %d messages to %s\n", len(msgs), *mailbox)
}

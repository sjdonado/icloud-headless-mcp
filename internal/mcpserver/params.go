package mcpserver

import "github.com/mark3labs/mcp-go/mcp"

// Param is one tool argument as clients see it in tools/list. Without
// these a client sees an empty input schema and cannot know that
// create_event needs a summary; the handlers stay the authority on
// validation.
type Param struct {
	Name     string
	Type     string // "string", "number", "boolean"
	Required bool
	Desc     string
}

func req(name, typ, desc string) Param { return Param{name, typ, true, desc} }
func opt(name, typ, desc string) Param { return Param{name, typ, false, desc} }

const (
	isoTime   = "ISO 8601, e.g. 2026-10-01T10:00; a time without an offset is read in the owner's zone"
	eventUID  = "the event uid, from list_events"
	calName   = "calendar name, from list_calendars"
	noteTitle = "the note title, as notes_list or notes_search shows it"
	noteMatch = "which of several notes with this title, from the candidates an ambiguous call returns"
	listName  = "reminder list name, from reminder_lists"
	mailbox   = "mailbox name, from list_mailboxes; default INBOX"
	mailUID   = "the message uid, from list_mail or search_mail"
	since     = "first day, YYYY-MM-DD, inclusive"
	until     = "last day, YYYY-MM-DD, inclusive"
)

// ToolParams maps each tool name to its arguments. A tool absent here
// takes none.
var ToolParams = map[string][]Param{
	"list_events": {
		opt("days_ahead", "number", "days after now to include; default 7"),
		opt("days_back", "number", "days before now to include; default 0"),
		opt("start", "string", "first day, YYYY-MM-DD; with end, replaces days_ahead and days_back"),
		opt("end", "string", "last day, YYYY-MM-DD, inclusive"),
		opt("calendar", "string", "only this calendar ("+calName+")"),
	},
	"create_event": {
		req("summary", "string", "the event title"),
		req("start", "string", "start, "+isoTime),
		opt("end", "string", "end, "+isoTime+"; default one hour after start"),
		opt("calendar", "string", calName+"; default AGENT_DEFAULT_CALENDAR"),
		opt("location", "string", "where"),
		opt("description", "string", "notes on the event"),
		opt("timezone", "string", "IANA zone for naive times; default the owner's zone"),
	},
	"update_event": {
		req("uid", "string", eventUID),
		opt("summary", "string", "new title"),
		opt("start", "string", "new start, "+isoTime),
		opt("end", "string", "new end, "+isoTime),
		opt("location", "string", "new location"),
		opt("description", "string", "new notes"),
		opt("timezone", "string", "IANA zone for naive times; default the owner's zone"),
	},
	"delete_event":    {req("uid", "string", eventUID)},
	"search_contacts": {req("query", "string", "name, email or phone to find"), opt("limit", "number", "default 10")},
	"list_mail": {
		opt("mailbox", "string", mailbox),
		opt("limit", "number", "default 10"),
		opt("unread_only", "boolean", "default false"),
		opt("since", "string", since),
		opt("until", "string", until),
	},
	"read_mail": {
		req("uid", "string", mailUID),
		opt("mailbox", "string", mailbox),
		opt("save_attachments", "boolean", "save allowed attachments to MAIL_ATTACHMENTS_DIR; default false"),
	},
	"search_mail": {
		req("query", "string", "text to find in sender, subject or body"),
		opt("mailbox", "string", mailbox),
		opt("limit", "number", "default 10"),
		opt("since", "string", since),
		opt("until", "string", until),
	},
	"send_mail": {
		req("to", "string", "recipient address"),
		req("subject", "string", "subject line"),
		req("body", "string", "plain-text body"),
	},
	"notes_list": {opt("folder", "string", "only this folder, from notes_folders"), opt("limit", "number", "default 25")},
	"notes_read": {
		req("title", "string", noteTitle),
		opt("folder", "string", "narrow to this folder"),
		opt("match", "number", noteMatch),
	},
	"notes_search": {req("query", "string", "text to find in titles and bodies"), opt("limit", "number", "default 15")},
	"update_note": {
		req("title", "string", noteTitle),
		req("body", "string", "the new full body; it replaces the old one"),
		opt("folder", "string", "narrow to this folder"),
		opt("match", "number", noteMatch),
	},
	"create_note": {
		req("title", "string", "the new note's title (its first line)"),
		opt("body", "string", "the text below the title"),
		opt("folder", "string", "folder to create it in, from notes_folders"),
	},
	"list_reminders": {req("list_name", "string", listName)},
	"completed_reminders": {
		opt("list_name", "string", "only this list, from reminder_lists"),
		opt("since", "string", "completed on or after this day, YYYY-MM-DD"),
		opt("until", "string", "completed on or before this day, YYYY-MM-DD"),
		opt("limit", "number", "default 50"),
	},
	"complete_reminder": {
		req("title", "string", "the reminder title, as list_reminders shows it"),
		req("list_name", "string", listName),
	},
	"create_reminder": {
		req("title", "string", "the reminder title"),
		req("list_name", "string", listName),
		opt("due", "string", "YYYY-MM-DD, or YYYY-MM-DD HH:MM on a 24-hour clock in the owner's zone"),
	},
	"health_days":     {opt("days", "number", "1 to 366; default 14")},
	"health_sleep":    {opt("nights", "number", "1 to 366; default 14")},
	"health_effort":   {opt("days", "number", "1 to 366; default 14"), opt("floor_bpm", "number", "heart-rate floor; default 100")},
	"health_recovery": {opt("recent", "number", "recent window in days; default 7"), opt("baseline", "number", "baseline window in days; default 28")},
	"health_sql":      {req("query", "string", "one read-only SELECT")},
}

// toolOptions turns a tool's params into mcp-go tool options.
func toolOptions(name string) []mcp.ToolOption {
	var out []mcp.ToolOption
	for _, p := range ToolParams[name] {
		props := []mcp.PropertyOption{mcp.Description(p.Desc)}
		if p.Required {
			props = append(props, mcp.Required())
		}
		switch p.Type {
		case "number":
			out = append(out, mcp.WithNumber(p.Name, props...))
		case "boolean":
			out = append(out, mcp.WithBoolean(p.Name, props...))
		default:
			out = append(out, mcp.WithString(p.Name, props...))
		}
	}
	return out
}

package mcpserver

// Output schemas, one per tool, declared next to ToolParams. They describe
// the success payload so a structured-content client knows each field's
// type. No field is required and additionalProperties stays true: a
// failure (isError) shares the tool, and a field added later must not
// break a validating client. The README examples are checked against
// these in server_test.go.

type schema = map[string]any

func obj(props schema) schema {
	return schema{"type": "object", "properties": props, "additionalProperties": true}
}

func arr(items schema) schema { return schema{"type": "array", "items": items} }

func orNull(s schema) schema {
	out := schema{}
	for k, v := range s {
		out[k] = v
	}
	out["type"] = []string{s["type"].(string), "null"}
	return out
}

var (
	tStr  = schema{"type": "string"}
	tNum  = schema{"type": "number"}
	tInt  = schema{"type": "integer"}
	tBool = schema{"type": "boolean"}
	tAny  = schema{}
	nStr  = orNull(tStr)
	nNum  = orNull(tNum)
	strs  = arr(tStr)

	// tISO documents the one date format: ISO 8601 with the offset, in
	// the owner's zone, or YYYY-MM-DD for an all-day date.
	tISO = schema{"type": "string", "description": "ISO 8601 with offset in the owner's zone; YYYY-MM-DD when all-day"}

	person  = obj(schema{"name": tStr, "address": tStr})
	address = obj(schema{"name": tStr, "address": tStr})

	event = obj(schema{
		"uid": tStr, "calendars": strs, "summary": tStr, "start": tISO, "end": tISO, "all_day": tBool,
		"location": tStr, "description": tStr, "status": tStr, "url": tStr, "organizer": person,
		"attendees":       arr(obj(schema{"name": tStr, "address": tStr, "role": tStr, "participation": tStr})),
		"recurrence_rule": tStr, "recurring_instance": tBool,
		"alarms":  arr(obj(schema{"action": tStr, "offset": tStr, "related": tStr, "at": tISO})),
		"created": tISO, "last_modified": tISO,
	})

	typed   = obj(schema{"value": tStr, "type": tStr})
	contact = obj(schema{
		"name": tStr, "nickname": tStr, "organization": tStr, "job_title": tStr, "birthday": tStr,
		"emails": arr(typed), "phones": arr(typed), "urls": strs, "notes": tStr,
		"addresses": arr(obj(schema{"type": tStr, "street": tStr, "extended": tStr, "po_box": tStr, "city": tStr,
			"region": tStr, "postal_code": tStr, "country": tStr})),
	})

	attachment  = obj(schema{"name": tStr, "mime_type": tStr, "size": tInt})
	mailMessage = schema{
		"uid": tStr, "mailbox": tStr, "from": arr(address), "to": arr(address), "cc": arr(address), "reply_to": arr(address),
		"subject": tStr, "message_id": tStr, "in_reply_to": tStr,
		"flags": obj(schema{"seen": tBool, "answered": tBool, "flagged": tBool, "draft": tBool}),
		"size":  tInt, "date": tISO, "date_raw": tStr, "internal_date": tISO,
		"has_attachments": tBool, "attachments": arr(attachment), "snippet": tStr,
	}
	bounds = obj(schema{"since": nStr, "until": nStr})

	paged = schema{"offset": tInt, "total_length": tInt, "truncated": tBool, "next_offset": tInt}

	reminder = obj(schema{
		"id": nStr, "list": tStr, "title": tStr, "notes": tStr, "due": tISO, "due_display": nStr, "all_day": tBool,
		"priority": nStr, "flagged": tBool, "completed": tBool, "completed_at": tISO, "created": tISO, "modified": tISO,
		"alarms": arr(obj(schema{"type": tStr, "at": tISO})), "recurring": tBool, "tag_count": tInt,
	})
	ckSync = arr(obj(schema{"zone": tStr, "pass": tStr, "changed": tInt, "removed": tInt, "seconds": tNum, "error": tAny}))

	noteRow = obj(schema{"id": tStr, "title": tStr, "folder": tStr, "snippet": tStr, "created": tISO, "modified": tISO,
		"modified_display": tStr})

	// write is the loose shape of a write tool's result: what it did and
	// what to tell the owner, with fields that vary by outcome.
	write = writeWith(nil)

	healthUnits = schema{"type": "object", "additionalProperties": nStr}
)

func writeWith(props schema) schema {
	return obj(withFields(schema{"tell_the_owner": tStr, "refused": tStr, "queued": tBool, "warning": tStr}, props))
}

func withFields(base schema, extra schema) schema {
	out := schema{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// ToolOutputs maps each tool to its output schema.
var ToolOutputs = map[string]schema{
	"list_calendars": obj(schema{"calendars": strs, "timezone": tStr, "note": tStr}),
	"list_events": obj(schema{"count": tInt, "events": arr(event),
		"window": obj(schema{"start": tISO, "end": tISO, "absolute": tBool})}),
	"create_event":    writeWith(schema{"uid": tStr, "calendar": tStr}),
	"update_event":    writeWith(schema{"uid": tStr, "calendar": tStr}),
	"delete_event":    writeWith(schema{"deleted": tBool, "uid": tStr}),
	"search_contacts": obj(schema{"count": tInt, "contacts": arr(contact)}),

	"list_mailboxes": obj(schema{"count": tInt, "mailboxes": strs, "note": tStr}),
	"list_mail": obj(schema{"mailbox": tStr, "count": tInt, "matched": tInt, "bounds": bounds,
		"messages": arr(obj(mailMessage))}),
	"read_mail": obj(withFields(withFields(mailMessage, paged), schema{"body": tStr, "body_format": tStr,
		"attachments_saved": arr(tAny), "attachments_skipped": arr(tAny), "attachments_expired": tInt, "warning": tStr})),
	"search_mail": obj(schema{"query": tStr, "count": tInt, "matched": tInt, "scanned_recent": tInt, "messages_in_range": tInt, "local_pass_error": tStr,
		"bounds": bounds, "messages": arr(obj(mailMessage))}),
	"send_mail": write,

	"notes_folders": obj(schema{"count": tInt, "folders": strs}),
	"notes_list": obj(schema{"source": tStr, "folder": tStr, "count": tInt, "total": tInt, "partial": tBool,
		"notes": arr(noteRow), "note": tStr}),
	"notes_read": obj(withFields(paged, schema{"id": tStr, "title": tStr, "folder": tStr, "created": tISO, "modified": tISO,
		"modified_display": tStr, "text": tStr, "source": tStr, "warning": tStr,
		"checklist":   arr(obj(schema{"text": tStr, "done": tBool})),
		"attachments": arr(obj(schema{"id": tStr, "type": tStr}))})),
	"notes_search": obj(schema{"query": tStr, "count": tInt, "notes": arr(noteRow), "scope": tStr, "warning": tStr}),
	"update_note":  writeWith(schema{"updated": tBool, "title": tStr}),
	"create_note":  writeWith(schema{"created": tAny, "title": tStr}),

	"reminder_lists": obj(schema{"count": tInt, "lists": strs}),
	"list_reminders": obj(schema{"source": tStr, "lists": strs, "count": tInt, "with_due_date": tInt, "with_priority": tInt,
		"reminders": arr(reminder), "sync": ckSync, "unavailable": strs, "note": tStr}),
	"completed_reminders": obj(schema{"total_in_window": tInt, "sync": ckSync, "note": tStr,
		"window": obj(schema{"since": nStr, "until": nStr, "timezone": tStr}),
		"lists":  arr(obj(schema{"list": tStr, "completed_total": tInt, "returned": tInt, "completed": arr(reminder)}))}),
	"complete_reminder": writeWith(schema{"completed": tBool, "id": nStr, "title": tStr}),
	"create_reminder":   writeWith(schema{"created": tAny, "title": tStr}),

	"drive_status": obj(schema{}),
	"reask_access": obj(schema{"reasked": tBool}),
	"open_login":   obj(schema{"door": tStr, "url": tStr}),
	"sign_out":     obj(schema{"signed_out": tBool}),

	"health_status": obj(schema{"unconfigured": tBool, "detail": tStr, "timezone": nStr, "stale": tBool,
		"newest_sample_date": tStr, "days_since_newest": tInt, "metrics_present_count": tInt, "unapplied_tombstones": tInt,
		"metrics": schema{"type": "object", "additionalProperties": obj(schema{"samples": tInt, "first_date": nStr,
			"last_date": nStr, "latest_recorded_at": nStr, "unit": nStr, "sources": strs})},
		"missing":     arr(obj(schema{"rollup": tStr, "reason": tStr, "folder_hints": strs})),
		"last_import": orNull(obj(schema{})), "last_sample_import": orNull(obj(schema{}))}),
	"health_days": obj(schema{"window_days": tInt, "units": healthUnits, "sources": strs, "note": tStr,
		"metrics_used": schema{"type": "object", "additionalProperties": strs},
		"days": arr(obj(schema{"date": tStr, "steps": nNum, "active_energy": nNum, "resting_bpm": nNum, "hrv": nNum,
			"heart_rate": orNull(obj(schema{"min": tNum, "max": tNum, "avg": tNum, "samples": tInt}))}))}),
	"health_sleep": obj(schema{"window_nights": tInt, "skipped_segments": tInt, "sources": strs, "note": tStr,
		"nights": arr(obj(schema{"night": tStr, "onset": tISO, "wake": tISO, "total_minutes": tNum,
			"asleep_minutes": tNum, "awake_minutes": nNum, "in_bed_minutes": nNum, "segments": tInt,
			"stages": schema{"type": "object", "additionalProperties": tNum}}))}),
	"health_effort": obj(schema{"floor_bpm": tInt, "window_days": tInt, "unit": nStr, "sources": strs,
		"days": arr(obj(schema{"date": tStr, "minutes_above_floor": nNum, "max_bpm": nNum, "avg_bpm": nNum,
			"samples": tInt, "skipped_samples": tInt}))}),
	"health_recovery": obj(schema{"recent_days": tInt, "baseline_days": tInt, "note": tStr,
		"recent_window": obj(schema{"start": tStr, "end": tStr}), "baseline_window": obj(schema{"start": tStr, "end": tStr}),
		"resting_bpm": recoveryPair, "hrv": recoveryPair}),
	"health_sql": obj(schema{"columns": strs, "rows": arr(obj(schema{})), "truncated": tBool}),
}

var recoveryPair = obj(schema{"recent": nNum, "baseline": nNum, "delta": nNum, "delta_percent": tNum,
	"recent_days": tInt, "baseline_days": tInt, "unit": nStr, "note": tStr})

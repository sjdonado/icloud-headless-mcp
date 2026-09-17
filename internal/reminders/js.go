package reminders

import "fmt"

// Selector and flow snippets for the Reminders web app, probed on the live
// app. Copied verbatim with the Python implementation; the DOM is the
// contract here, not the language.
//
// The flow snippets share one shadow-DOM walker, interpolated where each
// snippet holds a %s. snippetTests in reminders_test.go guards the
// interpolation: a stray % anywhere else in a snippet would corrupt it.

const rowsJS = `() => {
  const walk = (root, depth, fn) => {
    if (depth > 16) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, depth + 1, fn);
      fn(el);
    }
  };
  const items = [];
  walk(document, 0, (el) => {
    if ((el.className || '').toString().split(/\s+/).includes('reminder-item')) items.push(el);
  });
  return items.map((it) => {
    const leaves = [];
    walk(it, 0, (el) => {
      const text = (el.innerText || '').trim();
      if (text && el.children.length === 0) {
        leaves.push({cls: (el.className || '').toString().trim(), text});
      }
    });
    let title = '';
    walk(it, 0, (el) => {
      if (!title && (el.className || '').toString().split(/\s+/).includes('content-title')) {
        title = (el.innerText || '').trim();
      }
    });
    if (!title) {
      const first = (it.innerText || '').split('\n').map((t) => t.trim()).filter(Boolean)[0];
      title = first || '';
    }
    let priorityText = '', priorityLevel = '';
    walk(it, 0, (el) => {
      if (priorityText) return;
      if ((el.className || '').toString().split(/\s+/).includes('priority')) {
        priorityText = (el.innerText || '').trim();
        priorityLevel = el.getAttribute('data-priority-level') || '';
      }
    });
    if (priorityText && title.startsWith(priorityText)) {
      title = title.slice(priorityText.length).trim();
    }
    let flagged = false;
    walk(it, 0, (el) => {
      if ((el.className || '').toString().split(/\s+/).includes('flag-container')) {
        flagged = flagged || el.children.length > 0;
      }
    });
    let dueText = '';
    walk(it, 0, (el) => {
      if (!dueText && (el.className || '').toString().split(/\s+/).includes('due-date')) {
        dueText = (el.innerText || '').trim();
      }
    });
    return {
      id: it.id || '',
      title,
      leaves: leaves.map((l) => l.text),
      dueText,
      completed: it.getAttribute('data-completed') === 'true',
      priorityText,
      priorityLevel,
      flagged,
    };
  });
}`

const ckZonesJS = `async () => {
  const c = CloudKit.getDefaultContainer();
  const out = [{scope: 'private', zoneName: 'Reminders', owner: null}];
  try {
    const s = await c.sharedCloudDatabase.fetchAllRecordZones();
    for (const z of (s.zones || [])) if (z.zoneID.zoneName === 'Reminders')
      out.push({scope: 'shared', zoneName: 'Reminders', owner: z.zoneID.ownerRecordName});
  } catch (e) {}
  return out;
}`

const ckSyncJS = `async ([zone, token, keys]) => {
  const c = CloudKit.getDefaultContainer();
  const db = zone.scope === 'shared' ? c.sharedCloudDatabase : c.privateCloudDatabase;
  const zoneID = zone.owner ? {zoneName: zone.zoneName, ownerRecordName: zone.owner} : {zoneName: zone.zoneName};
  const t0 = Date.now(); let pages = 0; const changed = [], gone = [];
  while (pages < 120) {
    const entry = {zoneID, resultsLimit: 200, desiredKeys: keys};
    if (token) entry.syncToken = token;
    const r = await db.fetchRecordZoneChanges([entry]); pages++;
    const z = r.zones && r.zones[0];
    if (!z) return {error: (r.errors || []).map(e => String(e.reason || e.serverErrorCode || e)).join('; ') || 'no zone in response', pages};
    for (const rec of (z.records || [])) {
      if (rec.deleted) { gone.push(rec.recordName); continue; }
      const f = rec.fields || {}; const v = (k) => (f[k] ? f[k].value : undefined);
      changed.push({n: rec.recordName, t: rec.recordType, del: v('Deleted') === 1, c: v('Completed') === 1,
                    cd: v('CompletionDate'), dd: v('DueDate'), cr: v('CreationDate'),
                    list: f.List && f.List.value && f.List.value.recordName, title: v('TitleDocument'),
                    allday: v('AllDay') === 1, name: v('Name'), pri: v('Priority'), flag: v('Flagged') === 1});
    }
    token = z.syncToken;
    if (!z.moreComing) break;
  }
  return {token, changed, gone, pages, ms: Date.now() - t0};
}`

const walkPrelude = `const walk = (root, d, fn) => { if (d > 20) return; ` +
	`for (const el of root.querySelectorAll('*')) { ` +
	`if (el.shadowRoot) walk(el.shadowRoot, d+1, fn); fn(el); } };`

const rowGeoJS = `(title) => {
  ` + "%s" + `
  let row = null;
  walk(document, 0, (el) => {
    if (!row && (el.className||'').toString().split(/\s+/).includes('reminder-item')
        && (el.innerText||'').toLowerCase().includes(title.toLowerCase())) row = el;
  });
  if (!row) return JSON.stringify({error: 'row not found'});
  row.scrollIntoView({block: 'center'});
  const rr = row.getBoundingClientRect();
  let info = null;
  walk(row, 0, (el) => {
    if (!info && (el.className||'').toString().split(/\s+/).includes('info')) info = el;
  });
  if (!info) return JSON.stringify({error: 'info control not found'});
  const ir = info.getBoundingClientRect();
  const off = (r) => r.y < 0 || r.y > window.innerHeight || r.x < 0 || r.x > window.innerWidth;
  if (off(rr) || off(ir)) return JSON.stringify({
    error: 'the row is outside the viewport at y=' + Math.round(rr.y) +
           ' (height ' + window.innerHeight + '); a click there would be discarded'});
  return JSON.stringify({row: {x: Math.round(rr.x+20), y: Math.round(rr.y+rr.height/2)},
                         info: {x: Math.round(ir.x+ir.width/2), y: Math.round(ir.y+ir.height/2)}});
}`

const toggleDayJS = `() => {
  ` + "%s" + `
  let c = null;
  walk(document, 0, (el) => {
    if (!c && (el.className||'').toString().includes('date-switch-container')) c = el;
  });
  if (!c) return 'no date switch: popover not open';
  let sw = null;
  walk(c, 0, (el) => { if (!sw && el.tagName.toLowerCase() === 'ui-switch') sw = el; });
  if (!sw) return 'no ui-switch';
  if (sw.getAttribute('aria-checked') === 'true') return 'already on';
  sw.click();
  return 'on';
}`

const calMonthJS = `() => {
  ` + "%s" + `
  const tally = {};
  walk(document, 0, (el) => {
    const m = (el.getAttribute('aria-label') || '').match(/([A-Z][a-z]+) \d{1,2}, (\d{4})/);
    if (m) { const k = m[1] + ' ' + m[2]; tally[k] = (tally[k] || 0) + 1; }
  });
  let best = null, n = 0;
  for (const k in tally) if (tally[k] > n) { n = tally[k]; best = k; }
  return best ? JSON.stringify({month: best.split(' ')[0], year: +best.split(' ')[1]})
              : JSON.stringify({error: 'no day cells on screen'});
}`

const prevMonthJS = `() => {
  ` + "%s" + `
  let btn = null;
  walk(document, 0, (el) => {
    if (!btn && (el.getAttribute('aria-label')||'') === 'Previous Month') btn = el;
  });
  if (!btn) return 'no previous-month control';
  btn.click();
  return 'moved';
}`

const nextMonthJS = `() => {
  ` + "%s" + `
  let btn = null;
  walk(document, 0, (el) => {
    if (!btn && (el.getAttribute('aria-label')||'') === 'Next Month') btn = el;
  });
  if (!btn) return 'no next-month control';
  btn.click();
  return 'advanced';
}`

const clickDayJS = `(label) => {
  ` + "%s" + `
  const year = label.slice(-4);
  let hit = null;
  const seen = [];
  walk(document, 0, (el) => {
    const a = el.getAttribute('aria-label') || '';
    if (!a.includes(', ' + year)) return;
    seen.push(a);
    if (!hit && a.includes(label)) hit = el;
  });
  if (!hit) return 'day not visible: ' + label + '; the calendar shows ' + seen.slice(0, 3).join(' | ');
  hit.click();
  return 'selected';
}`

const segmentsJS = `() => {
  ` + "%s" + `
  const seg = {};
  walk(document, 0, (el) => {
    const a = el.getAttribute('aria-label') || '';
    if (el.getAttribute('role') === 'spinbutton' && ['month','day','year'].includes(a)) {
      seg[a] = (el.innerText || '').trim();
    }
  });
  return JSON.stringify(seg);
}`

const saveJS = `() => {
  ` + "%s" + `
  let btn = null;
  walk(document, 0, (el) => {
    if (!btn && el.tagName.toLowerCase() === 'ui-button'
        && (el.innerText||'').trim().toLowerCase() === 'save') btn = el;
  });
  if (!btn) return JSON.stringify({error: 'save not found'});
  const r = btn.getBoundingClientRect();
  return JSON.stringify({x: Math.round(r.x + r.width/2), y: Math.round(r.y + r.height/2)});
}`

const popoverOpenJS = `() => {
  ` + "%s" + `
  let open = false;
  walk(document, 0, (el) => {
    if (open || el.tagName.toLowerCase() !== 'ui-popover') return;
    const r = el.getBoundingClientRect();
    if (r.width > 0 && r.height > 0) open = true;
  });
  return open;
}`

const scrollEndJS = `() => {
  ` + "%s" + `
  let area = null;
  walk(document, 0, (el) => {
    const cls = (el.className || '').toString();
    if (!area && (cls.includes('reminder-list-items') || cls.includes('scrollable-area'))
        && el.scrollHeight > el.clientHeight + 4) area = el;
  });
  if (!area) return 'nothing to scroll';
  area.scrollTop = area.scrollHeight;
  return 'scrolled';
}`

const focusedTextJS = `() => {
  const a = document.activeElement;
  const deep = (el) => {
    while (el && el.shadowRoot && el.shadowRoot.activeElement) el = el.shadowRoot.activeElement;
    return el;
  };
  const el = deep(a);
  if (!el) return JSON.stringify({focused: false});
  const cls = (el.className || '').toString();
  return JSON.stringify({
    focused: /tt-input-field|content-title/.test(cls) || el.isContentEditable
             || el.tagName.toLowerCase() === 'input',
    cls: cls.slice(0, 40),
    text: (el.value !== undefined && el.value !== null ? el.value : (el.innerText || '')).trim()
  });
}`

const focusNewRowJS = `() => {
  ` + "%s" + `
  const rows = [];
  walk(document, 0, (el) => {
    if ((el.className||'').toString().split(/\s+/).includes('reminder-item')) rows.push(el);
  });
  let target = null;
  for (const r of rows) {
    let ct = '';
    walk(r, 0, (x) => {
      if (!ct && (x.className||'').toString().split(/\s+/).includes('content-title'))
        ct = (x.innerText||'').trim();
    });
    if (!ct) target = r;
  }
  if (!target) return JSON.stringify({error: 'no empty row to type into'});
  let field = null;
  walk(target, 0, (el) => {
    if (!field && (el.className||'').toString().includes('tt-input-field')) field = el;
  });
  if (!field) return JSON.stringify({error: 'no tt-input-field on the new row'});
  field.scrollIntoView({block: 'center'});
  const r = field.getBoundingClientRect();
  return JSON.stringify({x: Math.round(r.x + Math.min(40, r.width/2)),
                         y: Math.round(r.y + r.height/2)});
}`

const timeCheckboxJS = `() => {
  ` + "%s" + `
  let cb = null;
  walk(document, 0, (el) => {
    if (!cb && (el.className||'').toString().includes('time-checkbox')) cb = el;
  });
  if (!cb) return JSON.stringify({error: 'no time checkbox: is the date enabled?'});
  const checked = cb.getAttribute('aria-checked') === 'true';
  const r = cb.getBoundingClientRect();
  return JSON.stringify({checked, x: Math.round(r.x + r.width/2), y: Math.round(r.y + r.height/2)});
}`

const segmentJS = `(which) => {
  ` + "%s" + `
  let seg = null;
  walk(document, 0, (el) => {
    if (!seg && el.getAttribute('role') === 'spinbutton'
        && (el.getAttribute('aria-label')||'') === which) seg = el;
  });
  if (!seg) return JSON.stringify({error: 'no ' + which + ' segment'});
  const r = seg.getBoundingClientRect();
  return JSON.stringify({text: (seg.innerText||'').trim(),
                         x: Math.round(r.x + r.width/2), y: Math.round(r.y + r.height/2)});
}`

const timeSegmentsJS = `() => {
  ` + "%s" + `
  const out = {};
  walk(document, 0, (el) => {
    const a = el.getAttribute('aria-label') || '';
    if (el.getAttribute('role') === 'spinbutton' && ['hour','minute','AM/PM'].includes(a)) {
      out[a] = (el.innerText || '').trim();
    }
  });
  return JSON.stringify(out);
}`

const completeGeoJS = `(title) => {
  ` + "%s" + `
  const want = title.toLowerCase();
  const hits = [];
  walk(document, 0, (el) => {
    if (!(el.className||'').toString().split(/\s+/).includes('reminder-item')) return;
    if ((el.className||'').toString().includes('completed')) return;
    let t = '';
    walk(el, 0, (k) => {
      if (!t && (k.className||'').toString().split(/\s+/).includes('content-title')) {
        t = (k.innerText||'').trim();
      }
    });
    if (t && t.toLowerCase().includes(want)) hits.push({el, t});
  });
  if (hits.length === 0) return JSON.stringify({error: 'no open reminder matching that title'});
  if (hits.length > 1) {
    return JSON.stringify({error: 'more than one open reminder matches',
                           candidates: hits.map((h) => h.t).slice(0, 8)});
  }
  const row = hits[0].el;
  row.scrollIntoView({block: 'center'});
  let btn = null;
  walk(row, 0, (el) => {
    if (!btn && (el.className||'').toString().split(/\s+/).includes('mark-completed')) btn = el;
  });
  if (!btn) return JSON.stringify({error: 'the completion control was not found on that row'});
  const r = btn.getBoundingClientRect();
  if (r.y < 0 || r.y > window.innerHeight || r.x < 0 || r.x > window.innerWidth) {
    return JSON.stringify({error: 'the row sits outside the viewport at y=' + Math.round(r.y)});
  }
  return JSON.stringify({title: hits[0].t,
                         x: Math.round(r.x + r.width/2), y: Math.round(r.y + r.height/2)});
}`

// Interpolated snippets: each %s above takes the shared walker.
var (
	rowGeo       = fmt.Sprintf(rowGeoJS, walkPrelude)
	toggleDay    = fmt.Sprintf(toggleDayJS, walkPrelude)
	calMonth     = fmt.Sprintf(calMonthJS, walkPrelude)
	prevMonth    = fmt.Sprintf(prevMonthJS, walkPrelude)
	nextMonth    = fmt.Sprintf(nextMonthJS, walkPrelude)
	clickDay     = fmt.Sprintf(clickDayJS, walkPrelude)
	segments     = fmt.Sprintf(segmentsJS, walkPrelude)
	saveBtn      = fmt.Sprintf(saveJS, walkPrelude)
	popoverOpen  = fmt.Sprintf(popoverOpenJS, walkPrelude)
	scrollEnd    = fmt.Sprintf(scrollEndJS, walkPrelude)
	focusNewRow  = fmt.Sprintf(focusNewRowJS, walkPrelude)
	timeCheckbox = fmt.Sprintf(timeCheckboxJS, walkPrelude)
	segment      = fmt.Sprintf(segmentJS, walkPrelude)
	timeSegments = fmt.Sprintf(timeSegmentsJS, walkPrelude)
	completeGeo  = fmt.Sprintf(completeGeoJS, walkPrelude)
)

package notes

// Selector snippets for the Notes web app, probed on the live app. Class
// tokens match exactly: substring matching catches wrappers that repeat
// every entry. The DOM is the contract here, not the language.

const selectedFolderJS = `() => {
  let selected = '';
  const walk = (root, depth) => {
    if (depth > 16 || selected) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, depth + 1);
      if (selected) return;
      if ((el.className || '').toString().split(/\s+/).includes('folder-list-item-container')
          && el.getAttribute('aria-selected') === 'true') {
        selected = (el.getAttribute('aria-label') || el.innerText || '').trim();
      }
    }
  };
  walk(document, 0);
  return selected;
}`

const rowsJS = `() => {
  const rows = [];
  const walk = (root, depth, sink) => {
    if (depth > 14) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, depth + 1, sink);
      sink(el);
    }
  };
  const containers = [];
  walk(document, 0, (el) => {
    if ((el.className || '').toString().split(/\s+/).includes('note-list-item-container')) {
      containers.push(el);
    }
  });
  for (let slot = 0; slot < containers.length; slot++) {
    const c = containers[slot];
    const pick = (cls) => {
      let text = '';
      walk(c, 0, (el) => {
        if (!text && (el.className || '').toString().split(/\s+/).includes(cls)) {
          text = (el.innerText || '').trim();
        }
      });
      return text;
    };
    rows.push({
      slot: slot,
      title: pick('note-list-item-title'),
      date: pick('note-list-item-date'),
      snippet: pick('note-list-item-snippet'),
      folder: pick('note-list-item-folder-title'),
    });
  }
  return rows;
}`

const focusSearchJS = `() => {
  let target = null;
  const walk = (root, depth) => {
    if (depth > 14) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, depth + 1);
      const hint = ((el.getAttribute('placeholder') || '') + ' ' +
                    (el.getAttribute('aria-label') || '')).toLowerCase();
      if (!target && el.tagName.toLowerCase() === 'input' && /search/.test(hint)) target = el;
    }
  };
  walk(document, 0);
  if (!target) return false;
  target.scrollIntoView({block: 'center'});
  target.focus();
  target.click();
  return true;
}`

const clearSearchJS = `() => {
  let target = null;
  const walk = (root, depth) => {
    if (depth > 14) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, depth + 1);
      const hint = ((el.getAttribute('placeholder') || '') + ' ' +
                    (el.getAttribute('aria-label') || '')).toLowerCase();
      if (!target && el.tagName.toLowerCase() === 'input' && /search/.test(hint)) target = el;
    }
  };
  walk(document, 0);
  return target ? (target.value || '') : null;
}`

const btnByTitleJS = `(want) => {
  for (const el of document.querySelectorAll('button,[role=button]')) {
    if ((el.getAttribute('title') || '') === want) {
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.height > 0) {
        return JSON.stringify({x: Math.round(r.x + r.width/2), y: Math.round(r.y + r.height/2)});
      }
    }
  }
  return 'not found';
}`

const editorStateJS = `() => {
  const im = document.querySelector('.ct-input-manager');
  const canvas = document.querySelector('.notes-pad-view canvas');
  let ink = 0;
  if (canvas) {
    try {
      const ctx = canvas.getContext('2d');
      const w = Math.min(canvas.width, 1200), h = Math.min(canvas.height, 700);
      const data = ctx.getImageData(0, 0, w, h).data;
      for (let i = 0; i < data.length; i += 40) {
        if (data[i+3] > 10 && (data[i] < 230 || data[i+1] < 230 || data[i+2] < 230)) ink++;
      }
    } catch (e) { ink = -1; }
  }
  return JSON.stringify({hasInput: !!im, hasCanvas: !!canvas, ink});
}`

const focusEditorJS = `() => {
  const input = document.querySelector('.ct-input-manager > div[tabindex="0"]')
             || document.querySelector('.ct-input-manager');
  if (!input) return false;
  input.focus();
  return document.activeElement === input
      || (document.activeElement && document.activeElement.closest('.ct-input-manager') !== null);
}`

const setClipboardJS = `async (html) => {
  try {
    await navigator.clipboard.write([new ClipboardItem({
      'text/html':  new Blob([html], {type: 'text/html'}),
      'text/plain': new Blob([html.replace(/<[^>]+>/g, '')], {type: 'text/plain'}),
    })]);
    return 'ok';
  } catch (e) { return 'ERR ' + e.name + ': ' + e.message; }
}`

const readClipboardJS = `() => navigator.clipboard.readText()`

// noteRowPointJS returns the center of the on-screen list row for a note,
// matched on its exact first line (and date line when given). The list is
// virtualised: each note has a rendered copy plus a parked one at y=-9861,
// and DOM order is not screen order, so an index into the containers
// (the old slot) could click a parked copy or another note entirely
// (inspected live, 2026-09-25).
const noteRowPointJS = `([title, date]) => {
  const found = [];
  const walk = (root, depth) => {
    if (depth > 14) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, depth + 1);
      if ((el.className || '').toString().split(/\s+/).includes('note-list-item-container')) found.push(el);
    }
  };
  walk(document, 0);
  for (const el of found) {
    const b = el.getBoundingClientRect();
    if (b.height <= 0 || b.y < 0 || b.y + b.height > window.innerHeight) continue;
    const lines = (el.innerText || '').split('\n').map((l) => l.trim()).filter(Boolean);
    if (lines[0] !== title || (date && !lines.includes(date))) continue;
    return {x: Math.round(b.x + b.width / 2), y: Math.round(b.y + b.height / 2)};
  }
  return null;
}`

const clipboardStateJS = `async () => {
  let write = '?';
  try { write = (await navigator.permissions.query({name: 'clipboard-write'})).state; } catch (e) { write = e.name; }
  return 'clipboard-write=' + write + ' focus=' + document.hasFocus();
}`

const padCenterJS = `() => {
  const pads = document.querySelectorAll('.notes-pad-view');
  if (!pads.length) return 'not found';
  const r = pads[0].getBoundingClientRect();
  return JSON.stringify({x: Math.round(r.x + r.width/2), y: Math.round(r.y + r.height/2)});
}`

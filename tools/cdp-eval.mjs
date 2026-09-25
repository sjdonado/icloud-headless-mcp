// cdp-eval.mjs: a console for the resident browser's iCloud web apps.
// Experiment here before writing Go: run a selector or snippet in the app
// frame, read the result, click, type, and repeat. See AGENTS.md,
// "Debugging the web apps".
//
//   node tools/cdp-eval.mjs HINT 'EXPR'          evaluate EXPR in the app frame (main world)
//   node tools/cdp-eval.mjs HINT --click X Y     trusted click (hover, press buttons=1, release)
//   node tools/cdp-eval.mjs HINT --type TEXT     Input.insertText into the focused element
//   node tools/cdp-eval.mjs HINT --key KEY       Tab, Enter or Escape
//
// HINT picks the tab by URL (reminders, notes). EXPR may use $$all(sel,
// root) (querySelectorAll through shadow roots), box(el) and desc(el).
// CDP defaults to http://127.0.0.1:9222; ICLOUD_CDP overrides it. Needs
// Node 22 or newer (built-in fetch and WebSocket).
// ev.mjs HINT --type TEXT | --key KEY : insertText / key press
const [hint, ...rest] = process.argv.slice(2);
const all = await (await fetch((process.env.ICLOUD_CDP || 'http://127.0.0.1:9222') + '/json/list')).json();
const t = all.find(x => x.type === 'page' && x.url.includes(hint));
if (!t) { console.error('no tab matching ' + hint + '; open the app once first (any tool call does)'); process.exit(1); }
const ws = new WebSocket(t.webSocketDebuggerUrl); let id = 0; const pend = {}; const ctxs = [];
ws.onmessage = e => { const m = JSON.parse(e.data);
  if (m.method === 'Runtime.executionContextCreated') ctxs.push(m.params.context);
  if (m.id && pend[m.id]) { pend[m.id](m); delete pend[m.id]; } };
await new Promise(r => ws.onopen = r);
const call = (method, params = {}) => new Promise(r => { pend[++id] = r; ws.send(JSON.stringify({ id, method, params })); });
await call('Page.bringToFront');
const sleep = ms => new Promise(r => setTimeout(r, ms));
if (rest[0] === '--click') {
  const [x, y] = [+rest[1], +rest[2]];
  await call('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y }); await sleep(150);
  await call('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', buttons: 1, clickCount: 1 }); await sleep(60);
  await call('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', buttons: 0, clickCount: 1 });
  console.log('clicked', x, y); process.exit(0);
}
if (rest[0] === '--type') { await call('Input.insertText', { text: rest.slice(1).join(' ') }); console.log('typed'); process.exit(0); }
if (rest[0] === '--key') { const k = rest[1]; const codes = { Tab: 9, Enter: 13, Escape: 27 };
  for (const type of ['keyDown', 'keyUp']) await call('Input.dispatchKeyEvent', { type, key: k, code: k, windowsVirtualKeyCode: codes[k] || 0, ...(k === 'Enter' && type === 'keyDown' ? { text: '\r' } : {}) });
  console.log('key', k); process.exit(0); }
await call('Runtime.enable'); await sleep(300);
const tree = (await call('Page.getFrameTree')).result.frameTree;
const app = (tree.childFrames || []).find(f => f.frame.url.includes('applications'))?.frame.id;
const ctx = ctxs.find(c => c.auxData?.isDefault && c.auxData.frameId === app);
const helpers = `const $$all = (sel, root = document) => { const out = []; const walk = (r) => { out.push(...r.querySelectorAll(sel)); for (const el of r.querySelectorAll('*')) if (el.shadowRoot) walk(el.shadowRoot); }; walk(root); return out; };
const box = (el) => { const b = el.getBoundingClientRect(); return {x: Math.round(b.x + b.width/2), y: Math.round(b.y + b.height/2), w: Math.round(b.width), h: Math.round(b.height)}; };
const desc = (el) => ({tag: el.tagName.toLowerCase(), cls: (el.className||'').toString().slice(0, 70), text: (el.innerText||'').trim().slice(0, 50), aria: el.getAttribute('aria-label'), role: el.getAttribute('role'), ...box(el)});`;
const r = await call('Runtime.evaluate', { contextId: ctx.id, returnByValue: true, awaitPromise: true, expression: `(async () => { ${helpers} return (${rest.join(' ')}); })()` });
console.log(JSON.stringify(r.result.exceptionDetails ? r.result.exceptionDetails.exception?.description : r.result.result.value, null, 1));
process.exit(0);

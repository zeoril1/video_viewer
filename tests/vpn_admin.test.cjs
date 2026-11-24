'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function harness(fetch) {
  const nodes = new Map();
  function element() {
    const classes = new Set();
    let value = '';
    return { children: [], dataset: {}, events: {}, innerHTML: '', textContent: '', files: [],
      get value() { return value; },
      set value(next) { value = next; if (next === '') this.files = []; },
      addEventListener(name, fn) { this.events[name] = fn; },
      appendChild(child) { this.children.push(child); },
      querySelector() { return element(); },
      closest() { if (!this.wrapper) this.wrapper = element(); return this.wrapper; },
      classList: { add(name) { classes.add(name); }, remove(name) { classes.delete(name); },
        toggle(name, on) { if (on) classes.add(name); else classes.delete(name); },
        contains(name) { return classes.has(name); } },
    };
  }
  const node = id => { if (!nodes.has(id)) nodes.set(id, element()); return nodes.get(id); };
  class FormData {
    constructor() { this.entries = new Map(); }
    append(key, value) { this.entries.set(key, value); }
    get(key) { return this.entries.get(key); }
  }
  const context = vm.createContext({ fetch, FormData, console, setTimeout: () => 1, clearTimeout() {},
    document: { getElementById: node, querySelector: node, createElement: element, createDocumentFragment: element } });
  const source = fs.readFileSync(path.join(__dirname, '../web/admin.js'), 'utf8');
  vm.runInContext(source.replace(/init\(\);\s*$/, ''), context);
  const select = file => { node('vpn-config').files = file ? [file] : []; node('vpn-config').events.change(); };
  const submit = () => node('vpn-form').events.submit({ preventDefault() {} });
  return { context, node, select, submit };
}
const ready = { configured: true, name: 'new.conf', updated_at: '2026-11-24T09:00:00Z', status: 'ready' };

test('VPN tab hides film-only controls and reloads VPN instead of the catalogue', async () => {
  const calls = [];
  const f = harness(async url => { calls.push(url); return { ok: true, json: async () => ready }; });
  f.context.showTab('vpn');
  assert.equal(f.node('vpn-panel').hidden, false);
  assert.equal(f.node('admin-films-layout').hidden, true);
  assert.equal(f.node('users-panel').hidden, true);
  assert.equal(f.node('admin-film-note').hidden, true);
  assert.equal(f.node('refresh-all').hidden, true);
  assert.equal(f.node('tab-vpn').classList.contains('active'), true);
  f.node('reload-list').events.click();
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(calls, ['/api/admin/vpn']);
  f.context.showTab('missing');
  assert.equal(f.node('vpn-panel').hidden, true);
  assert.equal(f.node('admin-films-layout').hidden, false);
  assert.equal(f.node('refresh-all').hidden, false);
});

test('non-admin authentication hides the VPN tab without loading VPN settings', async () => {
  const calls = [];
  const f = harness(async url => { calls.push(url); return { ok: true, json: async () => ({ user: { role: 'user' } }) }; });
  await f.context.init();
  assert.equal(f.node('tab-vpn').hidden, true);
  assert.equal(f.node('vpn-panel').hidden, true);
  assert.equal(f.node('admin-deny').hidden, false);
  assert.deepEqual(calls, ['/api/auth/me']);
});

test('VPN status renders the filename as text and offers replacement', async () => {
  const f = harness(async () => ({ ok: true, json: async () => ({ ...ready, name: '<img src=x onerror=alert(1)>.conf' }) }));
  await f.context.loadVPN();
  assert.equal(f.node('vpn-name').textContent, '<img src=x onerror=alert(1)>.conf');
  assert.equal(f.node('vpn-name').innerHTML, '');
  assert.equal(f.node('vpn-state').textContent, 'Конфиг применён');
  assert.equal(f.node('vpn-submit').textContent, 'Заменить конфиг');
  assert.equal(f.node('vpn-submit').disabled, true);
});

test('invalid or empty WireGuard selections do not send an upload', async () => {
  const f = harness(async () => { throw new Error('must not fetch'); });
  for (const file of [null, { name: 'wrong.ovpn', size: 20 }, { name: 'empty.conf', size: 0 }, { name: 'large.conf', size: 65537 }]) {
    f.select(file);
    await f.submit();
    assert.match(f.node('vpn-status').className, /error/);
  }
});

test('upload sends the selected file as multipart and renders the applied result', async () => {
  let sent;
  const f = harness(async (url, options) => { sent = { url, options }; return { ok: true, status: 200, json: async () => ready }; });
  const file = { name: 'new.CONF', size: 65536 };
  f.select(file);
  await f.submit();
  assert.equal(sent.url, '/api/admin/vpn');
  assert.equal(sent.options.method, 'PUT');
  assert.equal(sent.options.body.get('config'), file);
  assert.equal(sent.options.headers, undefined, 'browser must supply the multipart boundary');
  assert.equal(f.node('vpn-name').textContent, 'new.conf');
  assert.equal(f.node('vpn-config').files.length, 0);
  assert.equal(f.node('vpn-submit').disabled, true);
  assert.match(f.node('vpn-status').className, /success/);
});

test('failed replacement retains the current filename and makes retry available', async () => {
  const f = harness(async () => ({ ok: false, status: 400, text: async () => JSON.stringify({ message: 'Некорректный конфиг WireGuard.' }) }));
  f.context.renderVPN({ ...ready, name: 'old.conf' });
  f.select({ name: 'new.conf', size: 400 });
  await f.submit();
  assert.equal(f.node('vpn-name').textContent, 'old.conf');
  assert.match(f.node('vpn-status').textContent, /Некорректный конфиг WireGuard/);
  assert.equal(f.node('vpn-submit').disabled, false);
  assert.equal(f.node('vpn-config').disabled, false);
});

test('a stale status response cannot overwrite a newly replaced config', async () => {
  let finishStatus;
  const f = harness(async (url, options) => options.method === 'PUT'
    ? { ok: true, status: 200, json: async () => ready }
    : new Promise(resolve => { finishStatus = resolve; }));
  const loading = f.context.loadVPN();
  f.select({ name: 'new.conf', size: 400 });
  await f.submit();
  finishStatus({ ok: true, json: async () => ({ configured: false, status: 'unconfigured' }) });
  await loading;
  assert.equal(f.node('vpn-name').textContent, 'new.conf');
  assert.equal(f.node('vpn-state').textContent, 'Конфиг применён');
});

test('an active replacement blocks duplicate uploads and status reloads', async () => {
  const calls = [];
  let finishUpload;
  const f = harness(async (url, options) => { calls.push({ url, options }); return new Promise(resolve => { finishUpload = resolve; }); });
  f.select({ name: 'new.conf', size: 400 });
  const saving = f.submit();
  assert.equal(f.node('vpn-submit').disabled, true);
  assert.equal(f.node('vpn-config').disabled, true);
  await f.submit();
  await f.context.loadVPN();
  assert.equal(calls.length, 1);
  finishUpload({ ok: true, status: 200, json: async () => ready });
  await saving;
  assert.equal(f.node('vpn-config').disabled, false);
});

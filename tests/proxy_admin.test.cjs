'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const snapshot = {
  config: { enabled: true, sources: ['https://example.org/proxies.txt'], static: ['198.51.100.1:8080'] },
  interval_seconds: 300, running: false, active_route: 'vpn', vpn_configured: true,
  last_started_at: '2026-11-28T09:00:00Z', last_finished_at: '2026-11-28T09:00:20Z', next_run_at: '2026-11-28T09:05:00Z',
  candidates: 30, available: 2, last_error: '',
  items: [
    { address: 'http://198.51.100.10:8080', latency_ms: 125.6, checked_at: '2026-11-28T09:00:12Z', source: 'https://example.org/proxies.txt' },
    { address: 'https://198.51.100.20:8443', latency_ms: 0, checked_at: '2026-11-28T09:00:13Z', source: 'static' },
  ],
};
const response = (data, status = 200) => ({
  ok: status >= 200 && status < 300, status,
  json: async () => JSON.parse(JSON.stringify(data)),
  text: async () => typeof data === 'string' ? data : JSON.stringify(data),
});
const flush = () => new Promise(resolve => setImmediate(resolve));

function harness(fetch = async () => response(snapshot)) {
  const nodes = new Map(), timers = new Map(), documentEvents = {};
  let nextTimer = 0;
  function element(tagName = 'div') {
    const classes = new Set();
    return { tagName, children: [], dataset: {}, events: {}, innerHTML: '', textContent: '', value: '', checked: false,
      addEventListener(name, listener) { this.events[name] = listener; },
      appendChild(child) { this.children.push(child); return child; },
      replaceChildren(...children) { this.children = children; },
      querySelector() { return element(); },
      closest() { return this.wrapper || (this.wrapper = element()); },
      classList: { add(name) { classes.add(name); }, remove(name) { classes.delete(name); },
        toggle(name, active) { if (active) classes.add(name); else classes.delete(name); }, contains(name) { return classes.has(name); } },
    };
  }
  const node = id => { if (!nodes.has(id)) nodes.set(id, element()); return nodes.get(id); };
  const document = { hidden: false, getElementById: node, querySelector: node, createElement: element, createDocumentFragment: element,
    addEventListener(name, listener) { documentEvents[name] = listener; } };
  const calls = [];
  const context = vm.createContext({ document, URL, AbortController, console,
    fetch: (url, options = {}) => { calls.push({ url, options }); return fetch(url, options); },
    setTimeout(fn, delay) { const id = ++nextTimer; timers.set(id, { fn, delay }); return id; },
    clearTimeout(id) { timers.delete(id); },
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../web/admin.js'), 'utf8').replace(/init\(\);\s*$/, ''), context);
  const edit = (id, value) => { const el = node(id); if (id === 'proxies-enabled') el.checked = value; else el.value = value; el.events[id === 'proxies-enabled' ? 'change' : 'input'](); };
  const submit = () => node('proxies-form').events.submit({ preventDefault() {} });
  const fireTimer = async delay => {
    const match = [...timers].find(([, timer]) => timer.delay === delay);
    assert.ok(match, 'expected timer with delay ' + delay);
    timers.delete(match[0]); match[1].fn(); await flush();
  };
  return { context, node, calls, timers, edit, submit, fireTimer, document, documentEvents };
}

test('proxy status renders available addresses, latency units and observed routes safely', () => {
  const f = harness();
  f.context.renderProxies(snapshot);
  assert.equal(f.node('proxies-count').textContent, '2 / 30');
  assert.equal(f.node('proxies-route').textContent, 'VPN (основной)');
  assert.match(f.node('proxies-interval').textContent, /5 мин\./);
  assert.equal(f.node('proxies-body').children[0].children[1].textContent, '126 мс');
  assert.equal(f.node('proxies-body').children[1].children[1].textContent, '0 мс');
  assert.equal(f.node('proxies-body').children[1].children[3].textContent, 'Вручную');
  assert.match(f.node('proxies-finished').textContent, /2026/);
  f.context.renderProxies({ ...snapshot, active_route: 'proxy', last_error: '<img src=x onerror=alert(1)>',
    items: [{ address: '<script>proxy</script>', latency_ms: null, source: '<svg onload=alert(1)>' }] });
  assert.equal(f.node('proxies-route').textContent, 'Резервные прокси');
  const row = f.node('proxies-body').children[0];
  assert.equal(row.children[0].textContent, '<script>proxy</script>');
  assert.equal(row.children[0].innerHTML, '');
  assert.equal(row.children[1].textContent, '—');
  assert.equal(row.children[3].innerHTML, '');
  assert.equal(f.node('proxies-check-error').innerHTML, '');
  f.context.renderProxies({ ...snapshot, active_route: 'unavailable' });
  assert.equal(f.node('proxies-route').textContent, 'Нет доступного маршрута');
});

test('proxy empty states explain running checks, disabled fallback and failed candidates', () => {
  const f = harness();
  f.context.renderProxies({ ...snapshot, items: [], running: true });
  assert.match(f.node('proxies-empty').textContent, /Идёт поиск/);
  assert.equal(f.node('proxies-refresh').disabled, true);
  f.context.renderProxies({ ...snapshot, items: [], config: { enabled: false } });
  assert.match(f.node('proxies-empty').textContent, /выключены/);
  f.context.renderProxies({ ...snapshot, items: [], available: 0 });
  assert.match(f.node('proxies-empty').textContent, /Доступных прокси не найдено/);
  f.context.renderProxies({ ...snapshot, items: [], available: 0, candidates: 0 });
  assert.match(f.node('proxies-empty').textContent, /Добавьте источники/);
});

test('non-admin initialization hides proxy controls without requesting their API', async () => {
  const f = harness(async () => response({ user: { role: 'user' } }));
  await f.context.init();
  assert.equal(f.node('tab-proxies').hidden, true);
  assert.equal(f.node('proxies-panel').hidden, true);
  assert.deepEqual(f.calls.map(call => call.url), ['/api/auth/me']);
});

test('proxy tab keeps the old tabs available and polls only while visible', async () => {
  const f = harness();
  f.context.showTab('proxies'); await flush();
  assert.equal(f.node('proxies-panel').hidden, false);
  assert.equal(f.node('admin-films-layout').hidden, true);
  assert.equal(f.node('vpn-panel').hidden, true);
  assert.equal(f.node('refresh-all').hidden, true);
  assert.equal(f.node('reload-list').textContent, 'Обновить статус');
  assert.equal(f.calls.length, 1);
  await f.fireTimer(10000);
  assert.equal(f.calls.length, 2);
  f.context.showTab('vpn');
  assert.equal(f.node('vpn-panel').hidden, false);
  assert.equal(f.node('proxies-panel').hidden, true);
  assert.equal(f.timers.size, 0);
  f.context.showTab('missing');
  assert.equal(f.node('admin-films-layout').hidden, false);
  assert.equal(f.node('refresh-all').hidden, false);
});

test('status requests never overlap and are cancelled when the document is hidden', async () => {
  let finish;
  const f = harness(async () => new Promise(resolve => { finish = resolve; }));
  f.context.showTab('proxies');
  f.context.loadProxies(); f.context.loadProxies(true);
  assert.equal(f.calls.length, 1);
  const oldSignal = f.calls[0].options.signal;
  f.document.hidden = true; f.documentEvents.visibilitychange();
  assert.equal(oldSignal.aborted, true);
  finish(response(snapshot)); await flush();
  assert.equal(f.timers.size, 0);
  f.document.hidden = false; f.documentEvents.visibilitychange();
  assert.equal(f.calls.length, 2);
  finish(response(snapshot)); await flush();
  assert.ok([...f.timers.values()].some(timer => timer.delay === 10000));
});

test('editing during a delayed status request preserves unsaved settings', async () => {
  let finish;
  const f = harness(async () => new Promise(resolve => { finish = resolve; }));
  f.context.renderProxies(snapshot);
  const loading = f.context.loadProxies();
  f.edit('proxies-sources', 'https://my-source.example/list.txt');
  f.edit('proxies-enabled', false);
  finish(response({ ...snapshot, available: 1 })); await loading;
  assert.equal(f.node('proxies-sources').value, 'https://my-source.example/list.txt');
  assert.equal(f.node('proxies-enabled').checked, false);
  assert.equal(f.node('proxies-count').textContent, '1 / 30');
  assert.equal(f.node('proxies-dirty').hidden, false);
});

test('configuration save sends normalized lists and can accept an empty successful response', async () => {
  const f = harness(async (url, options) => options.method === 'PUT' ? response(null, 204) : response(snapshot));
  f.context.renderProxies(snapshot);
  f.edit('proxies-enabled', false);
  f.edit('proxies-sources', ' https://source.example/proxies.txt\n\nhttps://source.example/proxies.txt ');
  f.edit('proxies-static', ' 198.51.100.42:8080\nhttps://198.51.100.43:8443 ');
  await f.submit();
  assert.equal(f.calls[0].url, '/api/admin/proxies');
  assert.equal(f.calls[0].options.method, 'PUT');
  assert.equal(f.calls[0].options.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(f.calls[0].options.body), { enabled: false,
    sources: ['https://source.example/proxies.txt'], static: ['198.51.100.42:8080', 'https://198.51.100.43:8443'] });
  assert.equal(f.node('proxies-dirty').hidden, true);
  assert.equal(f.node('proxies-save').disabled, true);
  assert.match(f.node('proxies-status').textContent, /сохранены/);
});

test('a status response started before saving cannot overwrite the saved configuration', async () => {
  let oldStatus;
  const f = harness(async (url, options) => options.method === 'PUT' ? response({ ...snapshot, config: { enabled: false, sources: [], static: [] } })
    : new Promise(resolve => { oldStatus = resolve; }));
  f.context.renderProxies(snapshot);
  const loading = f.context.loadProxies();
  f.edit('proxies-enabled', false); f.edit('proxies-sources', ''); f.edit('proxies-static', '');
  await f.submit();
  assert.equal(f.calls[0].options.signal.aborted, true);
  oldStatus(response(snapshot)); await loading;
  assert.equal(f.node('proxies-enabled').checked, false);
  assert.equal(f.node('proxies-sources').value, '');
});

test('manual refresh starts one asynchronous job, then polls status without saving dirty input', async () => {
  let accept;
  const f = harness(async (url, options) => options.method === 'POST' ? new Promise(resolve => { accept = resolve; })
    : response({ ...snapshot, running: true }));
  f.context.renderProxies(snapshot);
  f.context.showTab('proxies'); await flush();
  // Return to an idle server snapshot before starting the explicit job.
  f.context.renderProxies(snapshot);
  f.edit('proxies-sources', 'https://unsaved.example/list.txt');
  const running = f.node('proxies-refresh').events.click();
  await f.node('proxies-refresh').events.click();
  assert.equal(f.calls.filter(call => call.options.method === 'POST').length, 1);
  assert.equal(f.calls.at(-1).url, '/api/admin/proxies/refresh');
  accept(response(null, 202)); await running;
  assert.equal(f.calls.filter(call => call.options.method === 'PUT').length, 0);
  assert.equal(f.node('proxies-sources').value, 'https://unsaved.example/list.txt');
  assert.equal(f.node('proxies-refresh').disabled, true);
  assert.match(f.node('proxies-status').textContent, /Проверка запущена/);
  assert.ok([...f.timers.values()].some(timer => timer.delay === 2000));
});

test('invalid sources, proxy credentials and unsupported protocols are rejected before saving', async () => {
  const f = harness();
  f.context.renderProxies(snapshot);
  for (const [id, invalid] of [['proxies-sources', 'http://source.example/list.txt'], ['proxies-sources', 'https://user:pass@source.example/list.txt'],
    ['proxies-static', 'http://user:pass@198.51.100.1:8080'], ['proxies-static', 'socks5://198.51.100.1:1080']]) {
    f.context.renderProxies(snapshot);
    f.edit(id, invalid); await f.submit();
    assert.match(f.node('proxies-status').className, /error/);
    assert.equal(f.calls.length, 0);
    // Restore the dirty form explicitly, as the user would before another attempt.
    f.edit('proxies-sources', snapshot.config.sources.join('\n'));
    f.edit('proxies-static', snapshot.config.static.join('\n'));
  }
});

test('API authorization failures show a clear error and stop polling', async () => {
  for (const status of [401, 403]) {
    const f = harness(async () => response('denied', status));
    f.context.showTab('proxies'); await flush();
    assert.match(f.node('proxies-status').className, /error/);
    assert.match(f.node('proxies-status').textContent, status === 401 ? /Сессия истекла/ : /Нет доступа/);
    assert.equal(f.node('proxies-save').disabled, true);
    assert.equal(f.node('proxies-refresh').disabled, true);
    assert.equal(f.timers.size, 0);
    await f.context.loadProxies();
    assert.equal(f.calls.length, 1);
  }
});

test('failed configuration and manual refresh requests keep input and release controls for retry', async () => {
  const f = harness(async () => response({ message: 'Upstream temporarily unavailable' }, 503));
  f.context.renderProxies(snapshot);
  f.edit('proxies-static', '198.51.100.99:8080');
  await f.submit();
  assert.equal(f.node('proxies-static').value, '198.51.100.99:8080');
  assert.equal(f.node('proxies-save').disabled, false);
  assert.match(f.node('proxies-status').textContent, /Upstream temporarily unavailable/);
  await f.node('proxies-refresh').events.click();
  assert.equal(f.node('proxies-refresh').disabled, false);
  assert.match(f.node('proxies-status').textContent, /Не удалось запустить проверку/);
});

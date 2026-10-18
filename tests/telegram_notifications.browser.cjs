// Offline browser checks: no Telegram messages, external tabs or real user accounts.
'use strict';
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = path.resolve(__dirname, '../web');
const output = process.env.DESIGN_SCREENSHOTS || path.resolve(__dirname, '../output/design-preview');
const movie = { id: 'tt7001', imdb_id: 'tt7001', tmdb_id: '7001', kind: 'feature', title: 'Test Film', title_ru: 'Новый фильм', year: 2026, release_date: new Date().toISOString().slice(0, 10) };
const series = { ...movie, id: 'tt7002', imdb_id: 'tt7002', tmdb_id: '7002', kind: 'tvSeries', title_ru: 'Новый сериал', seasons: 1 };
let server, base, browser;
function staticResponse(url) {
  const filename = path.resolve(root, '.' + url.pathname);
  if (!filename.startsWith(root + path.sep) || !fs.existsSync(filename) || !fs.statSync(filename).isFile()) return { status: 404, body: '' };
  return { contentType: filename.endsWith('.js') ? 'application/javascript' : filename.endsWith('.css') ? 'text/css' : 'text/html; charset=utf-8', body: fs.readFileSync(filename) };
}
before(async () => {
  fs.mkdirSync(output, { recursive: true });
  server = http.createServer((req, res) => {
    const response = staticResponse(new URL(req.url, 'http://fixture'));
    res.writeHead(response.status || 200, { 'Content-Type': response.contentType || 'text/plain' });
    res.end(response.body);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  base = 'http://127.0.0.1:' + server.address().port;
  const executablePath = [process.env.CHROMIUM_PATH, chromium.executablePath(), 'C:/Program Files/Google/Chrome/Application/chrome.exe'].find(value => value && fs.existsSync(value));
  assert.ok(executablePath, 'Chromium must be installed');
  browser = await chromium.launch({ executablePath, headless: true });
});
after(async () => { await browser?.close(); await new Promise(resolve => server?.close(resolve)); });

async function fixture({ user = { id: 1, username: 'tester' }, telegram = {}, width = 1440, theme = 'light', personal = [] } = {}) {
  const context = await browser.newContext({ viewport: { width, height: 1000 }, colorScheme: theme });
  const state = { user, telegram: { configured: true, linked: false, enabled: false, bot_username: 'kinoteka_test_bot', username: '', check_interval_hours: 6, ...telegram }, link: 'https://t.me/kinoteka_test_bot?start=link_123', personal, fail: null, pending: null };
  const calls = [], errors = [], pages = [];
  context.on('page', page => pages.push(page));
  await context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) return route.abort();
    if (!url.pathname.startsWith('/api/')) return route.fulfill(staticResponse(url));
    const body = request.postData() ? JSON.parse(request.postData()) : null;
    calls.push({ path: url.pathname, method: request.method(), body, query: Object.fromEntries(url.searchParams) });
    const send = (data = {}, status = 200) => route.fulfill({ status, contentType: 'application/json', body: status === 204 ? '' : JSON.stringify(data) });
    if (url.pathname === '/api/auth/me') return state.user ? send({ user: state.user }) : send({}, 401);
    if (url.pathname === '/api/history') return send({ items: [] });
    if (url.pathname === '/api/personal') return send({ items: state.personal });
    if (url.pathname.startsWith('/api/personal/')) {
      const [, , , kind, encoded] = url.pathname.split('/'), key = decodeURIComponent(encoded);
      state.personal = state.personal.filter(item => item.kind !== kind || item.key !== key);
      if (request.method() === 'PUT') state.personal.push({ kind, key, data: body, updated_at: new Date().toISOString() });
      return send({}, 204);
    }
    if (url.pathname === '/api/notifications/telegram/link') {
      if (state.pending) await state.pending;
      return send({ url: state.link, expires_in: 600 });
    }
    if (url.pathname === '/api/notifications/telegram') {
      if (state.fail) return send({}, state.fail);
      if (request.method() === 'DELETE') Object.assign(state.telegram, { linked: false, enabled: false, username: '' });
      if (request.method() === 'PUT') state.telegram.enabled = body.enabled;
      if (request.method() !== 'GET') return send({}, 204);
      if (state.pending) await state.pending;
      return send(state.telegram);
    }
    if (url.pathname === '/api/discover/episodes') return send({ episodes: [] });
    if (url.pathname === '/api/discover/calendar') return send({ items: [], events: [], total_pages: 1 });
    if (url.pathname === '/api/films/' + movie.id) return send(movie);
    if (url.pathname === '/api/films/' + series.id) return send(series);
    if (url.pathname.endsWith('/explore')) return send({ items: [] });
    if (url.pathname.endsWith('/sources')) return send({ items: [], seasons: [], status: 'ready' });
    if (url.pathname.endsWith('/season')) return send({ episodes: [] });
    return send({ items: [] });
  });
  const page = await context.newPage();
  page.on('pageerror', e => errors.push(e.message));
  return { context, page, state, calls, errors, pages };
}
async function open(f) {
  await f.page.goto(base + '/library.html');
  await f.page.locator('#telegram-notifications [data-telegram-action="refresh"]:not([disabled])').waitFor();
}
function healthy(f) { assert.deepEqual(f.errors, []); }

test('Telegram connection is explicit and can be paused, resumed and removed', async () => {
  const f = await fixture();
  try {
    await open(f);
    assert.match(await f.page.locator('#telegram-notifications').innerText(), /каждые 6 часов/);
    await f.page.getByRole('button', { name: 'Подключить Telegram', exact: true }).click();
    const link = f.page.getByRole('link', { name: 'Открыть бота в Telegram', exact: true });
    await link.waitFor();
    assert.equal(await link.getAttribute('href'), f.state.link);
    assert.equal(await link.getAttribute('rel'), 'noopener noreferrer');
    assert.equal(f.pages.length, 1, 'Creating a link never auto-opens a popup');
    Object.assign(f.state.telegram, { linked: true, enabled: true, username: 'viewer' });
    await f.page.getByRole('button', { name: 'Я подключил — проверить', exact: true }).click();
    await f.page.getByRole('button', { name: 'Приостановить', exact: true }).click();
    await f.page.getByRole('button', { name: 'Возобновить', exact: true }).waitFor();
    assert.equal(f.state.telegram.enabled, false);
    await f.page.getByRole('button', { name: 'Возобновить', exact: true }).click();
    await f.page.getByRole('button', { name: 'Приостановить', exact: true }).waitFor();
    assert.equal(f.state.telegram.enabled, true);
    await f.page.getByRole('button', { name: 'Отключить Telegram', exact: true }).click();
    await f.page.getByRole('button', { name: 'Подключить Telegram', exact: true }).waitFor();
    assert.equal(f.state.telegram.linked, false);
    assert.deepEqual(f.calls.filter(call => call.method === 'PUT' && call.path.endsWith('/telegram')).map(call => call.body), [{ enabled: false }, { enabled: true }]);
    healthy(f);
  } finally { await f.context.close(); }
});

test('unconfigured Telegram and temporary server failure give recoverable local status', async () => {
  const f = await fixture({ telegram: { configured: false } });
  try {
    await open(f);
    assert.match(await f.page.locator('#telegram-notifications').innerText(), /администратора/);
    assert.equal(await f.page.getByRole('button', { name: 'Подключить Telegram', exact: true }).count(), 0);
    f.state.fail = 503;
    await f.page.getByRole('button', { name: 'Обновить статус', exact: true }).click();
    await f.page.locator('#telegram-notifications [role="alert"]').waitFor();
    f.state.fail = null;
    f.state.telegram.configured = true;
    await f.page.getByRole('button', { name: 'Обновить статус', exact: true }).click();
    await f.page.getByRole('button', { name: 'Подключить Telegram', exact: true }).waitFor();
    assert.equal(await f.page.locator('#telegram-notifications [role="alert"]').count(), 0);
    healthy(f);
  } finally { await f.context.close(); }
});

test('only the configured Telegram bot receives the temporary connection link', async () => {
  const f = await fixture();
  try {
    await open(f);
    for (const value of ['javascript:alert(1)', 'https://example.com/?start=secret', 'https://t.me/another_bot?start=secret', 'https://t.me/kinoteka_test_bot?start=secret&redirect=https://example.com']) {
      f.state.link = value;
      await f.page.getByRole('button', { name: 'Подключить Telegram', exact: true }).click();
      await f.page.locator('#telegram-notifications [role="alert"]').waitFor();
      assert.equal(await f.page.locator('#telegram-notifications a[href]').count(), 0);
    }
    healthy(f);
  } finally { await f.context.close(); }
});

test('guest requests no Telegram data; switching accounts discards a late connection link', async () => {
  const f = await fixture({ user: null });
  let release;
  try {
    await f.page.goto(base + '/library.html');
    await f.page.waitForFunction(() => !VV.user);
    assert.equal(await f.page.locator('#telegram-notifications').isVisible(), false);
    assert.equal(f.calls.filter(call => call.path.includes('/notifications/')).length, 0);
    f.state.user = { id: 1, username: 'one' };
    await f.page.evaluate(() => { currentUser = { id: 1, username: 'one' }; authHooks.forEach(fn => fn()); });
    await f.page.getByRole('button', { name: 'Подключить Telegram', exact: true }).waitFor();
    f.state.pending = new Promise(resolve => { release = resolve; });
    await f.page.getByRole('button', { name: 'Подключить Telegram', exact: true }).click();
    await f.page.waitForFunction(() => document.querySelector('#telegram-notifications').getAttribute('aria-busy') === 'true');
    f.state.pending = null;
    f.state.user = { id: 2, username: 'two' };
    await f.page.evaluate(() => { currentUser = { id: 2, username: 'two' }; authHooks.forEach(fn => fn()); });
    await f.page.locator('#telegram-notifications [data-telegram-action="link"]:not([disabled])').waitFor();
    release();
    await f.page.waitForTimeout(100);
    assert.equal(await f.page.locator('#telegram-notifications a[href]').count(), 0);
    healthy(f);
  } finally { release?.(); await f.context.close(); }
});

test('film and series follow actions preserve metadata; episode tools ignore followed movies', async () => {
  const f = await fixture();
  try {
    await f.page.goto(base + '/film.html?id=' + movie.id);
    await f.page.getByRole('button', { name: 'Следить за выходом', exact: true }).click();
    await f.page.getByRole('button', { name: '✓ Подписка оформлена', exact: true }).waitFor();
    assert.equal(f.state.personal.find(item => item.kind === 'follow' && item.key === movie.id).data.kind, 'feature');
    await f.page.goto(base + '/film.html?id=' + series.id);
    await f.page.getByRole('button', { name: 'Подписаться', exact: true }).click();
    await f.page.getByRole('button', { name: '✓ Подписка оформлена', exact: true }).waitFor();
    assert.equal(f.state.personal.find(item => item.kind === 'follow' && item.key === series.id).data.tmdb_id, series.tmdb_id);
    await open(f);
    const start = f.calls.length;
    await f.page.getByRole('button', { name: 'Новые серии', exact: true }).click();
    await f.page.getByText('Новых непросмотренных серий за последние 30 дней нет.', { exact: true }).waitFor();
    const episodeCalls = f.calls.slice(start).filter(call => call.path === '/api/discover/episodes');
    assert.ok(episodeCalls.length > 0);
    assert.ok(episodeCalls.every(call => call.query.tmdb_id === series.tmdb_id));
    await f.page.goto(base + '/calendar.html');
    await f.page.getByRole('checkbox', { name: 'Только подписки' }).check();
    await f.page.locator('.calendar-event.movie').waitFor();
    await f.page.locator('.calendar-event.series').waitFor();
    await f.page.locator('#feature-toolbar select').first().selectOption('movie');
    await f.page.waitForFunction(() => document.querySelector('.calendar-event.movie') && !document.querySelector('.calendar-event.series'));
    await f.page.locator('#feature-toolbar select').first().selectOption('tv');
    await f.page.waitForFunction(() => !document.querySelector('.calendar-event.movie') && document.querySelector('.calendar-event.series'));
    assert.ok(f.calls.filter(call => call.path === '/api/discover/episodes').every(call => call.query.tmdb_id === series.tmdb_id));
    healthy(f);
  } finally { await f.context.close(); }
});

for (const theme of ['light', 'dark']) test('Telegram layout fits desktop and phone in ' + theme + ' theme', async () => {
  const f = await fixture({ theme, telegram: { linked: true, enabled: true, username: 'movie_fan' } });
  try {
    await open(f);
    for (const width of [1440, 390]) {
      await f.page.setViewportSize({ width, height: width < 600 ? 844 : 1000 });
      const size = await f.page.evaluate(() => ({ width: innerWidth, scroll: document.documentElement.scrollWidth }));
      assert.ok(size.scroll <= size.width + 1, JSON.stringify(size));
      await f.page.screenshot({ path: path.join(output, `telegram-${theme}-${width}.png`), fullPage: true });
    }
    healthy(f);
  } finally { await f.context.close(); }
});

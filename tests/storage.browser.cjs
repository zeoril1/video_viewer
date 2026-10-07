const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const web = path.join(__dirname, '../web');
const hash = 'a'.repeat(40);
let browser;

before(async () => {
  const executablePath = [process.env.CHROMIUM_PATH, chromium.executablePath(),
    'C:/Program Files/Google/Chrome/Application/chrome.exe'].find(item => item && fs.existsSync(item));
  browser = await chromium.launch({ executablePath, headless: true });
});
after(async () => { await browser?.close(); });

async function fixture(options = {}) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await context.newPage(), requests = [], errors = [];
  page.setDefaultTimeout(5000);
  page.on('pageerror', error => errors.push(error.message));
  const data = {
    storage: { mode: options.mode || 'disk', used_bytes: 1024 ** 3,
      disk: { total_bytes: 100 * 1024 ** 3, used_bytes: 20 * 1024 ** 3, free_bytes: 80 * 1024 ** 3, available_bytes: 80 * 1024 ** 3 },
      torrents: [{ hash, name: 'Fixture Series', total: 4 * 1024 ** 3, downloaded: 1024 ** 3,
        stored_bytes: 1024 ** 3, metadata_ready: true, busy: !!options.busy,
        files: [{ index: 2, path: 'Series.S01E03.mkv', size: 2 * 1024 ** 3, downloaded: 1024 ** 3,
          stored_bytes: 1024 ** 3, percent: 50, download_rate: 3 * 1024 ** 2, downloading: true, available: true }] }],
    },
    hls: { used_bytes: 123 * 1024 ** 2, total_bytes: 100 * 1024 ** 3, used_bytes_disk: 20 * 1024 ** 3,
      free_bytes: 80 * 1024 ** 3, available_bytes: 80 * 1024 ** 3 },
    viewers: options.viewer ? [{ username: '<script>evil()</script>', film_id: 'tt1234567', hash, file: 2,
      season: 1, episode: 3, watched_seconds: 85, position: 400, duration: 1800, playing: true }] : [],
  };
  const users = [{ id: 1, username: 'zeoril', role: 'admin', created_at: '2026-10-07T10:00:00Z' },
    { id: 2, username: '<script>evil()</script>', role: 'user', created_at: '2026-10-07T10:00:00Z' }];
  await page.route('**/*', async route => {
    const req = route.request(), url = new URL(req.url());
    requests.push({ path: url.pathname, search: url.search, method: req.method(), body: req.postDataJSON() });
    const json = (value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) });
    if (url.pathname === '/api/auth/me') return json({ user: { id: 1, username: 'zeoril', role: options.role || 'admin' } });
    if (url.pathname === '/api/admin/storage') return json(data);
    if (url.pathname === '/api/admin/storage/' + hash) {
      if (options.conflict) return route.fulfill({ status: 409, body: 'busy' });
      data.storage.torrents = []; return json({ ok: true });
    }
    if (url.pathname === '/api/admin/users') return json({ items: users });
    if (url.pathname === '/api/admin/users/2/role') return json({ ok: true });
    if (url.pathname === '/api/admin/logs') return json({ lines: [] });
    if (url.pathname.startsWith('/api/')) return json({ items: [], total: 0 });
    const filename = path.join(web, path.basename(url.pathname));
    if (!fs.existsSync(filename)) return route.fulfill({ status: 404, body: '' });
    return route.fulfill({ body: fs.readFileSync(filename), contentType: filename.endsWith('.css') ? 'text/css'
      : filename.endsWith('.js') ? 'application/javascript' : 'text/html' });
  });
  await page.goto('http://video-viewer.test/' + (options.admin ? 'admin.html' : 'storage.html'));
  return { context, page, requests, errors, data };
}

test('storage shows disk space, live speeds, file size and actual watch duration safely', async () => {
  const f = await fixture({ viewer: true });
  try {
    await f.page.waitForSelector('.storage-release');
    assert.match(await f.page.locator('#storage-summary').innerText(), /Свободно: 80 ГиБ/);
    assert.match(await f.page.locator('#storage-files').innerText(), /1 ГиБ \/ 2 ГиБ/);
    assert.match(await f.page.locator('#storage-files').innerText(), /3 МиБ\/с/);
    assert.match(await f.page.locator('#storage-files').innerText(), /50%/);
    assert.match(await f.page.locator('#storage-viewers').innerText(), /1:25/);
    assert.match(await f.page.locator('#storage-viewers').innerText(), /6:40 \/ 30:00/);
    assert.match(await f.page.locator('#storage-viewers').innerText(), /<script>evil\(\)<\/script>/);
    assert.equal(await f.page.locator('#storage-viewers script').count(), 0);
    assert.equal(await f.page.getByRole('button', { name: 'Удалить файл', exact: true }).isDisabled(), true);
    if (process.env.STORAGE_SCREENSHOT) await f.page.screenshot({ path: process.env.STORAGE_SCREENSHOT, fullPage: true });
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('file deletion confirms exact filename and refreshes the inventory', async () => {
  const f = await fixture();
  try {
    let message = '';
    f.page.on('dialog', async dialog => { message = dialog.message(); await dialog.accept(); });
    await f.page.getByRole('button', { name: 'Удалить файл', exact: true }).click();
    await f.page.waitForFunction(() => document.getElementById('storage-status').textContent.startsWith('Удалено:'));
    assert.match(message, /Series\.S01E03\.mkv/);
    assert.ok(f.requests.some(req => req.method === 'DELETE' && req.path.endsWith(hash) && req.search === '?file=2'));
    assert.equal(await f.page.locator('.storage-release').count(), 0);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('busy deletion is explained and leaves file visible; RAM offers release deletion', async () => {
  const f = await fixture({ conflict: true });
  try {
    f.page.on('dialog', dialog => dialog.accept());
    await f.page.getByRole('button', { name: 'Удалить файл', exact: true }).click();
    await f.page.waitForFunction(() => document.getElementById('storage-status').textContent.includes('сейчас используется'));
    assert.equal(await f.page.locator('.storage-release').count(), 1);
  } finally { await f.context.close(); }
  const ram = await fixture({ mode: 'memory' });
  try {
    await ram.page.waitForSelector('.storage-release');
    assert.equal(await ram.page.getByRole('button', { name: 'Удалить файл', exact: true }).count(), 0);
    assert.equal(await ram.page.getByRole('button', { name: 'Удалить всю раздачу' }).count(), 1);
  } finally { await ram.context.close(); }
});

test('ordinary account cannot load storage inventory', async () => {
  const f = await fixture({ role: 'user' });
  try {
    await f.page.waitForSelector('#storage-deny', { state: 'visible' });
    assert.equal(f.requests.filter(req => req.path === '/api/admin/storage').length, 0);
  } finally { await f.context.close(); }
});

test('storage remains usable on a small screen without widening the page', async () => {
  const f = await fixture();
  try {
    await f.page.setViewportSize({ width: 390, height: 844 });
    await f.page.waitForSelector('.storage-release');
    assert.equal(await f.page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    assert.equal(await f.page.getByRole('button', { name: 'Удалить всю раздачу' }).isVisible(), true);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('administrator can assign moderator role and malicious usernames remain text', async () => {
  const f = await fixture({ admin: true });
  try {
    await f.page.getByRole('button', { name: 'Пользователи', exact: true }).click();
    await f.page.waitForSelector('#users-body tr');
    const row = f.page.locator('#users-body tr').nth(1);
    await row.locator('select').selectOption('moderator');
    await row.getByRole('button', { name: 'Сохранить' }).click();
    await f.page.waitForFunction(() => document.getElementById('users-status').textContent.includes('сохранены'));
    assert.ok(f.requests.some(req => req.method === 'PUT' && req.path === '/api/admin/users/2/role' && req.body.role === 'moderator'));
    assert.equal(await f.page.locator('#users-body script').count(), 0);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

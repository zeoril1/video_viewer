// Run: PLAYWRIGHT_MODULE=<bundled playwright path> node --test tests/theme.browser.cjs
// Serves real web assets and deterministic local API/media fixtures; no live server is needed.
'use strict';
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const web = path.join(__dirname, '../web');
const output = process.env.DESIGN_SCREENSHOTS || path.join(__dirname, '../output/design-preview');
const htmlPages = fs.readdirSync(web).filter(name => name.endsWith('.html'));
const now = new Date(), date = new Date(now.getFullYear(), now.getMonth(), 10, 12).toISOString().slice(0, 10);
const titles = ['Дюна: Часть вторая', 'Интерстеллар', 'Начало', 'Укрытие', 'Убийцы цветочной луны', 'Идеальные дни', 'Оппенгеймер', 'Душа'];
const palettes = [['#c76c32', '#2b2638'], ['#264567', '#8bb7c4'], ['#242842', '#885c77'], ['#28393f', '#bbd0a1'], ['#574333', '#ab7666'], ['#384a3c', '#c6b98c'], ['#973c20', '#2b2531'], ['#72529c', '#548bb5']];
const films = titles.map((title, i) => ({
  id: 'tt' + (100001 + i), imdb_id: 'tt' + (100001 + i), tmdb_id: '' + (100 + i),
  title, title_ru: title, kind: i === 3 ? 'tvSeries' : 'feature', year: 2023 + i % 2,
  release_date: date, genres: [i === 3 ? 'Drama' : 'Sci-Fi'], rating_tmdb: 7.4 + i / 10,
  movie_length: 125 + i, countries: ['США'], director: 'Режиссёр', actors: ['Актёр'],
  plot_ru: 'История о поиске своего места в мире, выборе и встречах, которые меняют жизнь.',
  poster: '/fixtures/poster-' + i + '.svg', poster_url: '/fixtures/poster-' + i + '.svg',
  seasons: i === 3 ? 2 : 0,
}));
const history = [0, 3].map(i => ({ ...films[i], film_id: films[i].id, position: 1430 + i * 180,
  duration: 7600, season: i === 3 ? 1 : 0, episode: i === 3 ? 4 : 0,
  file: 0, magnet: 'magnet:?xt=urn:btih:' + 'a'.repeat(40), updated_at: now.toISOString() }));
const personal = [0, 1, 2, 3].map(i => ({ kind: 'watchlist', key: films[i].id, data: films[i], updated_at: now.toISOString() }));
let server, browser, base;
const requests = [];

function responseFor(url) {
  requests.push(url.pathname + url.search);
  if (/^\/fixtures\/poster-[0-7]\.svg$/.test(url.pathname))
    return { contentType: 'image/svg+xml', body: poster(Number(url.pathname.match(/(\d)\.svg$/)[1])) };
  if (url.pathname.startsWith('/api/')) {
    const data = fixtureApi(url);
    return { status: data === undefined ? 404 : 200, contentType: 'application/json',
      body: JSON.stringify(data === undefined ? { error: 'Missing fixture: ' + url.pathname } : data) };
  }
  const name = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
  const filename = path.resolve(web, name);
  if (!filename.startsWith(web + path.sep) || !fs.existsSync(filename) || !fs.statSync(filename).isFile())
    return { status: 404, body: '' };
  return { contentType: filename.endsWith('.js') ? 'application/javascript'
    : filename.endsWith('.css') ? 'text/css' : 'text/html; charset=utf-8', body: fs.readFileSync(filename) };
}

function poster(i) {
  const [a, b] = palettes[i];
  return `<svg xmlns="http://www.w3.org/2000/svg" width="480" height="720" viewBox="0 0 480 720"><defs><linearGradient id="g" x2=".8" y2="1"><stop stop-color="${a}"/><stop offset="1" stop-color="${b}"/></linearGradient></defs><rect width="480" height="720" fill="url(#g)"/><circle cx="320" cy="230" r="120" fill="#fff" opacity=".14"/><path d="M0 600L180 330L480 580V720H0" fill="#101018" opacity=".45"/><path d="M0 540L360 280L480 440V720H0" fill="#141820" opacity=".28"/><text x="32" y="650" fill="#fff" font-family="Arial,sans-serif" font-size="28" font-weight="700">${titles[i]}</text><text x="32" y="685" fill="#fff" opacity=".65" font-family="Arial,sans-serif" font-size="15">КИНОТЕКА · ДЕМОНСТРАЦИЯ</text></svg>`;
}

function fixtureApi(url) {
  const name = url.pathname;
  if (name === '/api/auth/me') return { user: { id: 1, username: 'Андрей', role: 'admin' } };
  if (name === '/api/catalog') {
    const q = (url.searchParams.get('q') || '').toLowerCase();
    const items = films.filter(f => !q || f.title.toLowerCase().includes(q));
    return { items, page: 1, total_pages: 1, total: items.length };
  }
  if (name === '/api/catalog/meta') return { sections: { movie: 7, series: 1 }, genres: ['Drama', 'Sci-Fi'] };
  if (name === '/api/history') return { items: url.searchParams.has('film_id') ? [] : history };
  if (name === '/api/personal') return { items: personal };
  if (/^\/api\/films\/[^/]+$/.test(name)) return films.find(f => name.endsWith('/' + f.id)) || films[0];
  if (name.endsWith('/sources')) return { items: [], status: 'ready', seasons: [] };
  if (name.endsWith('/explore')) return { items: films.slice(1, 4), cast: [], directors: [], trailer: '' };
  if (name.includes('watch-order')) return { items: [] };
  if (name === '/api/discover/calendar') {
    const media = url.searchParams.get('media'), item = media === 'tv' ? films[3] : films[0];
    return { items: [item], total_pages: 1, events: media === 'tv' ? [{ date, item, season: 1, episode: 1, name: 'Начало' }] : [] };
  }
  if (name === '/api/discover/episodes') return { episodes: [{ air_date: date, season_number: 1, episode_number: 1, name: 'Начало' }] };
  if (name === '/api/discover/picks') return { items: films, total_pages: 1 };
  if (name === '/api/iptv/channels') return { channels: ['Первый канал', 'Культура', 'Кино', 'Спорт'].map((name, i) => ({ id: i + 1, name, group: 'Телевидение', play_url: '/api/iptv/play/' + i + '.m3u8' })), groups: [{ name: 'Телевидение', count: 4 }], playlists: [], now: now.toISOString() };
  if (name === '/api/iptv/guide') return { channels: [], now: now.toISOString(), has_more: false };
  if (name === '/api/iptv/playlists') return { playlists: [] };
  if (name === '/api/admin/storage') return { storage: { mode: 'disk', used_bytes: 2 ** 30, disk: { total_bytes: 100 * 2 ** 30, free_bytes: 80 * 2 ** 30, used_bytes: 20 * 2 ** 30 }, torrents: [] }, hls: { used_bytes: 0 }, viewers: [] };
  if (name === '/api/admin/logs') return { lines: [], running: false };
  if (name === '/api/admin/users') return { items: [{ id: 1, username: 'Андрей', role: 'admin', created_at: now.toISOString() }] };
  if (name.startsWith('/api/admin/films/')) return { items: [], total: 0 };
  if (name === '/api/auth/device/start') return { device_code: 'a'.repeat(64), user_code: 'ABCD1234', expires_in: 600, interval: 1, verification_uri: '/device.html?mode=approve' };
  // Unknown APIs deliberately fail visibly instead of supplying a wrong-shaped success object.
  return undefined;
}

before(async () => {
  fs.mkdirSync(output, { recursive: true });
  server = http.createServer((req, res) => {
    const response = responseFor(new URL(req.url, 'http://fixture'));
    res.statusCode = response.status || 200;
    if (response.contentType) res.setHeader('Content-Type', response.contentType);
    res.end(response.body);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  base = 'http://127.0.0.1:' + server.address().port;
  const executablePath = [process.env.CHROMIUM_PATH, chromium.executablePath(),
    'C:/Program Files/Google/Chrome/Application/chrome.exe'].find(item => item && fs.existsSync(item));
  assert.ok(executablePath, 'A Chromium browser must be installed');
  console.log('Browser: ' + executablePath);
  browser = await chromium.launch({ executablePath, headless: true });
});
after(async () => { await browser?.close(); await new Promise(resolve => server?.close(resolve)); });

async function fixture(options = {}) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, colorScheme: 'light', ...options });
  // Block all external media: fixtures remain useful offline and cannot contact providers.
  await context.route('**/*', route => {
    const url = new URL(route.request().url());
    if (url.origin !== base) return route.abort();
    // Intercept the same server fixture in restricted runners where Chromium cannot use loopback.
    return route.fulfill(responseFor(url));
  });
  const page = await context.newPage(), errors = [];
  page.setDefaultTimeout(7000);
  page.on('pageerror', error => errors.push(error.message));
  return { context, page, errors };
}
async function selectTheme(page, value) {
  const select = page.locator('#theme-select');
  if (!await select.isVisible()) await page.locator('.profile-menu > summary').click();
  await select.selectOption(value);
  // Close a settings popup so it does not cover screenshots or navigation.
  await page.locator('.profile-menu').evaluate(el => { el.open = false; });
}
async function expectTheme(page, theme) {
  await page.waitForFunction(value => document.documentElement.dataset.theme === value, theme);
}
async function expectNoOverflow(page, label) {
  const size = await page.evaluate(() => ({ width: innerWidth, scroll: document.documentElement.scrollWidth,
    wide: [...document.querySelectorAll('body *')].filter(el => el.getBoundingClientRect().right > innerWidth + 2)
      .slice(0, 8).map(el => el.id || el.className || el.tagName) }));
  assert.ok(size.scroll <= size.width + 1, label + ': ' + JSON.stringify(size));
}

test('all eleven pages load the common theme before content and use the new brand', () => {
  assert.equal(htmlPages.length, 11);
  assert.ok(fs.existsSync(path.join(web, 'redesign.css')), 'Common redesign stylesheet exists');
  for (const name of htmlPages) {
    const html = fs.readFileSync(path.join(web, name), 'utf8');
    assert.match(html, /<script[^>]+src=["']theme\.js(?:\?[^"']*)?["'][^>]*><\/script>/, name);
    assert.ok(html.indexOf('theme.js') < html.indexOf('</head>'), name + ' theme should initialize in the head');
    assert.match(html, /<link[^>]+href=["']redesign\.css(?:\?[^"']*)?["']/, name);
    assert.match(html, /<title>[^<]*Кинотека[^<]*<\/title>/, name);
    assert.doesNotMatch(html, /Video Viewer/i, name);
  }
});

test('light/dark/system choices survive reload and navigation, and system follows the browser', async () => {
  const f = await fixture();
  try {
    await f.page.goto(base);
    await expectTheme(f.page, 'light');
    for (const value of ['dark', 'light']) {
      await selectTheme(f.page, value);
      await expectTheme(f.page, value);
      await f.page.reload();
      await expectTheme(f.page, value);
      assert.equal(await f.page.locator('#theme-select').inputValue(), value);
      await f.page.locator('.site-nav a[href="/library.html"]').click();
      await expectTheme(f.page, value);
      assert.equal(await f.page.locator('#theme-select').inputValue(), value);
      await f.page.goto(base);
    }
    await selectTheme(f.page, 'system');
    await expectTheme(f.page, 'light');
    await f.page.emulateMedia({ colorScheme: 'dark' });
    await expectTheme(f.page, 'dark');
    await f.page.reload();
    assert.equal(await f.page.locator('#theme-select').inputValue(), 'system');
    await expectTheme(f.page, 'dark');
    await f.page.emulateMedia({ colorScheme: 'light' });
    await expectTheme(f.page, 'light');
    await selectTheme(f.page, 'light');
    await f.page.emulateMedia({ colorScheme: 'dark' });
    await expectTheme(f.page, 'light');
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('profile keeps language actions and can be dismissed by keyboard or outside click', async () => {
  const f = await fixture();
  try {
    await f.page.goto(base);
    await f.page.locator('.profile-toggle').click();
    await f.page.locator('#lang-en').click();
    assert.equal(await f.page.locator('#search').getAttribute('placeholder'), 'Search the catalog...');
    assert.deepEqual(await f.page.locator('.site-nav a').allTextContents(), ['Catalog', 'Library', 'Calendar', 'IPTV']);
    assert.deepEqual(await f.page.locator('#theme-select option').allTextContents(), ['System', 'Light', 'Dark']);
    assert.equal(await f.page.locator('html').getAttribute('lang'), 'en');
    await f.page.locator('#lang-ru').click();
    assert.equal(await f.page.locator('#search').getAttribute('placeholder'), 'Найти фильм или сериал');
    assert.deepEqual(await f.page.locator('.site-nav a').allTextContents(), ['Каталог', 'Медиатека', 'Календарь', 'IPTV']);
    await f.page.locator('#theme-select').focus();
    await f.page.keyboard.press('Escape');
    assert.equal(await f.page.locator('.profile-menu').evaluate(el => el.open), false);
    assert.equal(await f.page.locator('.profile-toggle').evaluate(el => el === document.activeElement), true);
    await f.page.locator('.profile-toggle').click();
    await f.page.locator('main').click({ position: { x: 10, y: 10 } });
    assert.equal(await f.page.locator('.profile-menu').evaluate(el => el.open), false);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('theme and catalog remain usable when browser storage is denied', async () => {
  const f = await fixture();
  try {
    await f.context.addInitScript(() => {
      for (const method of ['getItem', 'setItem', 'removeItem'])
        Object.defineProperty(Storage.prototype, method, { value() { throw new DOMException('Storage blocked', 'SecurityError'); } });
    });
    await f.page.goto(base);
    assert.deepEqual(f.errors, [], 'Page bootstrap must handle blocked browser storage');
    await f.page.locator('#grid .card').first().waitFor();
    await selectTheme(f.page, 'dark');
    await expectTheme(f.page, 'dark');
    await f.page.locator('#search').fill('Интерстеллар');
    await f.page.waitForFunction(() => document.querySelectorAll('#grid .card').length === 1 && document.querySelector('#grid').textContent.includes('Интерстеллар'));
    await f.page.locator('.profile-toggle').click();
    await f.page.locator('#lang-en').click();
    assert.equal(await f.page.locator('#search').getAttribute('placeholder'), 'Search the catalog...');
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('catalog search uses its original field and preserves continue watching after clearing', async () => {
  const f = await fixture();
  try {
    await f.page.goto(base);
    await f.page.locator('#continue').waitFor({ state: 'visible' });
    await f.page.locator('#search').fill('Интерстеллар');
    await f.page.waitForFunction(() => document.querySelectorAll('#grid .card').length === 1 && document.querySelector('#grid').textContent.includes('Интерстеллар'));
    assert.equal(await f.page.locator('#continue').isVisible(), false);
    assert.ok(requests.some(url => url.startsWith('/api/catalog?') && new URL(url, base).searchParams.get('q') === 'Интерстеллар'));
    await f.page.locator('#search').fill('');
    await f.page.locator('#continue').waitFor({ state: 'visible' });
    await f.page.waitForFunction(() => document.querySelectorAll('#grid .card').length === 8);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('mobile collection menus, filter reset, and resume links preserve the viewing state', async () => {
  const f = await fixture({ viewport: { width: 390, height: 844 }, hasTouch: true });
  try {
    await f.page.goto(base);
    await f.page.locator('#continue .continue-link').first().waitFor();
    const links = await f.page.locator('#continue .continue-link').evaluateAll(els => els.map(el => el.getAttribute('href')));
    for (let i = 0; i < history.length; i++) {
      const url = new URL(links[i], base), item = history[i];
      assert.equal(url.pathname, item.season ? '/film.html' : '/watch.html');
      assert.equal(url.searchParams.get('id'), item.film_id);
      assert.equal(url.searchParams.get('magnet'), item.magnet);
      assert.equal(url.searchParams.get('file'), String(item.file));
      assert.equal(url.searchParams.get('pos'), String(item.position));
      if (item.season) {
        assert.equal(url.searchParams.get('season'), String(item.season));
        assert.equal(url.searchParams.get('ep'), String(item.episode));
        assert.equal(url.searchParams.get('autoplay'), '1');
      }
    }
    const toggle = f.page.locator('[aria-controls="collection-menu-movie"]');
    const best = f.page.getByRole('button', { name: 'Лучшие фильмы', exact: true });
    await toggle.tap();
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true');
    assert.equal(await best.isVisible(), true);
    await expectNoOverflow(f.page, 'mobile category menu');
    await toggle.press('Escape');
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
    await toggle.tap();
    await f.page.locator('.catalog-heading').tap();
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
    await toggle.tap();
    const [collectionRequest] = await Promise.all([
      f.page.waitForRequest(req => {
        const url = new URL(req.url());
        return url.pathname === '/api/catalog' && url.searchParams.get('collection') === 'best';
      }),
      best.tap(),
    ]);
    assert.equal(new URL(collectionRequest.url()).searchParams.get('section'), 'movie');
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
    await f.page.locator('#filter-toggle').tap();
    await f.page.locator('#search').fill('Интерстеллар');
    await f.page.waitForFunction(() => document.querySelectorAll('#grid .card').length === 1 && document.querySelector('#grid').textContent.includes('Интерстеллар'));
    await f.page.locator('#genre').selectOption('Drama');
    await f.page.locator('#sort').selectOption('rating');
    await f.page.locator('#released').uncheck();
    await f.page.locator('#hide-watched').check();
    const [resetRequest] = await Promise.all([
      f.page.waitForRequest(req => {
        const url = new URL(req.url()), p = url.searchParams;
        return url.pathname === '/api/catalog' && p.get('section') === 'all' && p.get('sort') === 'year'
          && p.get('released') === '1' && !p.has('q') && !p.has('genre') && !p.has('collection');
      }),
      f.page.locator('#catalog-reset').tap(),
    ]);
    assert.ok(resetRequest);
    assert.equal(await f.page.locator('#search').inputValue(), '');
    assert.equal(await f.page.locator('#genre').inputValue(), '');
    assert.equal(await f.page.locator('#sort').inputValue(), 'year');
    assert.equal(await f.page.locator('#released').isChecked(), true);
    assert.equal(await f.page.locator('#hide-watched').isChecked(), false);
    assert.equal(await f.page.locator('[data-section="all"]').getAttribute('aria-pressed'), 'true');
    await f.page.locator('#continue').waitFor({ state: 'visible' });
    await f.page.waitForFunction(() => document.querySelectorAll('#grid .card').length === 8);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('TV Back closes profile and Enter on continue links resumes the saved film or episode', async () => {
  const f = await fixture();
  try {
    await f.page.goto(base + '/?tv=1');
    await f.page.locator('#continue .continue-link').first().waitFor();
    assert.equal(await f.page.locator('body').evaluate(el => el.classList.contains('tv')), true);
    await f.page.locator('.profile-toggle').focus();
    await f.page.keyboard.press('Enter');
    await f.page.waitForFunction(() => document.querySelector('.profile-menu').open);
    assert.equal(await f.page.evaluate(() => window.__vvBack()), 'consumed');
    await f.page.waitForFunction(() => !document.querySelector('.profile-menu').open
      && document.activeElement === document.querySelector('.profile-toggle'));
    for (let i = 0; i < history.length; i++) {
      if (i > 0) {
        await f.page.goto(base + '/?tv=1');
        await f.page.locator('#continue .continue-link').nth(i).waitFor();
      }
      const item = history[i], expectedPath = item.season ? '/film.html' : '/watch.html';
      await f.page.locator('#continue .continue-link').nth(i).focus();
      await Promise.all([
        f.page.waitForURL(url => url.pathname === expectedPath && url.searchParams.get('id') === item.film_id,
          { waitUntil: 'domcontentloaded' }),
        f.page.keyboard.press('Enter'),
      ]);
      // The destination intentionally keeps only its film ID in the address bar;
      // playback source/position live in the restored per-entry history state.
      await f.page.waitForFunction(() => !!history.state?.playback);
      const saved = new URLSearchParams(await f.page.evaluate(() => history.state.playback));
      assert.equal(saved.get('id'), item.film_id);
      assert.equal(saved.get('magnet'), item.magnet);
      assert.equal(saved.get('pos'), String(item.position));
      assert.equal(saved.get('file'), String(item.file));
      assert.equal(await f.page.locator('body').evaluate(el => el.classList.contains('tv')), true);
      if (item.season) {
        assert.equal(saved.get('season'), String(item.season));
        assert.equal(saved.get('ep'), String(item.episode));
        assert.equal(saved.get('autoplay'), '1');
      }
    }
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('all pages have one shared navigation, coherent surfaces, and fit desktop/mobile in both themes', { timeout: 120000 }, async () => {
  const f = await fixture();
  const overflow = [];
  const checkSize = async label => {
    try { await expectNoOverflow(f.page, label); }
    catch (error) { overflow.push(error.message); }
  };
  try {
    for (const theme of ['light', 'dark']) {
      await f.page.goto(base);
      await selectTheme(f.page, theme);
      for (const name of htmlPages) {
        const route = name === 'film.html' ? '/film.html?id=' + films[0].id
          : name === 'watch.html' ? '/watch.html?id=' + films[0].id : '/' + name;
        await f.page.setViewportSize({ width: 1440, height: 1000 });
        await f.page.goto(base + route);
        await expectTheme(f.page, theme);
        assert.equal(await f.page.locator('.site-nav').count(), 1, name + ' shared navigation count');
        assert.equal(await f.page.locator('.feature-nav').count(), 0, name + ' legacy navigation should be removed');
        const links = await f.page.locator('.site-nav a').evaluateAll(els => els.map(el => el.getAttribute('href')));
        assert.equal(new Set(links).size, links.length, name + ' repeated navigation links');
        const active = await f.page.locator('.site-nav [aria-current]').count();
        assert.equal(active, ['index.html', 'film.html', 'watch.html', 'library.html', 'calendar.html', 'iptv.html'].includes(name) ? 1 : 0, name + ' active section');
        const color = await f.page.locator('body').evaluate(el => getComputedStyle(el).backgroundColor);
        const rgb = color.match(/\d+/g).slice(0, 3).map(Number);
        assert.equal(rgb.reduce((a, b) => a + b) / 3 > 128, theme === 'light', name + ' page background ' + color);
        if (name === 'index.html') await f.page.locator('#grid .card').first().waitFor();
        if (name === 'library.html') await f.page.locator('.feature-card').first().waitFor();
        if (name === 'film.html') await f.page.locator('#details').waitFor({ state: 'visible' });
        if (name === 'iptv.html') await f.page.locator('.iptv-card').first().waitFor();
        if (name === 'storage.html') await f.page.locator('#storage-content').waitFor({ state: 'visible' });
        if (name === 'watch.html') {
          // Playback is outside this appearance fixture; reveal its real empty player for theme QA.
          await f.page.locator('#player-wrap').evaluate(el => { el.hidden = false; });
          const bg = await f.page.locator('#player-wrap').evaluate(el => getComputedStyle(el).backgroundColor);
          assert.ok(bg.match(/\d+/g).slice(0, 3).map(Number).every(n => n < 64), 'player stays dark: ' + bg);
        }
        await checkSize(name + '/' + theme + '/desktop');
        await f.page.screenshot({ path: path.join(output, name.replace('.html', '') + '-' + theme + '-desktop.png'), fullPage: true, animations: 'disabled' });
        await f.page.setViewportSize({ width: 390, height: 844 });
        await checkSize(name + '/' + theme + '/mobile');
        if (name === 'watch.html') {
          const clipped = await f.page.locator('#player-wrap').evaluate(wrap => {
            const bounds = wrap.getBoundingClientRect();
            return ['ctrl-play', 'ctrl-mute', 'ctrl-fullscreen'].map(id => {
              const el = document.getElementById(id), rect = el.getBoundingClientRect();
              return { id, width: rect.width, left: rect.left, right: rect.right, containerLeft: bounds.left, containerRight: bounds.right };
            }).filter(rect => rect.width === 0 || rect.left < bounds.left - 1 || rect.right > bounds.right + 1);
          });
          if (clipped.length) overflow.push(name + '/' + theme + '/mobile clipped essential player controls: ' + JSON.stringify(clipped));
        }
        await f.page.locator('.profile-menu > summary').click();
        await checkSize(name + '/' + theme + '/mobile/profile');
        await f.page.locator('.profile-menu > summary').click();
        if (name === 'index.html') {
          const filters = f.page.locator('#filter-toggle');
          await filters.click();
          assert.equal(await filters.getAttribute('aria-expanded'), 'true');
          assert.equal(await f.page.locator('#filter-controls').isVisible(), true);
          await checkSize(name + '/' + theme + '/mobile/filters');
        }
        await f.page.screenshot({ path: path.join(output, name.replace('.html', '') + '-' + theme + '-mobile.png'), fullPage: true, animations: 'disabled' });
      }
    }
    assert.deepEqual(f.errors, []);
    assert.deepEqual(overflow, [], 'Page overflow issues');
  } finally { await f.context.close(); }
});

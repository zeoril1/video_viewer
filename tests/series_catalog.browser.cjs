// Real film page integration, with deterministic catalog and tracker fixtures.
// Run with Playwright/Chromium; no external TMDB or torrent requests are made.
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');

const web = path.resolve(__dirname, '../web');
const calls = [];
const pendingSources = [];
let holdSources = true;
let unavailableRefresh = false;
let coldReadyResponse = null, coldRequestNotify = () => {};
const films = {
  tt200: { id: 'tt200', imdb_id: 'tt200', tmdb_id: '200', kind: 'tvSeries', title: 'Catalog series', title_ru: 'Сериал из каталога', seasons: 3, plot_ru: 'Описание', director: 'Режиссёр', actors: ['Актёр'], movie_length: 45 },
  tt201: { id: 'tt201', imdb_id: 'tt201', tmdb_id: '201', kind: 'tvSeries', title: 'Cached series', title_ru: 'Сериал из кэша', seasons: 2, plot_ru: 'Описание', director: 'Режиссёр', actors: ['Актёр'], movie_length: 45 },
  tt202: { id: 'tt202', imdb_id: 'tt202', tmdb_id: '202', kind: 'tvSeries', title: 'Cold series', title_ru: 'Сериал без кэша', seasons: 3, plot_ru: 'Описание', director: 'Режиссёр', actors: ['Актёр'], movie_length: 45 },
};
const seasonRows = [
  { season: 1, episodes: 3, name: 'Первый сезон', air_date: '2020-01-01' },
  { season: 2, episodes: 2, name: 'Второй сезон', air_date: '2021-01-01' },
  { season: 3, episodes: 1, name: 'Третий сезон', air_date: '2022-01-01' },
];
const sources = [{ magnet: 'magnet:?xt=urn:btih:' + 'a'.repeat(40), title: 'Catalog series S01 LostFilm WEB-DL 1080p', season: 1, seeds: 10, quality: '1080' }];

const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://fixture');
  calls.push({ path: u.pathname, method: req.method });
  const send = (data, status = 200) => {
    if (res.destroyed) return;
    res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' });
    res.end(JSON.stringify(data));
  };
  if (u.pathname === '/api/auth/me') return send({}, 401);
  const filmRoute = u.pathname.match(/^\/api\/films\/(tt\d+)(?:\/(.*))?$/);
  if (filmRoute) {
    const [, id, endpoint = ''] = filmRoute;
    if (!endpoint) return send(films[id] || {}, films[id] ? 200 : 404);
    if (endpoint === 'seasons') {
      if (id === 'tt202') {
        if (calls.filter(x => x.path === '/api/films/tt202/seasons').length === 1) return send({ id, status: 'loading', seasons: [] });
        coldReadyResponse = () => send({ id, tmdb_id: 202, status: 'ready', seasons: seasonRows });
        coldRequestNotify();
        return;
      }
      if (id === 'tt200' && unavailableRefresh) return send({ id, status: 'unavailable', seasons: [] });
      const cached = id === 'tt201';
      return send({ id, tmdb_id: Number(films[id].tmdb_id), status: cached ? 'unavailable' : 'ready', stale: cached, seasons: cached ? seasonRows.slice(0, 2) : seasonRows });
    }
    if (endpoint.startsWith('seasons/')) {
      const season = Number(endpoint.split('/')[1]);
      if (id === 'tt200' && unavailableRefresh) return send({ id, season, status: 'unavailable', episodes: [] });
      const unavailable = id === 'tt201' || season === 3;
      return send({ id, tmdb_id: Number(films[id].tmdb_id), season, status: unavailable ? 'unavailable' : 'ready', stale: unavailable, episodes: unavailable ? [] : Array.from({ length: seasonRows[season - 1].episodes }, (_, i) => ({ episode: i + 1, name: `Название S${season}E${i + 1}`, air_date: `202${season}-01-0${i + 1}` })) });
    }
    if (endpoint === 'sources') {
      if (id === 'tt202') return send({ status: 'ready', items: sources });
      // A tracker may still carry obsolete season counts. The independent catalog
      // must remain the authority, including cached data during upstream failure.
      const finish = () => send({ status: 'ready', items: sources, seasons: [{ season: 1, episodes: 99 }] });
      if (holdSources) pendingSources.push(finish);
      else finish();
      return;
    }
    if (endpoint === 'files') return send({ files: [{ index: 0, season: 1, episode: 1 }] });
    if (endpoint === 'watch-order' || endpoint === 'explore') return send({ items: [] });
  }
  if (u.pathname.startsWith('/api/')) return send({ items: [], sections: {}, genres: [] });
  const filename = path.resolve(web, '.' + (u.pathname === '/' ? '/film.html' : u.pathname));
  if (!filename.startsWith(web + path.sep) || !fs.existsSync(filename)) {
    res.writeHead(404);
    return res.end();
  }
  res.setHeader('Content-Type', filename.endsWith('.js') ? 'application/javascript' : filename.endsWith('.css') ? 'text/css' : 'text/html; charset=utf-8');
  res.end(fs.readFileSync(filename));
});

function releaseSources() {
  holdSources = false;
  for (const finish of pendingSources.splice(0)) finish();
}
function detailCalls(id) {
  return calls.filter(x => x.path.startsWith(`/api/films/${id}/seasons/`)).map(x => Number(x.path.split('/').pop()));
}
function fileCalls() {
  return calls.filter(x => x.path.endsWith('/files'));
}
async function expectSeason(page, number, count) {
  await page.waitForFunction(({ number, count }) => {
    const selected = document.querySelector('#sources-season .active');
    return selected && selected.textContent.includes(String(number)) && document.querySelectorAll('#sources-episodes .ep-btn').length === count;
  }, { number, count });
  assert.equal(await page.locator('#sources-episodes .ep-btn').count(), count);
}

(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined, args: ['--no-sandbox'] });
  const errors = [];
  const base = `http://127.0.0.1:${server.address().port}`;
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
    page.on('pageerror', e => errors.push(e.message));
    await page.goto(base + '/film.html?id=tt200');
    await expectSeason(page, 1, 3);
    await page.waitForFunction(() => SeriesCatalog.episodes('tt200', 1).length === 3);
    assert.match(await page.locator('#sources-episodes .ep-btn').first().getAttribute('title'), /Название S1E1/);
    assert.equal(await page.locator('#sources-season .chip').count(), 3, 'All canonical seasons appear before tracker sources finish');
    assert.ok(pendingSources.length, 'Tracker source response is still held');
    assert.deepEqual(detailCalls('tt200'), [1], 'Only the initial selected season details are fetched');
    assert.equal(fileCalls().length, 0, 'Opening a series grid must not open torrent metadata');

    await page.locator('#sources-season .chip').nth(1).click();
    await expectSeason(page, 2, 2);
    await page.waitForFunction(() => SeriesCatalog.episodes('tt200', 2).length === 2);
    assert.deepEqual(detailCalls('tt200'), [1, 2], 'Changing a season fetches its details lazily');
    assert.equal(fileCalls().length, 0);

    await page.locator('#sources-season .chip').nth(2).click();
    await expectSeason(page, 3, 1);
    await page.waitForFunction(() => SeriesCatalog.episodes('tt200', 3).length === 0);
    assert.equal(await page.locator('#sources-episodes .ep-btn').count(), 1, 'Unavailable episode names do not erase canonical episode counts');
    releaseSources();
    await page.locator('#sources-rel .chip').first().waitFor();
    await expectSeason(page, 3, 1);
    assert.equal(fileCalls().length, 0, 'Late tracker data must not trigger probes or replace the catalog grid');

    await page.locator('#sources-season .chip').first().click();
    await expectSeason(page, 1, 3);
    assert.equal(await page.locator('#sources-episodes .ep-btn:enabled').count(), 3, 'Cached canonical episodes can be played when their source appears');
    assert.equal(detailCalls('tt200').filter(n => n === 1).length, 1, 'Previously fetched season details are reused');
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'Season and episode grids fit a phone screen');

    unavailableRefresh = true;
    await page.evaluate(async () => {
      await SeriesCatalog.load('tt200', undefined, true);
      await SeriesCatalog.loadEpisodes('tt200', 1, undefined, true);
    });
    assert.equal(await page.evaluate(() => SeriesCatalog.seasons('tt200').length), 3, 'An unavailable refresh cannot erase remembered season rows');
    assert.equal(await page.evaluate(() => SeriesCatalog.episodes('tt200', 1).length), 3, 'An unavailable refresh cannot erase remembered episode names');
    await expectSeason(page, 1, 3);
    unavailableRefresh = false;

    const failures = await page.evaluate(async () => {
      const originalFetch = window.fetch, originalTimeout = window.setTimeout;
      SeriesCatalog.cancel();
      window.fetch = (url, options = {}) => {
        if (url === '/api/films/tt900/seasons') return Promise.reject(new TypeError('fixture network failure'));
        if (url === '/api/films/tt901/seasons') return Promise.resolve({ ok: true, json: async () => ({ id: 'another-series', status: 'ready', seasons: [{ season: 1, episodes: 99 }] }) });
        if (url === '/api/films/tt902/seasons' || url === '/api/films/tt903/seasons') return new Promise((resolve, reject) => {
          options.signal.addEventListener('abort', () => reject(new DOMException('fixture abort', 'AbortError')), { once: true });
        });
        return originalFetch(url, options);
      };
      try {
        const failed = [], mismatched = [], canceled = [], timedOut = [];
        await SeriesCatalog.load('tt900', data => failed.push(data.status));
        await SeriesCatalog.load('tt901', data => mismatched.push(data.status));
        const pending = SeriesCatalog.load('tt902', data => canceled.push(data.status));
        SeriesCatalog.cancel();
        await pending;
        // Exercise the actual request-timeout branch without a ten-second pause.
        window.setTimeout = (fn, delay, ...args) => originalTimeout(fn, delay === 10000 ? 10 : delay, ...args);
        await SeriesCatalog.load('tt903', data => timedOut.push(data.status));
        return { failed, mismatched, canceled, timedOut, wrongSeasons: SeriesCatalog.seasons('tt901') };
      } finally { window.fetch = originalFetch; window.setTimeout = originalTimeout; }
    });
    assert.deepEqual(failures, { failed: ['unavailable'], mismatched: ['unavailable'], canceled: [], timedOut: ['unavailable'], wrongSeasons: [] }, 'Network failures and timeout settle; canceled or mismatched responses cannot supply season data');

    // Reloading starts a new browser session cache; the persistent catalog API is
    // enough to reconstruct the grid without opening torrent metadata again.
    await page.reload();
    await expectSeason(page, 1, 3);
    assert.equal(fileCalls().length, 0);
    await page.goto(base + '/film.html?id=tt201');
    await expectSeason(page, 1, 3);
    assert.equal(await page.locator('#sources-season .chip').count(), 2, 'Cached season counts remain visible while upstream is unavailable');
    await page.locator('#sources-season .chip').nth(1).click();
    await expectSeason(page, 2, 2);
    assert.equal(fileCalls().length, 0, 'Cached unavailable metadata still prevents grid-time torrent probes');

    // Opposite ordering: tracker sources arrive first on a cold catalog cache.
    // Holding the second metadata response makes unintended torrent probes
    // observable while the initial loading result is being refreshed.
    let coldWaitTimer;
    const coldRequested = new Promise((resolve, reject) => {
      coldRequestNotify = () => { clearTimeout(coldWaitTimer); resolve(); };
      coldWaitTimer = setTimeout(() => reject(new Error('Catalog did not retry the cold loading result')), 8000);
    });
    await page.goto(base + '/film.html?id=tt202');
    await page.locator('#sources-rel .chip').first().waitFor();
    await coldRequested;
    assert.equal(fileCalls().length, 0, 'Ready tracker sources must wait for cold canonical metadata before probing torrent files');
    assert.equal(await page.locator('#sources-episodes .ep-btn').count(), 0, 'Cold metadata does not produce guessed episode counts');
    coldReadyResponse();
    await expectSeason(page, 1, 3);
    await page.waitForFunction(() => SeriesCatalog.episodes('tt202', 1).length === 3);
    assert.equal(fileCalls().length, 0, 'The ready canonical grid still needs no torrent probes');
    assert.deepEqual(detailCalls('tt202'), [1]);
    assert.deepEqual(errors, []);
    console.log('Series catalog browser checks passed: independent metadata, lazy season details, cached/stale counts, tracker and cold-cache delays, reload, no grid-time torrent probes, timeout/cancel/error recovery, mobile layout.');
  } finally {
    releaseSources();
    await browser.close();
    server.closeAllConnections();
    server.close();
  }
})().catch(error => { console.error(error); server.closeAllConnections(); server.close(); process.exitCode = 1; });

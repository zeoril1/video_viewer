// Real film/series page integration, with deterministic offline API and media fixtures.
// Run: PLAYWRIGHT_MODULE=<bundled playwright path> node --test tests/film_page.browser.cjs
'use strict';
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const web = path.resolve(__dirname, '../web');
const output = process.env.DESIGN_SCREENSHOTS || path.resolve(__dirname, '../output/design-preview');
const savedMagnet = 'magnet:?xt=urn:btih:' + 'a'.repeat(40);
const otherMagnet = 'magnet:?xt=urn:btih:' + 'b'.repeat(40);
const movie = { id: 'tt7001', imdb_id: 'tt7001', tmdb_id: '7001', kind: 'feature',
  title: 'Dune: Part Two', title_ru: 'Дюна: Часть вторая', year: 2024, movie_length: 166,
  genres: ['Adventure', 'Drama', 'Sci-Fi'], countries: ['США', 'Канада'], rating: 8.5, rating_tmdb: 8.2,
  director: 'Дени Вильнёв', actors: ['Тимоти Шаламе', 'Зендея', 'Ребекка Фергюсон'],
  poster_url: '/fixtures/movie.svg', plot_ru: 'Пол Атрейдес объединяется с Чани и фрименами, чтобы отомстить заговорщикам, уничтожившим его семью. Выбор между любовью и судьбой становится выбором будущего целой вселенной.' };
const series = { ...movie, id: 'tt7002', imdb_id: 'tt7002', tmdb_id: '7002', kind: 'tvSeries',
  title: 'Silo', title_ru: 'Укрытие', year: 2023, movie_length: 48, seasons: 2, poster_url: '/fixtures/series.svg',
  genres: ['Drama', 'Sci-Fi'], director: 'Мортен Тильдум', actors: ['Ребекка Фергюсон', 'Тим Роббинс'],
  plot_ru: 'В подземном убежище живут последние люди на Земле. Инженер Джульетта начинает расследование и обнаруживает тайны, которые могут изменить жизнь каждого обитателя.' };
const suggestions = Array.from({ length: 9 }, (_, i) => ({ ...movie, id: 'tt710' + i, imdb_id: 'tt710' + i,
  title_ru: ['Интерстеллар', 'Начало', 'Бегущий по лезвию 2049', 'Прибытие', 'Дюна', 'Марсианин', 'Контакт', 'Гравитация', 'Луна 2112'][i],
  title: 'Recommended film ' + i, poster_url: '/fixtures/related-' + i + '.svg' }));
const episodes = season => [
  { episode: 1, name: season === 1 ? 'Свобода' : 'Инженер', air_date: season === 1 ? '2023-05-05' : '2024-11-15', runtime: 49,
    overview: 'Джульетта узнаёт о загадочных событиях в убежище и начинает искать ответы.' },
  { episode: 2, name: season === 1 ? 'Холстон' : 'Порядок', air_date: season === 1 ? '2023-05-05' : '2024-11-22', runtime: 46,
    overview: 'Новая улика заставляет героев переосмыслить знакомые правила.' },
  // Intentionally incomplete metadata: the episode remains usable without invented details.
  { episode: 3 },
];
const files = [1, 2].flatMap(season => [1, 2, 3].map(episode => ({ index: (season - 1) * 3 + episode - 1,
  name: `Silo.S0${season}E0${episode}.mkv`, season, episode, size: 2 ** 30 })));
const history = [movie, series].map(item => ({ ...item, film_id: item.id, magnet: savedMagnet,
  position: 1500, duration: item === series ? 2880 : 9960, file: item === series ? 4 : 0,
  season: item === series ? 2 : 0, episode: item === series ? 2 : 0,
  voice: 'LostFilm', track: 2, subs: 3, quality: '720', updated_at: '2026-10-08T08:00:00Z' }));
const sources = id => [{ magnet: savedMagnet, title: id === series.id ? 'Silo S01-S02 LostFilm WEB-DL 1080p' : 'Dune Part Two 1080p', seasons: [1, 2], seeds: 30, quality: '1080', size: '20 GB' },
  { magnet: otherMagnet, title: id === series.id ? 'Silo S01-S02 NewStudio WEB-DL 720p' : 'Dune Part Two 720p', seasons: [1, 2], seeds: 12, quality: '720', size: '10 GB' }];
let server, browser, base;

function poster(filename) {
  const isSeries = filename.includes('series'), title = isSeries ? series.title_ru : filename.includes('related') ? 'КИНОТЕКА' : movie.title_ru;
  const colors = isSeries ? ['#203836', '#9cac82'] : ['#b16939', '#343345'];
  return `<svg xmlns="http://www.w3.org/2000/svg" width="480" height="720"><defs><linearGradient id="g" x2=".8" y2="1"><stop stop-color="${colors[0]}"/><stop offset="1" stop-color="${colors[1]}"/></linearGradient></defs><rect width="480" height="720" fill="url(#g)"/><circle cx="320" cy="200" r="120" fill="#fff" opacity=".14"/><path d="M0 570L180 300L480 580V720H0" fill="#101018" opacity=".5"/><text x="30" y="650" fill="white" font-family="Arial" font-size="27" font-weight="700">${title}</text></svg>`;
}
function staticResponse(url) {
  if (url.pathname.startsWith('/fixtures/')) return { contentType: 'image/svg+xml', body: poster(url.pathname) };
  const filename = path.resolve(web, '.' + url.pathname);
  if (!filename.startsWith(web + path.sep) || !fs.existsSync(filename) || !fs.statSync(filename).isFile()) return { status: 404, body: '' };
  return { contentType: filename.endsWith('.js') ? 'application/javascript' : filename.endsWith('.css') ? 'text/css' : 'text/html; charset=utf-8', body: fs.readFileSync(filename) };
}
before(async () => {
  fs.mkdirSync(output, { recursive: true });
  server = http.createServer((req, res) => { const response = staticResponse(new URL(req.url, 'http://fixture')); res.statusCode = response.status || 200; res.setHeader('Content-Type', response.contentType || 'text/plain'); res.end(response.body); });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  base = 'http://127.0.0.1:' + server.address().port;
  const executablePath = [process.env.CHROMIUM_PATH, chromium.executablePath(), 'C:/Program Files/Google/Chrome/Application/chrome.exe'].find(value => value && fs.existsSync(value));
  assert.ok(executablePath, 'Chromium must be installed');
  browser = await chromium.launch({ executablePath, headless: true });
});
after(async () => { await browser?.close(); await new Promise(resolve => server?.close(resolve)); });

async function fixture({ resume = true, theme = 'light', width = 1440 } = {}) {
  const context = await browser.newContext({ viewport: { width, height: width < 600 ? 844 : 1000 }, colorScheme: theme });
  const calls = [], errors = [], unexpected = [], personal = [{ kind: 'preferences', key: 'player', data: { prepare: false } }];
  await context.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) return route.abort();
    if (!url.pathname.startsWith('/api/')) return route.fulfill(staticResponse(url));
    const body = request.postData() ? JSON.parse(request.postData()) : null;
    calls.push({ path: url.pathname, search: url.search, method: request.method(), body });
    let data, status = 200;
    const pathname = url.pathname;
    if (pathname === '/api/auth/me') data = { user: { id: 1, username: 'Андрей' } };
    else if (pathname === '/api/stream/viewing') status = 204;
    else if (pathname === '/api/history') data = { items: resume ? history.filter(item => !url.searchParams.has('film_id') || url.searchParams.get('film_id') === item.film_id) : [] };
    else if (pathname === '/api/personal') data = { items: personal };
    else if (pathname.startsWith('/api/personal/')) {
      const [, , , kind, key] = pathname.split('/');
      const index = personal.findIndex(item => item.kind === kind && item.key === decodeURIComponent(key));
      if (index >= 0) personal.splice(index, 1);
      if (request.method() === 'PUT') personal.push({ kind, key: decodeURIComponent(key), data: body, updated_at: new Date().toISOString() });
      status = 204;
    } else {
      const match = pathname.match(/^\/api\/films\/(tt\d+)(?:\/(.*))?$/);
      if (match) {
        const [, id, endpoint = ''] = match;
        if (!endpoint) data = id === series.id ? series : movie;
        else if (endpoint === 'sources') data = { status: 'ready', items: sources(id), seasons: [{ season: 1, episodes: 3 }, { season: 2, episodes: 3 }] };
        else if (endpoint === 'seasons') data = { id, tmdb_id: 7002, status: 'ready', seasons: [{ season: 1, episodes: 3, name: 'Первый сезон' }, { season: 2, episodes: 3, name: 'Второй сезон' }] };
        else if (/^seasons\/\d+$/.test(endpoint)) { const season = Number(endpoint.split('/')[1]); data = { id, season, status: 'ready', episodes: episodes(season) }; }
        else if (endpoint === 'files') data = { files: id === series.id ? files : [{ index: 0, name: 'Dune.Part.Two.mkv' }] };
        else if (endpoint === 'explore') data = { items: suggestions, cast: [{ id: 10, name: id === series.id ? 'Ребекка Фергюсон' : 'Тимоти Шаламе' }], directors: [{ id: 20, name: id === series.id ? 'Мортен Тильдум' : 'Дени Вильнёв' }], trailer: 'https://www.youtube.com/watch?v=abcdefghijk' };
        else if (endpoint === 'watch-order') data = { items: [] };
      }
    }
    if (data === undefined && status !== 204) { unexpected.push(pathname); status = 404; data = { error: 'Missing fixture: ' + pathname }; }
    return route.fulfill({ status, contentType: 'application/json', body: status === 204 ? '' : JSON.stringify(data) });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(7000);
  page.on('pageerror', error => errors.push(error.message));
  await context.addInitScript(value => { try { localStorage.setItem('kinoteka_theme', value); } catch (_) { /* Blocked trailer iframe storage is expected. */ } }, theme);
  return { context, page, calls, errors, unexpected, personal };
}
async function open(f, item = movie) {
  await f.page.goto(base + '/film.html?id=' + item.id);
  try {
    await f.page.waitForFunction(title => document.querySelector('#details-title')?.textContent === title, item.title_ru);
    await f.page.waitForFunction(() => lastSourceItems.length > 0);
  } catch (error) {
    error.message += '\nFixture diagnostics: ' + JSON.stringify({ errors: f.errors, unexpected: f.unexpected, calls: f.calls,
      title: await f.page.locator('#details-title').textContent() });
    throw error;
  }
  await f.page.locator('#manual-sources').waitFor({ state: 'attached' });
  if (item === series) await f.page.waitForFunction(() => document.querySelectorAll('#sources-episodes .ep-btn').length === 3 && /Порядок|Холстон/.test(document.querySelector('#sources-episodes').textContent));
}
async function capturePlayback(page) {
  await page.evaluate(() => {
    window.__plays = [];
    window.__playing = null;
    window.__stopCalls = 0;
    PP.start = options => {
      const copy = JSON.parse(JSON.stringify(options));
      window.__plays.push(copy);
      window.__playing = copy;
      const state = { id: copy.id, magnet: copy.magnet, file: copy.file, season: copy.season || 0, episode: copy.ep || 0,
        voice: copy.voice || '', position: copy.pos || 0, active: true, playing: true, quality: copy.quality || 'source' };
      document.getElementById('player-wrap').hidden = false;
      onPlayerState(state);
      window.dispatchEvent(new CustomEvent('playbackchange', { detail: state }));
    };
    PP.playing = () => !!window.__playing;
    PP.stop = () => {
      window.__stopCalls++;
      window.__playing = null;
      document.getElementById('player-wrap').hidden = true;
      onPlayerState({ active: false, playing: false });
      window.dispatchEvent(new Event('playbackstop'));
    };
    PP.state = () => window.__playing ? { ...window.__playing, episode: window.__playing.ep, active: true } : { active: false };
    PP.season = () => window.__playing?.season || 0;
    PP.episode = () => window.__playing?.ep || 0;
    PP.release = () => window.__playing?.release || '';
    go = url => {
      const parsed = new URL(url, location.href), id = parsed.searchParams.get('id');
      window.__navigation = { pathname: parsed.pathname, params: Object.fromEntries(new URLSearchParams(sessionStorage.getItem('vv:navigate:' + parsed.pathname + ':' + id) || parsed.search)) };
    };
  });
}
async function noOverflow(page, label) {
  const size = await page.evaluate(() => ({ width: innerWidth, scroll: document.documentElement.scrollWidth,
    wide: [...document.querySelectorAll('body *')].filter(el => el.getBoundingClientRect().right > innerWidth + 2).slice(0, 8).map(el => el.id || el.className) }));
  assert.ok(size.scroll <= size.width + 1, label + ': ' + JSON.stringify(size));
}
function healthy(f) { assert.deepEqual(f.errors, []); assert.deepEqual(f.unexpected, []); }

test('movie hero has one title and one main playback action; history keeps its exact source', async () => {
  const f = await fixture();
  try {
    await open(f);
    assert.equal(await f.page.locator('h1').count(), 1);
    assert.equal((await f.page.locator('h1').textContent()).trim(), movie.title_ru);
    const duplicates = await f.page.evaluate(() => {
      const ids = [...document.querySelectorAll('[id]')].map(el => el.id);
      return [...new Set(ids.filter((id, index) => ids.indexOf(id) !== index))];
    });
    assert.deepEqual(duplicates, [], 'Page controls have unique identifiers');
    assert.equal(await f.page.locator('#resume-btn').isVisible(), true);
    assert.equal(await f.page.locator('#watch-btn').isVisible(), false);
    await capturePlayback(f.page);
    await f.page.locator('#resume-btn').click();
    const navigation = await f.page.evaluate(() => window.__navigation);
    assert.equal(navigation.pathname, '/watch.html');
    assert.equal(navigation.params.id, movie.id);
    assert.equal(navigation.params.magnet, savedMagnet);
    assert.equal(navigation.params.file, '0');
    assert.equal(navigation.params.pos, '1500');
    assert.equal(navigation.params.voice, 'LostFilm');
    await f.page.locator('#startover-btn').click();
    const restart = await f.page.evaluate(() => window.__navigation.params);
    assert.equal(restart.magnet, savedMagnet); assert.equal(restart.file, '0');
    assert.equal(restart.pos, '0', 'An explicit zero position prevents the player from resuming its history');
    healthy(f);
  } finally { await f.context.close(); }
});

test('series resume identifies season/episode and start over keeps the saved source and file', async () => {
  const f = await fixture();
  try {
    await open(f, series);
    assert.equal(await f.page.locator('h1').count(), 1);
    assert.match(await f.page.locator('#resume-btn').textContent(), /2.*2/);
    assert.equal(await f.page.locator('#watch-btn').isVisible(), false);
    assert.match(await f.page.locator('#film-resume-note').textContent(), /25:00/);
    assert.equal(await f.page.locator('#film-progress').getAttribute('aria-valuenow'), '52');
    assert.equal(await f.page.locator('#sources-episodes .ep-btn').nth(1).locator('.episode-progress > span').evaluate(el => el.style.width), '52%');
    await capturePlayback(f.page);
    await f.page.locator('#resume-btn').click();
    assert.deepEqual(await f.page.evaluate(() => { const play = window.__plays.at(-1); return { id: play.id, magnet: play.magnet, file: play.file, season: play.season, ep: play.ep, pos: play.pos, voice: play.voice }; }),
      { id: series.id, magnet: savedMagnet, file: 4, season: 2, ep: 2, pos: 1500, voice: 'LostFilm' });
    await f.page.locator('#startover-btn').click();
    assert.deepEqual(await f.page.evaluate(() => { const play = window.__plays.at(-1); return { magnet: play.magnet, file: play.file, season: play.season, ep: play.ep, pos: play.pos }; }),
      { magnet: savedMagnet, file: 4, season: 2, ep: 2, pos: 0 });
    healthy(f);
  } finally { await f.context.close(); }
});

test('the main Watch button launches both a fresh movie and the selected season of a fresh series', async () => {
  for (const item of [movie, series]) {
    const f = await fixture({ resume: false });
    try {
      await open(f, item);
      await capturePlayback(f.page);
      assert.equal(await f.page.locator('#watch-btn').isVisible(), true);
      assert.equal(await f.page.locator('#resume-btn').isVisible(), false);
      if (item === series) {
        await f.page.locator('#sources-season .chip').nth(1).click();
        await f.page.waitForFunction(() => document.querySelector('#sources-episodes').textContent.includes('Инженер'));
      }
      await f.page.locator('#watch-btn').click();
      if (item === series) {
        await f.page.waitForFunction(() => window.__plays.length === 1);
        const play = await f.page.evaluate(() => window.__plays[0]);
        assert.equal(play.id, series.id); assert.equal(play.season, 2); assert.equal(play.ep, 1); assert.equal(play.file, 3);
      } else {
        const navigation = await f.page.evaluate(() => window.__navigation);
        assert.equal(navigation.pathname, '/watch.html'); assert.equal(navigation.params.id, movie.id);
        assert.equal(navigation.params.magnet, savedMagnet);
      }
      healthy(f);
    } finally { await f.context.close(); }
  }
});

test('season browsing stays silent, rich episode metadata is visible, and season marks use the selection', async () => {
  const f = await fixture({ resume: false });
  try {
    await open(f, series);
    await capturePlayback(f.page);
    assert.equal(await f.page.locator('#watch-btn').isVisible(), true);
    assert.equal(await f.page.locator('#resume-btn').isVisible(), false);
    assert.equal(f.calls.filter(call => call.path.endsWith('/files')).length, 0);
    assert.match(await f.page.locator('#sources-episodes .ep-btn').first().textContent(), /Свобода/);
    assert.match(await f.page.locator('#sources-episodes .ep-btn').first().textContent(), /49/);
    assert.match(await f.page.locator('#sources-episodes .ep-btn').first().textContent(), /2023/);
    await f.page.locator('#sources-episodes details.episode-description').first().locator('summary').click();
    assert.match(await f.page.locator('#sources-episodes details.episode-description').first().textContent(), /Джульетта/);
    assert.match(await f.page.locator('#sources-episodes .ep-btn').nth(2).textContent(), /3/);
    assert.doesNotMatch(await f.page.locator('#sources-episodes .ep-btn').nth(2).textContent(), /undefined|null|NaN/);
    await f.page.locator('#sources-season .chip').nth(1).click();
    await f.page.waitForFunction(() => document.querySelector('#sources-episodes')?.textContent.includes('Инженер'));
    assert.equal((await f.page.evaluate(() => window.__plays)).length, 0);
    assert.equal(f.calls.filter(call => call.path.endsWith('/files')).length, 0);
    const mark = f.page.locator('#season-personal').getByRole('button', { name: /сезон 2.*просмотренным|Сезон 2 просмотрен/ });
    await mark.click();
    assert.ok(f.calls.some(call => call.method === 'PUT' && call.path === '/api/personal/watched/' + series.id + '%3As2' && call.body.season === 2));
    assert.equal(await f.page.locator('input[aria-label="Номер просмотренного сезона"]').count(), 0);
    await f.page.locator('#sources-episodes .ep-btn').nth(1).click();
    await f.page.waitForFunction(() => window.__plays.length === 1);
    const play = await f.page.evaluate(() => window.__plays[0]);
    assert.equal(play.season, 2); assert.equal(play.ep, 2); assert.equal(play.file, 4);
    healthy(f);
  } finally { await f.context.close(); }
});

test('manual source choices survive a metadata rerender and play the currently selected series episode', async () => {
  const f = await fixture();
  try {
    await open(f, series);
    await capturePlayback(f.page);
    const settings = f.page.locator('#film-options');
    await settings.locator(':scope > summary').focus();
    await f.page.keyboard.press('Enter');
    assert.equal(await settings.evaluate(el => el.open), true);
    const box = f.page.locator('#manual-sources');
    await box.locator('summary').click();
    await box.locator('select').selectOption(otherMagnet);
    await f.page.evaluate(() => { FilmFeatures.sources(lastSourceItems, filmId); });
    assert.equal(await box.evaluate(el => el.open), true);
    assert.equal(await box.locator('select').inputValue(), otherMagnet);
    await f.page.locator('.profile-toggle').click();
    await f.page.locator('#lang-en').click();
    assert.equal(await box.evaluate(el => el.open), true);
    assert.equal(await box.locator('select').inputValue(), otherMagnet);
    assert.equal(await box.locator('summary').textContent(), 'Choose an option manually');
    await f.page.locator('#lang-ru').click();
    await f.page.keyboard.press('Escape');
    await box.getByRole('button', { name: 'Смотреть выбранный вариант', exact: true }).click();
    await f.page.waitForFunction(() => window.__plays.length === 1);
    const play = await f.page.evaluate(() => window.__plays[0]);
    assert.equal(play.magnet, otherMagnet); assert.equal(play.season, 2); assert.equal(play.ep, 2); assert.equal(play.file, 4);
    const playerSettings = f.page.locator('.playback-extras > details');
    await playerSettings.locator(':scope > summary').focus();
    await f.page.keyboard.press('Enter');
    assert.equal(await playerSettings.evaluate(el => el.open), true);
    await f.page.locator('#subtitle-size').selectOption('130');
    await f.page.getByText('Настройки сохранены.', { exact: true }).waitFor();
    assert.equal(f.personal.find(item => item.kind === 'preferences' && item.key === 'player').data.size, 130);
    healthy(f);
  } finally { await f.context.close(); }
});

test('trailer opens and closes accessibly, and recommendations scroll by button and touch-compatible overflow', async () => {
  const f = await fixture({ width: 390 });
  try {
    await open(f);
    const trailer = f.page.locator('#trailer-toggle');
    await trailer.click();
    assert.equal(await trailer.getAttribute('aria-pressed'), 'true');
    assert.equal(await f.page.locator('#trailer-player').isVisible(), true);
    assert.match(await f.page.locator('#trailer-player').getAttribute('src'), /^https:\/\/www\.youtube-nocookie\.com\/embed\//);
    await trailer.click();
    assert.equal(await trailer.getAttribute('aria-pressed'), 'false');
    assert.equal(await f.page.locator('#trailer-player').getAttribute('src'), null);
    const rail = f.page.locator('#film-explore .feature-carousel');
    const next = f.page.getByRole('button', { name: 'Следующие рекомендации', exact: true });
    const previous = f.page.getByRole('button', { name: 'Предыдущие рекомендации', exact: true });
    await rail.evaluate(el => { el.scrollLeft = 0; });
    await f.page.waitForFunction(() => document.querySelector('#film-explore .continue-nav-btn:first-child').disabled);
    assert.equal(await previous.isDisabled(), true);
    await next.click();
    await f.page.waitForFunction(() => document.querySelector('#film-explore .feature-carousel').scrollLeft > 100);
    assert.equal(await rail.evaluate(el => getComputedStyle(el).overflowX), 'auto');
    await rail.evaluate(el => { el.scrollLeft = el.scrollWidth; });
    await f.page.waitForFunction(() => document.querySelector('#film-explore .continue-nav-btn:last-child').disabled);
    assert.equal(await previous.isDisabled(), false);
    await rail.evaluate(el => { el.scrollLeft = 0; });
    await f.page.waitForFunction(() => document.querySelector('#film-explore .feature-carousel').scrollLeft === 0);
    await f.page.evaluate(() => {
      window.__documentArrows = 0;
      document.addEventListener('keydown', event => { if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') window.__documentArrows++; });
    });
    await rail.focus();
    await f.page.keyboard.press('ArrowRight');
    await f.page.waitForFunction(() => document.querySelector('#film-explore .feature-carousel').scrollLeft > 0);
    assert.equal(await f.page.evaluate(() => window.__documentArrows), 0, 'Carousel navigation must not reach the player seek handler');
    healthy(f);
  } finally { await f.context.close(); }
});

test('movie and active series fit desktop and phone in both themes with player before recommendations', async () => {
  for (const item of [movie, series]) for (const theme of ['light', 'dark']) for (const width of [1440, 1000, 390, 320]) {
    const f = await fixture({ theme, width });
    const kind = item === series ? 'series' : 'movie', screen = width === 1440 ? 'desktop' : width === 1000 ? 'compact-desktop' : width === 390 ? 'mobile' : 'small-mobile';
    try {
      await open(f, item);
      await capturePlayback(f.page);
      if (item === series) await f.page.locator('#resume-btn').click();
      assert.equal(await f.page.locator('html').getAttribute('data-theme'), theme);
      await noOverflow(f.page, `${kind}/${theme}/${width}`);
      if (item === series) {
        assert.equal(await f.page.locator('#film-explore h3').textContent(), 'Похожие сериалы');
        const layout = await f.page.evaluate(() => {
          const player = document.getElementById('player-wrap'), episodes = document.getElementById('sources-episodes-wrap'), related = document.getElementById('film-explore');
          const p = player.getBoundingClientRect(), e = episodes.getBoundingClientRect();
          return { playerBeforeRelated: !!(player.compareDocumentPosition(related) & Node.DOCUMENT_POSITION_FOLLOWING),
            left: p.x, right: p.right, episodesLeft: e.x, playerTop: p.top, episodesTop: e.top };
        });
        assert.equal(layout.playerBeforeRelated, true, 'Player appears before recommendations in reading order');
        if (width >= 980) assert.ok(layout.right <= layout.episodesLeft + 2, 'Desktop player sits beside the episodes');
        else assert.ok(layout.playerTop < layout.episodesTop, 'Phone player appears above the episodes');
        assert.match(await f.page.locator('#film-playing-title').textContent(), /2.*2/);
        const clippedControls = await f.page.evaluate(() => {
          const wrap = document.getElementById('player-wrap').getBoundingClientRect();
          return [...document.querySelectorAll('#player-controls button, #player-controls input')].filter(el => {
            const rect = el.getBoundingClientRect();
            return rect.width > 0 && (rect.x < wrap.x - 1 || rect.right > wrap.right + 1);
          }).map(el => el.id || el.className);
        });
        assert.deepEqual(clippedControls, [], 'Visible player controls stay inside the video column');
      }
      await f.page.locator('.profile-toggle').click();
      assert.equal(await f.page.locator('#theme-select').isVisible(), true);
      await noOverflow(f.page, `profile/${kind}/${theme}/${width}`);
      await f.page.keyboard.press('Escape');
      assert.equal(await f.page.locator('.profile-menu').evaluate(el => el.open), false);
      if (item === series) {
        assert.equal(await f.page.evaluate(() => window.__stopCalls), 0, 'Closing the profile must not stop playback');
        assert.equal(await f.page.evaluate(() => PP.playing()), true);
        assert.equal(await f.page.locator('#player-wrap').isVisible(), true);
      }
      await f.page.evaluate(() => scrollTo(0, 0));
      await f.page.screenshot({ path: path.join(output, `film-page-${kind}-${theme}-${screen}.png`), fullPage: true });
      healthy(f);
    } finally { await f.context.close(); }
  }
});

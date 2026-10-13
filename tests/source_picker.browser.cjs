// Offline Chromium integration for manual source selection. Real page markup,
// source dialog, error buttons, torrent file lookup and playback URL persistence
// run together; only playback itself is controlled to avoid remote media.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const web = path.join(__dirname, '../web');
const output = path.join(__dirname, '../output/design-preview/source-picker');
const source = (name, title, extra = {}) => ({ magnet: 'magnet:?xt=urn:btih:' + name.repeat(40), title,
  quality: '1080p', size: '3.3 GB', seeds: 22, ...extra });
const old = source('a', 'Текущий источник — Red Head Sound');
const good = source('b', 'Новый источник — LostFilm / Red Head Sound');
const wrong = source('c', 'Другой сезон — LostFilm');
const inventory = [old, wrong, good];
const shared = fs.readFileSync(path.join(web, 'shared.js'), 'utf8');
const navigation = shared.slice(shared.indexOf('function playbackPageUrl('), shared.indexOf('// ---- ТВ-режим'));
const timeFormat = shared.slice(shared.indexOf('function fmtTime('), shared.indexOf('function escapeHtml('));

function bootstrap(series) {
  return `
const fixture={starts:[],navigation:[],logs:[],follower:false,
  state:${JSON.stringify({ id: 'tt-fixture', magnet: old.magnet, file: 4,
    season: series ? 2 : 0, episode: series ? 3 : 0, voice: series ? 'LostFilm' : 'Red Head Sound',
    position: 1260.5, quality: 'source', active: true, paused: true })}};
window.fixture=fixture;
const VV={user:null},DEBUG=false,t=x=>x,dbg=x=>fixture.logs.push(x);
${timeFormat}
const isSeriesKind=kind=>kind==='series';
const Personal={get:()=>({data:{prepare:false}}),rank:items=>items,put:async()=>{}};
const PP={available:true,isFollower:()=>fixture.follower,playing:()=>true,ready:()=>false,
  state:()=>({...fixture.state}),release:()=>${JSON.stringify(old.title)},files:()=>[],selectSubtitle:()=>{},
  start:options=>{fixture.starts.push({...options});fixture.state={...fixture.state,...options,
    episode:options.ep,position:options.pos};window.dispatchEvent(new Event('playbackstart'));return true;}};
${navigation}
go=url=>fixture.navigation.push(url);
`;
}

async function scenario(browser, { pageType = 'watch', series = false, theme = 'dark', viewport, tv = false } = {}) {
  const context = await browser.newContext({ viewport: viewport || { width: 1280, height: 900 } });
  const page = await context.newPage();
  const errors = [], requests = [], waiters = [], pending = new Set();
  page.on('pageerror', error => errors.push(error.message));
  const request = kind => {
    const found = requests.find(item => item.kind === kind && !item.claimed);
    if (found) { found.claimed = true; return Promise.resolve(found); }
    return new Promise(resolve => waiters.push({ kind, resolve }));
  };
  await context.route('**/*', async route => {
    const url = new URL(route.request().url());
    if (/\/api\/films\/[^/]+\/(sources|files)$/.test(url.pathname)) {
      const item = { kind: path.basename(url.pathname), url: url.href,
        body: route.request().postDataJSON(), claimed: false,
        respond: async (data, status = 200) => {
          pending.delete(item);
          await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) }).catch(() => {});
        } };
      requests.push(item); pending.add(item);
      const waiter = waiters.findIndex(waiter => waiter.kind === item.kind);
      if (waiter >= 0) { item.claimed = true; waiters.splice(waiter, 1)[0].resolve(item); }
      return;
    }
    if (url.pathname === '/' + pageType + '.html') {
      let html = fs.readFileSync(path.join(web, pageType + '.html'), 'utf8')
        .replace(/<script\b[^>]*>[\s\S]*?<\/script>/g, '');
      // The real watch keyboard listener must not navigate when Escape dismisses
      // a dialog. Avoid unrelated authentication and page startup in this fixture.
      let escape = '';
      if (pageType === 'watch') {
        const watch = fs.readFileSync(path.join(web, 'watch.js'), 'utf8');
        const start = watch.indexOf("document.addEventListener('keydown'");
        const end = watch.indexOf('async function initWatchPage()', start);
        assert.ok(start >= 0 && end > start, 'real watch Escape handler is extracted');
        escape = "const filmLink=document.getElementById('film-link');\n" + watch.slice(start, end);
      }
      html = html.replace('</body>', `<script>${bootstrap(series)}</script><script src="/series.js"></script>
        <script>currentItem={id:'tt-fixture',tmdb_id:123,kind:${JSON.stringify(series ? 'series' : 'movie')}};
          document.documentElement.dataset.theme=${JSON.stringify(theme)};
          document.getElementById('player-wrap').hidden=false;
          document.getElementById('film-watch-layout')?.removeAttribute('hidden');
          document.getElementById('film-playback-column')?.removeAttribute('hidden');
        </script><script src="/playback-extras.js"></script><script src="/source-picker.js"></script>
        <script>${escape}</script><script src="/recovery.js"></script><script src="/tv.js"></script></body>`);
      return route.fulfill({ contentType: 'text/html; charset=utf-8', body: html });
    }
    if (url.pathname.startsWith('/api/')) return route.fulfill({ contentType: 'application/json', body: '{}' });
    const file = path.join(web, url.pathname);
    return route.fulfill(fs.existsSync(file) ? { contentType: url.pathname.endsWith('.js') ? 'text/javascript' : 'text/css',
      body: fs.readFileSync(file) } : { status: 404 });
  });
  await page.goto('http://fixture.local/' + pageType + '.html?id=tt-fixture' + (tv ? '&tv=1' : ''));
  await page.evaluate(() => {
    window.dispatchEvent(new Event('playbackstart'));
    window.dispatchEvent(new CustomEvent('playbackfailure', { detail: { reason: 'repeated_media_fragment',
      message: 'Выберите другой источник — позиция просмотра сохранится.' } }));
  });
  const options = page.locator('#source-picker-list .source-picker-option');
  const choose = title => options.filter({ hasText: title }).locator('input[type=radio]').check();
  const open = async selector => {
    await page.locator(selector).click();
    assert.equal(await page.locator('#source-picker').isVisible(), true, 'dialog opens before the sources response');
    assert.equal(await page.evaluate(() => fixture.starts.length), 0, 'opening a chooser never changes playback');
  };
  return { context, page, errors, requests, request, options, choose, open,
    close: () => context.close() };
}

(async () => {
  fs.mkdirSync(output, { recursive: true });
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH,
    args: ['--no-sandbox'] });
  try {
    {
      const s = await scenario(browser);
      try {
        const sources = s.request('sources');
        await s.open('#loading-source');
        assert.equal(await s.page.locator('#source-picker-play').isDisabled(), true);
        assert.equal(s.requests.filter(r => r.kind === 'files').length, 0);
        await (await sources).respond({ items: inventory, status: 'ready' });
        await s.page.waitForFunction(() => document.querySelector('#source-picker-list input[type=radio]'));
        assert.equal(await s.options.filter({ hasText: old.title }).locator('input[type=radio]').isChecked(), true,
          'the current source is identified without automatically choosing a replacement');
        assert.equal(await s.page.locator('#source-picker-play').isDisabled(), true,
          'opening a loaded source list still requires a different explicit selection');
        await s.choose(good.title);
        assert.equal(await s.page.evaluate(() => fixture.starts.length), 0, 'choosing a row does not launch it');
        const files = s.request('files');
        await s.page.locator('#source-picker-play').click();
        const fileRequest = await files;
        assert.equal(fileRequest.body.magnet, good.magnet);
        assert.equal(fileRequest.body.tmdb, '123', 'real file lookup retains canonical series metadata');
        await s.page.evaluate(() => { fixture.state.position = 1271.875; });
        await fileRequest.respond({ files: [{ index: 2, name: 'sample.mkv', length: 100 },
          { index: 7, name: 'movie.mkv', length: 9000 }] });
        await s.page.locator('#source-picker-file').waitFor({ state: 'visible' });
        assert.equal(await s.page.evaluate(() => fixture.starts.length), 0,
          'a source with multiple videos requires the viewer to choose the movie file');
        assert.equal(await s.page.locator('#source-picker-play').isDisabled(), true);
        await s.page.locator('#source-picker-file').selectOption('7');
        await s.page.evaluate(() => { fixture.state.position = 1279.25; });
        await s.page.locator('#source-picker-play').click();
        await s.page.waitForFunction(() => fixture.starts.length === 1);
        const result = await s.page.evaluate(() => ({ start: fixture.starts[0], params: [...playbackPageParams()],
          navigation: fixture.navigation }));
        assert.equal(result.start.magnet, good.magnet);
        assert.equal(result.start.file, 7, 'a movie uses the explicitly selected video file');
        assert.equal(result.start.pos, 1279.25, 'position is sampled at confirmation after file preparation');
        assert.equal(result.start.voice, 'Red Head Sound');
        assert.equal(result.start.quality, 'source');
        const params = new URLSearchParams(result.params);
        assert.equal(params.get('magnet'), good.magnet);
        assert.equal(params.get('pos'), '1279.25');
        assert.deepEqual(result.navigation, []);
        assert.equal(await s.page.locator('#source-picker').isVisible(), false);
        assert.deepEqual(s.errors, []);
        console.log('PASS: overlay button opens immediately; only explicit confirmation switches a movie source and preserves the latest position.');
      } finally { await s.close(); }
    }
    {
      const s = await scenario(browser, { pageType: 'film', series: true });
      try {
        const sources = s.request('sources');
        await s.open('.recovery-offer button');
        await (await sources).respond({ items: inventory, status: 'searching' });
        await s.choose(wrong.title);
        assert.match(await s.page.locator('#source-picker-status').textContent(), /поиск|ищ|обнов/i,
          'cached source rows remain available while the tracker search is in progress');
        assert.equal(s.requests.filter(r => r.kind === 'files').length, 0);
        const wrongFiles = s.request('files');
        await s.page.locator('#source-picker-play').click();
        await (await wrongFiles).respond({ files: [{ index: 9, season: 1, episode: 3 },
          { index: 10, season: 2, episode: 4 }] });
        await s.page.waitForFunction(() => !document.getElementById('source-picker-play').disabled);
        assert.equal(await s.page.evaluate(() => fixture.starts.length), 0, 'an unavailable episode never substitutes another episode');
        assert.equal(await s.page.locator('#source-picker').isVisible(), true);
        assert.match(await s.page.locator('#source-picker-status').textContent(), /сер|сез|S2|E3/i);
        await s.choose(good.title);
        const rightFiles = s.request('files');
        await s.page.locator('#source-picker-play').click();
        await s.page.evaluate(() => { fixture.state.position = 1324.25; });
        await (await rightFiles).respond({ files: [{ index: 88, season: 2, episode: 3, name: 'S02E03.mkv' },
          { index: 89, season: 2, episode: 4, name: 'S02E04.mkv', length: 999999 }] });
        await s.page.waitForFunction(() => fixture.starts.length === 1);
        const start = await s.page.evaluate(() => fixture.starts[0]);
        assert.equal(start.file, 88);
        assert.equal(start.season, 2); assert.equal(start.ep, 3);
        assert.equal(start.voice, 'LostFilm'); assert.equal(start.pos, 1324.25);
        assert.deepEqual(s.errors, []);
        console.log('PASS: recovery button shows cached search results; a series retains its exact episode, voice and position and rejects a mismatched source.');
      } finally { await s.close(); }
    }
    for (const cancel of ['close', 'Escape']) {
      const s = await scenario(browser);
      try {
        const sources = s.request('sources');
        await s.open('#loading-source');
        const pending = await sources;
        if (cancel === 'Escape') await s.page.keyboard.press('Escape');
        else await s.page.locator('#source-picker-close').click();
        assert.equal(await s.page.locator('#source-picker').isVisible(), false);
        await pending.respond({ items: inventory, status: 'ready' });
        await s.page.evaluate(() => new Promise(resolve => setTimeout(resolve, 50)));
        assert.equal(await s.page.evaluate(() => fixture.starts.length), 0);
        assert.deepEqual(await s.page.evaluate(() => fixture.navigation), [], 'closing a dialog must not leave the watch page');
        assert.equal(await s.page.locator('#source-picker').isVisible(), false, 'late source results must not reopen a cancelled dialog');
        if (cancel === 'Escape') {
          await s.page.keyboard.press('Escape');
          assert.deepEqual(await s.page.evaluate(() => fixture.navigation), ['/'], 'normal watch Escape navigation remains available after closing');
        }
        assert.deepEqual(s.errors, []);
        console.log('PASS: ' + cancel + ' cancels the source search and ignores late results without navigating away.');
      } finally { await s.close(); }
    }
    {
      const s = await scenario(browser);
      try {
        const first = s.request('sources');
        await s.open('#loading-source');
        const oldRequest = await first;
        const second = s.request('sources');
        await s.page.evaluate(() => {
          document.getElementById('source-picker').close();
          PlaybackSources.open();
        });
        const newRequest = await second;
        await newRequest.respond({ items: [old, good], status: 'ready' });
        await s.choose(good.title);
        await oldRequest.respond({ items: [old, wrong], status: 'ready' });
        assert.equal(await s.page.locator('#source-picker').isVisible(), true,
          'the queued close event from an old dialog must not cancel a newly opened chooser');
        assert.equal(await s.options.filter({ hasText: wrong.title }).count(), 0,
          'a superseded response cannot replace the new source list');
        const files = s.request('files');
        await s.page.locator('#source-picker-play').click();
        await (await files).respond({ files: [{ index: 7, name: 'movie.mkv' }] });
        await s.page.waitForFunction(() => fixture.starts.length === 1);
        assert.equal(await s.page.evaluate(() => fixture.starts[0].magnet), good.magnet);
        assert.deepEqual(s.errors, []);
        console.log('PASS: rapid native close and reopen keeps the new chooser usable and rejects old results.');
      } finally { await s.close(); }
    }
    {
      const s = await scenario(browser, { pageType: 'film', series: true });
      try {
        const sources = s.request('sources');
        await s.open('.recovery-offer button');
        await (await sources).respond({ items: inventory, status: 'ready' });
        await s.choose(good.title);
        const files = s.request('files');
        await s.page.locator('#source-picker-play').click();
        const pending = await files;
        await s.page.locator('#source-picker-close').click();
        await pending.respond({ files: [{ index: 88, season: 2, episode: 3 }] });
        await s.page.evaluate(() => new Promise(resolve => setTimeout(resolve, 50)));
        assert.equal(await s.page.evaluate(() => fixture.starts.length), 0, 'cancelled file preparation cannot start playback');
        assert.equal(await s.page.locator('#source-picker').isVisible(), false);
        assert.deepEqual(await s.page.evaluate(() => fixture.navigation), []);
        assert.deepEqual(s.errors, []);
        console.log('PASS: cancellation during torrent file preparation prevents a delayed source switch.');
      } finally { await s.close(); }
    }
    {
      const s = await scenario(browser);
      try {
        await s.page.evaluate(() => {
          fixture.follower = true; window.dispatchEvent(new Event('playbackrole'));
          window.dispatchEvent(new Event('requestsourcechange')); PlaybackSources.open();
        });
        assert.equal(await s.page.locator('#source-picker').isVisible(), false);
        assert.equal(s.requests.length, 0, 'a room guest cannot search or replace the host source');
        await s.page.evaluate(() => { fixture.follower = false; window.dispatchEvent(new Event('playbackrole')); });
        const sources = s.request('sources');
        await s.open('#loading-source');
        const pending = await sources;
        await s.page.evaluate(() => { fixture.follower = true; window.dispatchEvent(new Event('playbackrole')); });
        await pending.respond({ items: inventory, status: 'ready' });
        await s.page.evaluate(() => new Promise(resolve => setTimeout(resolve, 50)));
        assert.equal(await s.page.locator('#source-picker').isVisible(), false);
        assert.equal(await s.page.evaluate(() => fixture.starts.length), 0);
        assert.deepEqual(s.errors, []);
        console.log('PASS: room guests cannot replace a source; entering the guest role cancels an open chooser.');
      } finally { await s.close(); }
    }
    {
      const s = await scenario(browser);
      try {
        const sources = s.request('sources');
        await s.open('#loading-source');
        await (await sources).respond({ error: 'unavailable' }, 503);
        await s.page.waitForFunction(() => !document.getElementById('source-picker-retry').disabled);
        assert.match(await s.page.locator('#source-picker-status').textContent(), /не удалось|недоступ|ошиб/i);
        assert.equal(await s.page.evaluate(() => fixture.starts.length), 0);
        const retry = s.request('sources');
        await s.page.locator('#source-picker-retry').click();
        await (await retry).respond({ items: inventory, status: 'ready' });
        await s.choose(good.title);
        assert.equal(await s.page.locator('#source-picker-play').isDisabled(), false);
        assert.deepEqual(s.errors, []);
        console.log('PASS: a failed source lookup stays in the chooser with a usable retry.');
      } finally { await s.close(); }
    }
    {
      const s = await scenario(browser, { tv: true });
      try {
        const sources = s.request('sources');
        await s.open('#loading-source');
        await (await sources).respond({ items: inventory, status: 'ready' });
        const radio = s.options.filter({ hasText: good.title }).locator('input[type=radio]');
        await s.options.filter({ hasText: old.title }).locator('input[type=radio]').focus();
        await s.page.keyboard.press('ArrowDown');
        await s.page.keyboard.press('ArrowDown');
        assert.equal(await s.page.evaluate(() => document.activeElement.value), 'urn:btih:' + 'b'.repeat(40),
          'TV arrows navigate among source options without selecting an alternative automatically');
        assert.equal(await radio.isChecked(), false);
        await s.page.keyboard.press('Enter');
        assert.equal(await radio.isChecked(), true);
        await s.page.keyboard.press('ArrowDown');
        assert.equal(await s.page.evaluate(() => document.activeElement.id), 'source-picker-retry',
          'TV down leaves the radio group for the footer');
        await s.page.keyboard.press('ArrowRight');
        assert.equal(await s.page.evaluate(() => document.activeElement.id), 'source-picker-play',
          'a remote control can reach confirmation without a Tab key');
        assert.equal(await s.page.evaluate(() => fixture.starts.length), 0);
        const files = s.request('files');
        await s.page.keyboard.press('Enter');
        await (await files).respond({ files: [{ index: 7, name: 'movie.mkv' }] });
        await s.page.waitForFunction(() => fixture.starts.length === 1);
        assert.equal(await s.page.evaluate(() => fixture.starts[0].magnet), good.magnet);
        await s.page.evaluate(() => PlaybackSources.open());
        assert.equal(await s.page.evaluate(() => window.__vvBack()), 'consumed');
        assert.equal(await s.page.locator('#source-picker').isVisible(), false);
        assert.deepEqual(await s.page.evaluate(() => fixture.navigation), [], 'TV Back closes the chooser without leaving playback');
        assert.deepEqual(s.errors, []);
        console.log('PASS: TV remote arrows move out of source radios and Enter confirms the selected source.');
      } finally { await s.close(); }
    }
    for (const config of [{ theme: 'dark', viewport: { width: 1280, height: 900 } },
      { theme: 'light', viewport: { width: 390, height: 844 } }]) {
      const s = await scenario(browser, config);
      try {
        const sources = s.request('sources');
        await s.open('#loading-source');
        const long = source('d', 'Очень длинное название фильма: Легенда об Аанге — Последний маг воздуха, WEB-DL 1080p | Дублированный перевод | Red Head Sound / HDRezka Studio');
        await (await sources).respond({ items: [old, good, long, ...Array.from({ length: 10 }, (_, i) =>
          source(String(i + 1), 'Другой источник ' + (i + 1) + ' — WEB-DL 1080p | LostFilm'))], status: 'ready' });
        await s.choose(long.title);
        const bounds = await s.page.locator('#source-picker').evaluate(dialog => {
          const rect = dialog.getBoundingClientRect();
          return { x: rect.x, y: rect.y, right: rect.right, bottom: rect.bottom,
            width: innerWidth, height: innerHeight, scrollWidth: dialog.scrollWidth, clientWidth: dialog.clientWidth,
            background: getComputedStyle(dialog).backgroundColor };
        });
        assert.ok(bounds.x >= 0 && bounds.right <= bounds.width + 1, 'dialog fits the viewport horizontally');
        assert.ok(bounds.y >= 0 && bounds.bottom <= bounds.height + 1, 'dialog fits the viewport vertically');
        assert.ok(bounds.scrollWidth <= bounds.clientWidth + 1, 'long source titles do not create horizontal overflow');
        assert.notEqual(bounds.background, 'rgba(0, 0, 0, 0)', 'dialog has a readable opaque themed surface');
        await s.page.screenshot({ path: path.join(output, 'source-picker-' + config.theme + '.png') });
        assert.deepEqual(s.errors, []);
        console.log('PASS: source chooser fits ' + config.viewport.width + 'px ' + config.theme + ' layout with long titles and a scrolling source list.');
      } finally { await s.close(); }
    }
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });

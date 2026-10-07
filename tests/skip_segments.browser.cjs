// Optional browser integration suite. Run with Playwright available in NODE_PATH:
// node --test --test-isolation=none tests/skip_segments.browser.cjs
// PLAYWRIGHT_MODULE and CHROMIUM_PATH may select an installed browser runtime.
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');

const root = path.join(__dirname, '..');
const magnet = 'magnet:?xt=urn:btih:' + 'a'.repeat(40);
const filmID = 'tt1234567';
let browser;

before(async () => {
  const candidates = [process.env.CHROMIUM_PATH, process.env.CHROME_BIN, chromium.executablePath(),
    'C:/Program Files/Google/Chrome/Application/chrome.exe',
    'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'];
  const executablePath = candidates.find(value => value && fs.existsSync(value));
  browser = await chromium.launch({ executablePath, headless: true });
});
after(async () => { if (browser) await browser.close(); });

async function fixture(options = {}) {
  const context = await browser.newContext({ viewport: options.viewport || { width: 1280, height: 900 } });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const html = fs.readFileSync(path.join(root, 'web/watch.html'), 'utf8')
    .replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi, '');
  await page.route('**/*', route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/watch.html') return route.fulfill({ contentType: 'text/html', body: html });
    const filename = path.join(root, 'web', path.basename(url.pathname));
    if (url.pathname.endsWith('.css') && fs.existsSync(filename)) {
      return route.fulfill({ contentType: 'text/css', body: fs.readFileSync(filename, 'utf8') });
    }
    return route.fulfill({ status: 404, body: '' });
  });
  await page.goto('http://video-viewer.test/watch.html' + (options.tv ? '?tv=1' : ''));
  await page.evaluate(({ options, magnet, filmID }) => {
    const video = document.getElementById('player');
    const media = { time: 0, buffered: 0, paused: true, ready: 0 };
    window.fixture = {
      requests: [], launches: [], personal: new Map(), media, host: null, navigations: 0,
      files: options.files || [{ index: 0, season: 1, episode: 1, name: 'Episode 1' }],
      chapters: options.chapters || [], external: options.external || [], duration: options.duration || 1000,
    };
    const emit = name => video.dispatchEvent(new Event(name));
    Object.defineProperties(video, {
      currentTime: { get: () => media.time, set: value => {
        media.time = value; queueMicrotask(() => { emit('timeupdate'); emit('seeked'); });
      } },
      duration: { get: () => Infinity }, paused: { get: () => media.paused },
      readyState: { get: () => media.ready },
      buffered: { get: () => ({ length: media.buffered > 0 ? 1 : 0, end: () => media.buffered }) },
    });
    video.load = () => { media.time = 0; media.buffered = 0; media.ready = 0; };
    video.pause = () => { if (!media.paused) { media.paused = true; emit('pause'); } };
    video.play = () => {
      const wasPaused = media.paused; media.paused = false; media.ready = 4;
      if (wasPaused) emit('play'); emit('playing'); return Promise.resolve();
    };
    window.fetch = async (input, request = {}) => {
      const url = new URL(input, location.href), pathname = url.pathname;
      const body = request.body ? JSON.parse(request.body) : undefined;
      fixture.requests.push({ path: pathname, search: url.search, method: request.method || 'GET', body });
      let data = {};
      if (pathname.endsWith('/tracks')) {
        const file = Number(url.searchParams.get('file') || 0);
        data = { duration: fixture.duration, items: [{ ordinal: 0, title: 'Original', language: 'eng' }],
          subtitles: [], codec: 'h264', height: 1080, segments: file === 0 ? fixture.chapters : [],
          media_key: 'segments.' + 'a'.repeat(40) + '.' + file };
      } else if (pathname.endsWith('/segments')) {
        data = { status: fixture.external.length ? 'ready' : 'not_found', segments: fixture.external };
      } else if (pathname.startsWith('/api/rooms/') && !pathname.endsWith('/leave')) {
        data = { state: fixture.host, server_time: Date.now(), updated: Date.now(), participants: [] };
      }
      return { ok: true, status: 200, json: async () => data, text: async () => '' };
    };
    window.Hls = class {
      static Events = { MANIFEST_PARSED: 'manifest', ERROR: 'error', SUBTITLE_TRACKS_UPDATED: 'subtitles',
        LEVEL_SWITCHED: 'level', FRAG_BUFFERED: 'frag' };
      static isSupported() { return true; }
      constructor() { this.subtitleTracks = []; this.levels = []; this.dead = false; }
      loadSource(source) { fixture.launches.push(source); }
      attachMedia() { media.ready = 4; }
      on(event, handler) {
        if (event === 'manifest') queueMicrotask(() => { if (!this.dead) handler(); });
      }
      destroy() { this.dead = true; }
    };
    window.VV = { user: { id: 'test-user' } };
    window.Personal = {
      get: (kind, key) => fixture.personal.has(key) ? { data: fixture.personal.get(key) } : null,
      put: async (kind, key, data) => {
        fixture.personal.set(key, data); window.dispatchEvent(new Event('personalchange'));
      },
      preferences: () => ({}), rank: items => items,
    };
    window.currentItem = { id: filmID, kind: 'tv', tmdb_id: '101' };
    window.DEBUG = false; window.dbg = () => {}; window.t = key => key;
    window.fmtTime = value => String(Math.round(value || 0));
    window.episodeHistoryEntry = () => null; window.rememberWatchProgress = () => {};
    window.seasonEpisodeCount = () => fixture.files.length; window.canonicalByNumber = () => null;
    window.isSeriesKind = kind => kind === 'tv';
    window.fetchFiles = async () => fixture.files;
    window.titleVoices = () => []; window.matchVoiceOrdinal = () => 0;
    window.onAuth = () => {}; window.onLang = () => {};
    window.PlaybackExtras = { next: () => null, loading: () => {} };
    const room = document.createElement('div');
    room.hidden = true;
    room.innerHTML = ['room-create','room-leave','room-copy'].map(id => '<button id="'+id+'"></button>').join('')
      + ['room-invite','room-participants','room-participant-count','room-status'].map(id => '<div id="'+id+'"></div>').join('')
      + '<input id="room-link"><ul id="room-participant-list"></ul>';
    document.body.append(room);
    document.addEventListener('keydown', event => { if (event.key === 'Escape') fixture.navigations++; });
  }, { options, magnet, filmID });
  for (const name of ['player.js', 'watch-room.js', 'skip-segments.js', 'tv.js']) {
    await page.addScriptTag({ path: path.join(root, 'web', name) });
  }
  await page.evaluate(({ magnet, filmID, position }) => {
    PP.init({}); PP.start({ id: filmID, magnet, file: 0, season: 1, ep: 1, pos: position || 0 });
  }, { magnet, filmID, position: options.position });
  await page.waitForFunction(() => PP.ready() && fixture.launches.length > 0 && !fixture.media.paused);
  return { page, context, errors };
}

async function position(page, absolute, buffered = absolute + 5) {
  await page.evaluate(({ absolute, buffered }) => {
    const source = new URL(fixture.launches.at(-1), location.href);
    const start = Number(source.searchParams.get('start') || 0);
    fixture.media.time = absolute - start;
    fixture.media.buffered = buffered - start;
    document.getElementById('player').dispatchEvent(new Event('timeupdate'));
  }, { absolute, buffered });
}

async function mode(page, type, value) {
  await page.evaluate(({ type, value }) => {
    const select = document.getElementById('skip-mode-' + type);
    select.value = value; select.dispatchEvent(new Event('change'));
  }, { type, value });
}

async function capturePanel(page, name) {
  if (!process.env.SKIP_SCREENSHOT_DIR) return;
  await page.screenshot({ path: path.join(process.env.SKIP_SCREENSHOT_DIR, name + '.png'), fullPage: true });
  console.log(name + ' layout: ' + JSON.stringify(await page.evaluate(() => {
    const panel = document.getElementById('skip-segments-panel');
    const bounds = panel.getBoundingClientRect();
    return { viewport: innerWidth, width: Math.round(bounds.width), height: Math.round(bounds.height),
      horizontalOverflow: panel.scrollWidth > panel.clientWidth, verticalScroll: panel.scrollHeight > panel.clientHeight };
  })));
}

test('real player skips and restores absolute time after an HLS restart', async () => {
  const f = await fixture({ position: 100, chapters: [{ type: 'intro', start: 130, end: 170, auto_skip: true }] });
  try {
    await position(f.page, 140, 145);
    if (process.env.SKIP_SCREENSHOT_DIR) {
      await f.page.locator('#skip-segments-open').click();
      await capturePanel(f.page, 'skip-settings-desktop');
      await f.page.locator('#skip-segments-close').click();
    }
    await f.page.locator('#skip-segment').click();
    await f.page.waitForFunction(() => PP.state().position === 170 && !fixture.media.paused);
    assert.equal(await f.page.evaluate(() => new URL(fixture.launches.at(-1), location.href).searchParams.get('start')), '170');
    await f.page.locator('#skip-segment-undo').click();
    await f.page.waitForFunction(() => PP.state().position === 140 && !fixture.media.paused);
    assert.equal(await f.page.evaluate(() => new URL(fixture.launches.at(-1), location.href).searchParams.get('start')), '140');
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('automatic trusted skip fires once and undo permits watching the same range', async () => {
  const f = await fixture({ chapters: [{ type: 'intro', start: 10, end: 40, auto_skip: true }] });
  try {
    await mode(f.page, 'intro', 'auto'); await position(f.page, 15, 20);
    await f.page.waitForFunction(() => PP.state().position === 40 && !fixture.media.paused);
    await f.page.locator('#skip-segment-undo').click();
    await f.page.waitForFunction(() => PP.state().position === 15 && !fixture.media.paused);
    const launches = await f.page.evaluate(() => fixture.launches.length);
    await position(f.page, 16, 20);
    assert.equal(await f.page.evaluate(() => PP.state().position), 16);
    assert.equal(await f.page.evaluate(() => fixture.launches.length), launches);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('external suggestions require confirmation even when automatic mode is selected', async () => {
  const f = await fixture({ external: [{ type: 'intro', start: 10, end: 40, auto_skip: true }] });
  try {
    await f.page.waitForFunction(() => document.getElementById('skip-segments-source-status').textContent.includes('Найдена'));
    await mode(f.page, 'intro', 'auto'); await position(f.page, 15, 20);
    assert.equal(await f.page.evaluate(() => PP.state().position), 15);
    await f.page.locator('#skip-segments-open').click();
    await f.page.locator('#skip-segments-list button').filter({ hasText: 'Подтвердить' }).click();
    await f.page.locator('#skip-segments-close').click();
    await position(f.page, 16, 20);
    await f.page.waitForFunction(() => PP.state().position === 40 && !fixture.media.paused);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('credit blocks preserve the intervening scene and terminal skip saves completion before advancing', async () => {
  const f = await fixture({ files: [{ index: 0, season: 1, episode: 1 }, { index: 1, season: 1, episode: 2 }],
    chapters: [{ type: 'credits', start: 900, end: 940, auto_skip: true }, { type: 'credits', start: 970, end: 1000, auto_skip: true }] });
  try {
    await position(f.page, 920, 925); await f.page.locator('#skip-segment').click();
    await f.page.waitForFunction(() => PP.state().position === 940 && !fixture.media.paused);
    assert.equal(await f.page.evaluate(() => PP.state().file), 0);
    await position(f.page, 980, 985);
    assert.equal(await f.page.locator('#skip-segment').textContent(), 'Следующая серия');
    await f.page.locator('#skip-segment').click();
    await f.page.waitForFunction(() => PP.state().file === 1 && PP.ready() && !fixture.media.paused);
    const result = await f.page.evaluate(() => ({
      completed: fixture.requests.some(r => r.path === '/api/history/progress' && r.body.file === 0 && r.body.position === 1000),
      eofLaunch: fixture.launches.some(url => new URL(url, location.href).searchParams.get('start') === '1000'),
      position: PP.state().position,
    }));
    assert.deepEqual(result, { completed: true, eofLaunch: false, position: 0 });
    assert.equal(await f.page.locator('#skip-segment-undo').isVisible(), false);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('room followers cannot skip while host synchronization can still seek', async () => {
  const f = await fixture({ chapters: [{ type: 'intro', start: 130, end: 170, auto_skip: true }] });
  try {
    await mode(f.page, 'intro', 'auto');
    await position(f.page, 0, 180);
    await f.page.evaluate(async ({ magnet, filmID }) => {
      fixture.host = { id: filmID, magnet, file: 0, season: 1, episode: 1, position: 140, paused: false };
      await WatchRoom.join('b'.repeat(32));
    }, { magnet, filmID });
    await position(f.page, 140, 180);
    assert.equal(await f.page.locator('#skip-segment').isDisabled(), true);
    const skipped = await f.page.evaluate(() => PP.skipSegment(170));
    assert.equal(skipped, false);
    assert.equal(await f.page.evaluate(() => PP.state().position), 140);
    await f.page.evaluate(() => PP.seek(145));
    await f.page.waitForFunction(() => PP.state().position === 145);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('mobile manual editor accepts timestamps and Escape closes it without leaving playback', async () => {
  const f = await fixture({ viewport: { width: 390, height: 844 } });
  try {
    await f.page.locator('#skip-segments-open').click();
    await capturePanel(f.page, 'skip-settings-mobile');
    assert.ok(await f.page.locator('#skip-segments-panel').evaluate(node => node.getBoundingClientRect().height > 400),
      'Mobile settings must use the phone viewport rather than a small video-height scroller');
    await f.page.locator('#skip-edit-type').selectOption('intro');
    await f.page.locator('#skip-edit-start').fill('0:10');
    await f.page.locator('#skip-edit-end').fill('0:30');
    await f.page.locator('#skip-edit-save').click();
    await f.page.waitForFunction(() => fixture.personal.has('segments.' + 'a'.repeat(40) + '.0'));
    await f.page.locator('#skip-edit-start').focus(); await f.page.keyboard.press('Escape');
    assert.equal(await f.page.locator('#skip-segments-panel').isVisible(), false);
    assert.equal(await f.page.evaluate(() => fixture.navigations), 0);
    assert.deepEqual(await f.page.evaluate(() => fixture.personal.get('segments.' + 'a'.repeat(40) + '.0').segments.map(s => ({ type:s.type,start:s.start,end:s.end,auto_skip:s.auto_skip }))),
      [{ type: 'intro', start: 10, end: 30, auto_skip: true }]);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

test('TV arrows move focus from skip toolbar without seeking the video', async () => {
  const f = await fixture({ tv: true, chapters: [{ type: 'intro', start: 10, end: 40, auto_skip: true }] });
  try {
    await position(f.page, 15, 30); await f.page.locator('#skip-segment').focus();
    await f.page.keyboard.press('ArrowLeft');
    assert.equal(await f.page.evaluate(() => PP.state().position), 15);
    await f.page.locator('#skip-segments-open').click();
    await f.page.evaluate(() => VideoViewerTV.handleBack());
    assert.equal(await f.page.locator('#skip-segments-panel').isVisible(), false);
    assert.equal(await f.page.evaluate(() => PP.state().position), 15);
    assert.deepEqual(f.errors, []);
  } finally { await f.context.close(); }
});

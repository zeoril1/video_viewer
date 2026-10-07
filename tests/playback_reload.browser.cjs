// Reload lifecycle regression with the real page/player scripts and deterministic
// audio metadata/HLS fixtures; no torrent or external service is contacted.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const assert = require('node:assert/strict');
const web = path.join(__dirname, '../web');
const old = 'magnet:?xt=urn:btih:' + 'a'.repeat(40);
const good = 'magnet:?xt=urn:btih:' + 'b'.repeat(40);
const wrong = 'magnet:?xt=urn:btih:' + 'c'.repeat(40);
let trackDelay = 20, filmDelay = 20, failSources = false;
const history = new Map();
const bootstrap = `
window.hlsRequests = [];
const media = document.getElementById('player');
let fixturePaused = true;
Object.defineProperty(media, 'paused', {get: () => fixturePaused});
Object.defineProperty(media, 'readyState', {get: () => 4});
media.load = () => {media.currentTime = 0;};
media.play = async () => {
  if (!fixturePaused) return;
  fixturePaused = false;
  media.dispatchEvent(new Event('play'));
  media.dispatchEvent(new Event('playing'));
};
media.pause = () => {
  if (fixturePaused) return;
  fixturePaused = true;
  media.dispatchEvent(new Event('pause'));
};
window.Hls = class {
  static Events = {MANIFEST_PARSED:'manifest', SUBTITLE_TRACKS_UPDATED:'subs', LEVEL_SWITCHED:'level', FRAG_BUFFERED:'frag', ERROR:'error'};
  static isSupported() {return true;}
  constructor() {this.handlers = {}; this.subtitleTracks = [];}
  on(name, fn) {this.handlers[name] = fn;}
  loadSource(src) {window.hlsRequests.push(src);}
  attachMedia() {setTimeout(() => {if (!this.dead) this.handlers.manifest?.();}, 20);}
  destroy() {this.dead = true;}
};
const WatchRoom = {join: async id => {window.joinedRoom = id;}};
`;
const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://local');
  const json = (data, status = 200) => {
    res.writeHead(status, {'Content-Type': 'application/json'});
    res.end(JSON.stringify(data));
  };
  if (u.pathname === '/api/auth/me') return json({user: {id: 1, username: 'Fixture'}});
  if (u.pathname === '/api/personal') return json({items: []});
  if (u.pathname === '/api/history/progress') {
    let body = '';
    req.on('data', chunk => {body += chunk;});
    return req.on('end', () => {
      const item = JSON.parse(body);
      history.set(item.film_id + ':' + item.season + ':' + item.episode, item);
      json({});
    });
  }
  if (u.pathname === '/api/history') {
    const id = u.searchParams.get('film_id');
    return json({items: [...history.values()].filter(item => !id || item.film_id === id).reverse()});
  }
  if (u.pathname.endsWith('/files')) {
    let body = '';
    req.on('data', chunk => {body += chunk;});
    return req.on('end', () => {
      const magnet = JSON.parse(body).magnet;
      const files = magnet === wrong ? [{index: 9, season: 1, episode: 3}]
        : magnet === good ? [{index: 8, season: 1, episode: 2}]
          : [{index: 0, season: 1, episode: 1}, {index: 1, season: 1, episode: 2}];
      json({files});
    });
  }
  if (u.pathname.endsWith('/tracks')) return setTimeout(() => json({
    duration: 900, codec: 'h264', height: 720,
    items: [{ordinal: 0, language: 'rus', title: 'LostFilm'}], subtitles: [],
  }), trackDelay);
  if (u.pathname.endsWith('/sources')) return json({
    status: 'ready', seasons: [{season: 1, episodes: 3}],
    items: [{magnet: old, title: 'Season 1 LostFilm', season: 1, seeds: 1},
      {magnet: wrong, title: 'Season 1 LostFilm', season: 1, seeds: 1},
      {magnet: good, title: 'Season 1 LostFilm', season: 1, seeds: 1}],
  }, failSources ? 503 : 200);
  if (/^\/api\/films\/tt[12]$/.test(u.pathname)) return setTimeout(() => json({
    id: u.pathname.endsWith('tt1') ? 'tt1' : 'tt2',
    imdb_id: u.pathname.endsWith('tt1') ? 'tt1' : 'tt2',
    title: 'Reload fixture', kind: u.pathname.endsWith('tt1') ? 'tvSeries' : 'feature',
    tmdb_id: '123', seasons: 1,
  }), filmDelay);
  if (u.pathname === '/api/stream/keep' || u.pathname.includes('/hls/stop')) {res.writeHead(204); return res.end();}
  if (u.pathname.startsWith('/api/')) return json({items: [], seasons: [], episodes: []});
  if (u.pathname === '/film.html' || u.pathname === '/watch.html') {
    const keep = /^(shared|personal|series|series-catalog|player|film|watch|recovery)\.js(?:\?|$)/;
    let html = fs.readFileSync(path.join(web, u.pathname), 'utf8');
    html = html.replace(/<script\b[^>]*src="([^"]+)"[^>]*>[\s\S]*?<\/script>/g,
      (tag, src) => keep.test(src) ? tag : '');
    html = html.replace(/<script/, `<script>${bootstrap}</script><script`);
    res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8'});
    return res.end(html);
  }
  const file = path.join(web, u.pathname);
  if (!file.startsWith(web) || !fs.existsSync(file)) {res.writeHead(404); return res.end();}
  res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'image/png');
  res.end(fs.readFileSync(file));
});
(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({headless: true, executablePath: process.env.CHROMIUM_PATH, args: ['--no-sandbox']});
  const errors = [];
  try {
    const page = await browser.newPage();
    page.on('pageerror', error => {errors.push(error.message); console.error('Page error:', error.message);});
    const base = 'http://127.0.0.1:' + server.address().port;
    await page.addInitScript(() => {
      if (!sessionStorage.getItem('fixture-initialized')) {
        sessionStorage.setItem('fixture-initialized', '1');
        sessionStorage.setItem('vv:item:tt1', JSON.stringify({id: 'tt1', imdb_id: 'tt1', kind: 'tvSeries', title: 'Fixture'}));
      }
    });
    trackDelay = 800;
    await page.goto(base + '/film.html?id=tt1&magnet=' + encodeURIComponent(old) + '&file=0&season=1&ep=1&autoplay=1');
    await page.waitForFunction(() => PP.state().active && !PP.state().playing).catch(async error => {
      console.error(await page.evaluate(() => ({state: PP.state(), sources: lastSourceItems, item: currentItem, ready: seriesUiReady, params: filmParams.toString(), errors: document.getElementById('film-note').textContent})));
      throw error;
    });
    assert.equal(await page.evaluate(() => new URLSearchParams(window.history.state.playback).get('magnet')), old);
    await page.reload();
    await page.waitForFunction(() => PP.state().playing && !document.querySelector('#player').paused);
    assert.equal(await page.evaluate(() => PP.fileIndex()), 0);
    assert.equal(await page.evaluate(() => PP.episode()), 1);
    trackDelay = 20;
    await page.evaluate(async () => {document.getElementById('player').currentTime = 123; await PP.saveProgress(true);});
    await page.reload();
    await page.waitForFunction(() => PP.state().playing && !document.querySelector('#player').paused);
    assert.equal(await page.evaluate(() => PP.state().position), 123);
    assert.equal(new URL(page.url()).search, '?id=tt1');

    await page.evaluate(magnet => PP.start({id: 'tt1', magnet, file: 1, season: 1, ep: 2, voice: 'LostFilm'}), old);
    await page.waitForFunction(() => PP.state().playing && PP.episode() === 2);
    await page.reload();
    await page.waitForFunction(() => PP.state().playing && PP.episode() === 2);
    assert.equal(await page.evaluate(() => PP.fileIndex()), 1);

    await page.evaluate(() => window.dispatchEvent(new Event('requestsourcechange')));
    await page.waitForFunction(magnet => PP.magnet() === magnet && PP.state().playing, good);
    assert.equal(await page.evaluate(() => PP.fileIndex()), 8);
    assert.equal(new URL(page.url()).search, '?id=tt1');
    await page.reload();
    await page.waitForFunction(magnet => PP.magnet() === magnet && PP.state().playing, good);
    assert.equal(await page.evaluate(() => PP.episode()), 2);
    assert.equal(await page.evaluate(() => PP.fileIndex()), 8);

    filmDelay = 300;
    failSources = true;
    await page.evaluate(() => sessionStorage.removeItem('vv:item:tt1'));
    await page.reload();
    await page.waitForFunction(magnet => PP.magnet() === magnet && PP.state().playing, good);
    assert.equal(await page.evaluate(() => PP.episode()), 2, 'late basic film metadata restores independently of torrent search');
    failSources = false;
    filmDelay = 20;

    await page.evaluate(() => history.replaceState(history.state, '', location.pathname + location.search + '&tv=1'));
    await page.reload();
    await page.waitForFunction(magnet => PP.magnet() === magnet && PP.state().playing, good);
    assert.equal(new URL(page.url()).searchParams.get('tv'), '1');
    await page.evaluate(() => PP.stop());
    await page.reload();
    await page.waitForFunction(() => seriesUiReady);
    assert.equal(await page.evaluate(() => PP.state().active), false, 'a deliberate stop must remain stopped after reload');

    await page.evaluate(magnet => {
      storeItem({id: 'tt2', imdb_id: 'tt2', kind: 'feature', title: 'Movie'});
      go(watchUrl('tt2', {magnet, file: 0}));
    }, old);
    await page.waitForURL('**/watch.html?id=tt2');
    await page.waitForFunction(() => PP.state().playing && PP.state().id === 'tt2');
    await page.reload();
    await page.waitForFunction(() => PP.state().playing && PP.state().id === 'tt2');
    assert.equal(await page.evaluate(() => PP.magnet()), old);
    assert.equal(await page.evaluate(() => PP.fileIndex()), 0);
    await page.goto(base + '/watch.html?room=' + 'd'.repeat(32));
    await page.waitForFunction(() => window.joinedRoom === 'd'.repeat(32));
    assert.equal(new URL(page.url()).searchParams.get('room'), 'd'.repeat(32));
    assert.deepEqual(errors, []);
    console.log('Playback reload browser checks passed: pending tracks, active HLS, episode switch, saved position, source recovery, late metadata, failed source search, TV flag, explicit stop, movie page, room invitation.');
  } finally {
    await browser.close();
    server.close();
  }
})().catch(error => {console.error(error); server.close(); process.exitCode = 1;});

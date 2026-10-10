const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const shared = fs.readFileSync(path.join(__dirname, '../web/shared.js'), 'utf8');
const source = shared.slice(shared.indexOf('function playbackPageUrl('), shared.indexOf('// ---- ТВ-режим'));

function setup(search = '?id=tt1') {
  const data = new Map();
  const location = { pathname: '/film.html', search, href: 'http://localhost/film.html' + search, origin: 'http://localhost' };
  const history = { state: null, replaceState(state, _, url) { this.state = state; location.search = new URL(url, location.origin).search; } };
  const ctx = vm.createContext({ URL, URLSearchParams, location, history,
    sessionStorage: { setItem: (k, v) => data.set(k, v), getItem: k => data.get(k), removeItem: k => data.delete(k) } });
  vm.runInContext(source, ctx);
  return ctx;
}

test('legacy links are shortened and reload retains playback selection', () => {
  const c = setup('?id=tt1&magnet=magnet%3Asaved&season=2&ep=3&file=4&autoplay=1');
  assert.equal(c.playbackPageParams().get('magnet'), 'magnet:saved');
  assert.equal(c.location.search, '?id=tt1');
  assert.equal(c.playbackPageParams().get('ep'), '3');
  assert.equal(c.playbackPageParams().get('file'), '4');
  c.location.search = '?id=tt2';
  assert.equal(c.playbackPageParams().get('magnet'), null);
});

test('watch navigation transfers parameters without exposing them in the URL', () => {
  const c = setup();
  const url = c.watchUrl('tt1', { magnet: 'magnet:saved', season: 2, ep: 3, voice: 'voice', file: 4 });
  assert.equal(url, '/watch.html?id=tt1');
  c.location.pathname = '/watch.html';
  assert.equal(c.playbackPageParams().get('magnet'), 'magnet:saved');
  assert.equal(c.playbackPageParams().get('voice'), 'voice');
});

test('starting a movie over retains an explicit zero position', () => {
  const c = setup();
  c.watchUrl('tt1', { magnet: 'magnet:saved', file: 0, pos: 0 });
  c.location.pathname = '/watch.html';
  assert.equal(c.playbackPageParams().get('pos'), '0');
});

test('room invitation URLs remain usable', () => {
  const c = setup('?id=tt1&room=invite');
  assert.equal(c.playbackPageParams().get('room'), 'invite');
  assert.equal(c.location.search, '?id=tt1&room=invite');
});

test('reload with a TV flag retains the saved source and episode', () => {
  const c = setup('?id=tt1');
  c.savePlaybackPage(new URLSearchParams('id=tt1&magnet=magnet:saved&season=2&ep=3&file=4&autoplay=1'));
  c.location.search = '?id=tt1&tv=1';
  const params = c.playbackPageParams();
  assert.equal(params.get('magnet'), 'magnet:saved');
  assert.equal(params.get('ep'), '3');
  assert.equal(params.get('tv'), '1');
  assert.equal(c.location.search, '?id=tt1&tv=1');
});

test('an explicit episode link does not inherit the previous source of the same series', () => {
  const c = setup('?id=tt1');
  c.savePlaybackPage(new URLSearchParams('id=tt1&magnet=magnet:saved&season=2&ep=3&file=4&autoplay=1'));
  c.location.search = '?id=tt1&season=2&ep=4&autoplay=1';
  const params = c.playbackPageParams();
  assert.equal(params.get('ep'), '4');
  assert.equal(params.get('magnet'), null);
  assert.equal(params.get('file'), null);
});

test('navigation retains all playback parameters when session storage is unavailable', () => {
  const c = setup();
  c.sessionStorage.setItem = () => { throw new Error('disabled'); };
  const url = c.watchUrl('tt1', { magnet: 'magnet:saved', file: 4, season: 2, ep: 3 });
  const params = new URL(url, 'http://localhost').searchParams;
  assert.equal(params.get('magnet'), 'magnet:saved');
  assert.equal(params.get('file'), '4');
  c.location.pathname = '/watch.html';
  c.location.search = new URL(url, 'http://localhost').search;
  assert.equal(c.playbackPageParams().get('ep'), '3');
});

test('saving an active room keeps its invitation in the visible URL', () => {
  const c = setup('?id=tt1&room=invite');
  c.savePlaybackPage(new URLSearchParams('id=tt1&room=invite&magnet=magnet:saved'));
  assert.equal(c.playbackPageParams().get('room'), 'invite');
  assert.equal(new URLSearchParams(c.location.search).get('room'), 'invite');
});

test('a source waiting for tracks is saved as active and an explicit stop removes it', () => {
  const film = fs.readFileSync(path.join(__dirname, '../web/film.js'), 'utf8');
  const c = setup();
  Object.assign(c, { filmId: 'tt1', voicePref: { 2: 'LostFilm' }, wantVoice: '', PP: { release: () => 'Season release' } });
  vm.runInContext(film.slice(film.indexOf('function syncFilmUrl('), film.indexOf('// Раздача кончилась: ищем')), c);
  c.syncFilmUrl({ active: true, playing: false, magnet: 'magnet:saved', file: 4, season: 2, episode: 3 });
  let params = c.playbackPageParams();
  assert.equal(params.get('autoplay'), '1');
  assert.equal(params.get('magnet'), 'magnet:saved');
  assert.equal(params.get('rt'), 'Season release');
  c.syncFilmUrl({ active: false, playing: false, season: 2, episode: 3 });
  params = c.playbackPageParams();
  assert.equal(params.get('magnet'), null);
  assert.equal(params.get('autoplay'), null);
});

test('page unload saves progress without publishing an explicit playback stop', () => {
  const player = fs.readFileSync(path.join(__dirname, '../web/player.js'), 'utf8');
  const handlers = {}, progress = [];
  const c = vm.createContext({ available: true, document: { addEventListener() {} },
    window: { addEventListener: (name, fn) => { handlers[name] = fn; } },
    maybeSaveProgress: (...args) => progress.push(args),
    leave() {}, hlsPlayer: {}, stop() { assert.fail('unload must not clear the saved playback source'); } });
  const start = player.lastIndexOf('  if (available) {', player.indexOf("document.addEventListener('visibilitychange'"));
  vm.runInContext(player.slice(start, player.indexOf('  // ---- Публичный API ----', start)), c);
  handlers.beforeunload();
  assert.deepEqual(progress, [[true, true]]);
  assert.equal(typeof handlers.pagehide, 'function');
});

test('delayed film metadata restores the saved source exactly once', () => {
  const film = fs.readFileSync(path.join(__dirname, '../web/film.js'), 'utf8');
  const starts = [];
  const c = vm.createContext({ seriesUiReady: false, savedPlaybackRestored: false,
    filmId: 'tt1', filmParams: new URLSearchParams('rt=Season'), startMagnet: 'magnet:saved',
    startFile: 4, wantSeason: 2, wantEp: 3, startPos: 0, wantVoice: 'LostFilm', wantPlay: true,
    PP: { available: true, playing: () => false, start: options => starts.push(options) } });
  vm.runInContext(film.slice(film.indexOf('function resumeSavedPlayback('), film.indexOf('async function initFilmPage(')), c);
  c.resumeSavedPlayback();
  assert.equal(starts.length, 0);
  c.seriesUiReady = true;
  c.resumeSavedPlayback();
  c.resumeSavedPlayback();
  assert.equal(starts.length, 1);
  assert.equal(starts[0].magnet, 'magnet:saved');
  assert.equal(starts[0].file, 4);
  assert.equal(starts[0].ep, 3);
  assert.equal(c.wantPlay, false);
});

test('file metadata uses a JSON body and preserves cancellation', async () => {
  const code = fs.readFileSync(path.join(__dirname, '../web/series.js'), 'utf8');
  let request;
  const signal = {};
  const c = vm.createContext({ currentItem: { tmdb_id: '123' }, dbg() {},
    fetch: async (url, options) => { request = { url, options }; return { ok: true, json: async () => ({ files: [1] }) }; } });
  vm.runInContext(code.slice(code.indexOf('async function fetchFiles('), code.indexOf('function allKnownSeasons(')), c);
  await c.fetchFiles('tt1', 'magnet:saved', 'Release', { signal });
  assert.equal(request.url, '/api/films/tt1/files');
  assert.equal(request.options.method, 'POST');
  assert.equal(request.options.signal, signal);
  assert.deepEqual(JSON.parse(request.options.body), { magnet: 'magnet:saved', title: 'Release', tmdb: '123' });
});

test('canonical episodes can be selected before torrent metadata arrives', async () => {
  const film = fs.readFileSync(path.join(__dirname, '../web/film.js'), 'utf8');
  const buttons = [];
  let played;
  const c = vm.createContext({ wantEp: 0, selectedEpisode: null, selectedSeason: 1,
    seriesEpisodes: {}, filmSeasonEps: { tt1: { 1: 3 } },
    lastSourceId: 'tt1', lastSourceItems: [{ magnet: 'cached' }],
    sourcesEpisodesWrap: {}, episodesNote: {},
    sourcesEpisodes: { appendChild() {}, querySelectorAll: () => [], contains: () => false },
    document: { createElement: tag => {
      const element = { dataset: {}, appendChild() {}, addEventListener() {}, setAttribute() {} };
      if (tag === 'button') buttons.push(element);
      return element;
    } },
    seasonEpisodeCount: () => 3, seasonReleaseSources: items => items,
    showNote() {}, t: s => s, filmText: ru => ru, escapeHtml: s => s,
    playEpisode: async (...args) => { played = args; },
  });
  vm.runInContext(film.slice(film.indexOf('function renderEpisodeGrid('), film.indexOf('async function playEpisode(')), c);
  c.renderEpisodeGrid('tt1');
  assert.equal(buttons.length, 3);
  assert.equal(buttons.every(b => !b.disabled), true);
  await c.onPickEpisode(2);
  assert.deepEqual(played, ['tt1', 1, 2]);
  c.lastSourceItems = [];
  buttons.length = 0;
  c.renderEpisodeGrid('tt1');
  assert.equal(buttons.every(b => b.disabled), true);
});

test('an unavailable selected episode never starts a different episode', async () => {
  const film = fs.readFileSync(path.join(__dirname, '../web/film.js'), 'utf8');
  const items = [{ magnet: 'cached', seeds: 1 }];
  let note;
  const c = vm.createContext({ lastSourceItems: items, voicePref: {}, relPref: {}, playToken: 0,
    episodeProbeLimit: 6, seasonEpisodeCount: () => 3,
    seasonReleaseSources: () => items, pickSeasonSource: () => items[0],
    probeOrder: x => x, Personal: { rank: x => x, preferences: () => ({}) },
    fetchFiles: async () => [{ index: 0, season: 1, episode: 1 }],
    releaseSeasonFit: () => ({ fits: true, count: 1 }), tmdbMappingOn: () => true,
    rememberSeasonFit() {}, dbg() {}, t: s => s, flashFilmNote: s => { note = s; },
  });
  vm.runInContext(film.slice(film.indexOf('async function playEpisode('), film.indexOf('\nfunction openWatch(')), c);
  assert.equal(await c.playEpisode('tt1', 1, 3), false);
  assert.equal(note, 'episodesUnavailable');
});

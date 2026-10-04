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

test('room invitation URLs remain usable', () => {
  const c = setup('?id=tt1&room=invite');
  assert.equal(c.playbackPageParams().get('room'), 'invite');
  assert.equal(c.location.search, '?id=tt1&room=invite');
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
    sourcesEpisodes: { appendChild: b => buttons.push(b) },
    document: { createElement: () => ({ addEventListener() {} }) },
    seasonEpisodeCount: () => 3, seasonReleaseSources: items => items,
    showNote() {}, t: s => s,
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
  vm.runInContext(film.slice(film.indexOf('async function playEpisode('), film.indexOf('// Фильм: играем лучшую раздачу')), c);
  assert.equal(await c.playEpisode('tt1', 1, 3), false);
  assert.equal(note, 'episodesUnavailable');
});

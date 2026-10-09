const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const read = (file) => fs.readFileSync(path.join(__dirname, '../web', file), 'utf8');
const film = read('film.js');
const restore = film.slice(film.indexOf('function restoreWatchSelection()'), film.search(/(?:async\s+)?function initFilmPage\(\)/));
const entry = { film_id: 'tt1', season: 3, episode: 7, voice: 'LostFilm', magnet: 'magnet:saved', file: 6, position: 125 };
function selection(query = '') {
  const p = new URLSearchParams(query);
  const ctx = vm.createContext({ filmId: 'tt1', filmParams: p, historyEntry: () => entry,
    wantSeason: Number(p.get('season')), wantEp: Number(p.get('ep')), wantVoice: p.get('voice') || '',
    wantPlay: p.get('play') === '1', startMagnet: p.get('magnet') || '', startFile: p.has('file') ? Number(p.get('file')) : -1, startPos: Number(p.get('pos')) });
  vm.runInContext(restore + '\nrestoreWatchSelection();', ctx);
  return ctx;
}
test('opening a series restores season, episode and voice', () => {
  const c = selection();
  assert.equal(c.wantSeason, 3); assert.equal(c.wantEp, 7); assert.equal(c.wantVoice, 'LostFilm');
  assert.equal(c.startMagnet, '');
});
test('autoplay resumes saved source, file and position', () => {
  const c = selection('play=1');
  assert.equal(c.startMagnet, entry.magnet); assert.equal(c.startFile, 6); assert.equal(c.startPos, 125);
});
test('explicit next episode is not replaced by history', () => {
  const c = selection('season=4&ep=1&play=1');
  assert.equal(c.wantSeason, 4); assert.equal(c.wantEp, 1); assert.equal(c.startMagnet, '');
});
test('reload of same source restores position and voice but respects explicit position', () => {
  assert.equal(selection('magnet=magnet:saved&file=6').startPos, 125);
  assert.equal(selection('magnet=magnet:saved&file=6').wantVoice, 'LostFilm');
  assert.equal(selection('magnet=magnet:saved&file=6&pos=0').startPos, 0);
});
const player = read('player.js');
const save = player.slice(player.indexOf('  function maybeSaveProgress('), player.indexOf('  function keepStreamCache('));

test('loading files restores episode metadata and notifies the page, including after a switch', async () => {
  const source = player.slice(player.indexOf('  async function loadFiles()'), player.indexOf('  function renderEpisodeList()'));
  let resolveFiles, notified;
  const ctx = vm.createContext({ currentPlay: { id: 'tt0460681', magnet: 'm' }, releaseTitle: '',
    currentFile: 3, curSeason: 0, curEpisode: 0, hooks: {},
    fetchFiles: () => new Promise(resolve => { resolveFiles = resolve; }), dbg() {}, renderEpisodeList() {},
    notify() { notified = [ctx.curSeason, ctx.curEpisode]; } });
  vm.runInContext(source, ctx);
  const pending = ctx.loadFiles();
  ctx.currentFile = 4;
  resolveFiles([{ index: 3, season: 1, episode: 4 }, { index: 4, season: 1, episode: 5 }]);
  await pending;
  assert.deepEqual(notified, [1, 5]);
  const stale = ctx.loadFiles();
  ctx.currentPlay = { id: 'other', magnet: 'new' };
  resolveFiles([{ index: 4, season: 9, episode: 9 }]);
  await stale;
  assert.deepEqual(notified, [1, 5]);
});
test('series progress includes episode and voice and updates local history after success', async () => {
  let sent, remembered;
  const ctx = vm.createContext({ VV: { user: {} }, currentPlay: { id: 'tt1', magnet: entry.magnet },
    playbackReady: true, lastSavedSample: '', lastProgressSend: 0, absTime: () => 125, totalDuration: 1800, keepStreamCache() {},
    currentFile: 6, lastFiles: [{ index: 6, season: 3, episode: 7 }], curSeason: 3, curEpisode: 7,
    selectedVoice: 'LostFilm', seasonEpisodeCount: () => 10,
    fetch: async (_, opts) => { sent = JSON.parse(opts.body); assert.equal(opts.keepalive, true); return { ok: true }; },
    rememberWatchProgress: (body) => { remembered = body; }, dbg() {} });
  vm.runInContext(save, ctx);
  await ctx.maybeSaveProgress(true, true);
  assert.equal(sent.season, 3); assert.equal(sent.episode, 7); assert.equal(sent.voice, 'LostFilm');
  assert.equal(sent.position, 125); assert.equal(remembered.film_id, 'tt1');
});
test('failed progress save is retried and does not update local history', async () => {
  const ctx = vm.createContext({ VV: { user: {} }, currentPlay: { id: 'tt1' }, playbackReady: true, lastSavedSample: '', lastProgressSend: 0,
    absTime: () => 125, totalDuration: 0, currentFile: 6, lastFiles: [], curSeason: 3, curEpisode: 7,
    selectedVoice: 'LostFilm', seasonEpisodeCount: () => 10, fetch: async () => ({ ok: false, status: 500 }),
    rememberWatchProgress: () => assert.fail('failed save must not update history'), dbg() {} });
  vm.runInContext(save, ctx);
  await ctx.maybeSaveProgress(true);
  assert.equal(ctx.lastProgressSend, 0);
});
test('switching episode saves the previous file before resetting playback and retains voice', () => {
  const source = player.slice(player.indexOf('  function selectEpisode('), player.indexOf('  function episodeNeighbor('));
  let savedFile, savedTime, playedTrack;
  const ctx = vm.createContext({ currentPlay: { id: 'tt1', magnet: entry.magnet }, currentFile: 6,
    selectedVoice: 'LostFilm', currentTrack: 2, streamStart: 125, currentQuality: 'source', episodeHistoryEntry: () => ({ position: 87 }),
    lastFiles: [{ index: 7, season: 3, episode: 8 }],
    maybeSaveProgress() { savedFile = ctx.currentFile; savedTime = ctx.streamStart; },
    renderEpisodeList() {}, notify() {}, dbg() {}, loadTracks() {}, beginPlayback(id, magnet, file, track) { playedTrack = track; } });
  vm.runInContext(source, ctx);
  ctx.selectEpisode(7);
  assert.equal(savedFile, 6); assert.equal(savedTime, 125);
  assert.equal(ctx.curEpisode, 8); assert.equal(ctx.autoVoice, 'LostFilm');
  assert.equal(ctx.streamStart, 87);
  assert.equal(playedTrack, 2);
});

test('unchanged position and a stream that has not started cannot refresh old history', async () => {
  let calls = 0;
  const ctx = vm.createContext({ VV: { user: {} }, currentPlay: { id: 'tt1', magnet: 'm' },
    playbackReady: false, lastSavedSample: '', lastProgressSend: 0, absTime: () => 125,
    totalDuration: 0, currentFile: 1, lastFiles: [], curSeason: 4, curEpisode: 21,
    selectedVoice: '', seasonEpisodeCount: () => 30,
    fetch: async () => { calls++; return { ok: true }; }, rememberWatchProgress() {}, dbg() {} });
  vm.runInContext(save, ctx);
  await ctx.maybeSaveProgress(true);
  assert.equal(calls, 0);
  ctx.playbackReady = true;
  await ctx.maybeSaveProgress(true);
  await ctx.maybeSaveProgress(true, true);
  assert.equal(calls, 1);
  ctx.absTime = () => 130;
  await ctx.maybeSaveProgress(true);
  assert.equal(calls, 2);
});

test('each episode keeps its own position, including across releases', async () => {
  const shared = read('shared.js');
  const source = shared.slice(shared.indexOf('const episodeHistory ='), shared.indexOf('// В истории хранится'));
  const ctx = vm.createContext({ currentUser: {}, watchHistory: [], authHooks: [],
    apiGet: async () => ({ ok: true, data: { items: [
      { film_id: 'tt1', season: 4, episode: 21, magnet: 'old', file: 142, position: 136 },
      { film_id: 'tt1', season: 3, episode: 32, magnet: 'old', file: 121, position: 1479 },
    ] } }) });
  vm.runInContext(source, ctx);
  await ctx.loadEpisodeHistory('tt1');
  assert.equal(ctx.episodeHistoryEntry('tt1', 3, 32, 'new', 8).position, 1479);
  assert.equal(ctx.episodeHistoryEntry('tt1', 4, 21, 'new', 9).position, 136);
  assert.equal(ctx.episodeHistoryEntry('tt1', 4, 22, 'old', 142), null);
  ctx.rememberWatchProgress({ film_id: 'tt1', season: 4, episode: 21, magnet: 'old', file: 142, position: 200 });
  assert.equal(ctx.episodeHistoryEntry('tt1', 4, 21, 'new', 9).position, 200);
  assert.equal(ctx.episodeHistoryEntry('tt1', 3, 32, 'new', 8).position, 1479);
});

test('opening an episode restores its position and respects explicit restart from zero', () => {
  const source = player.slice(player.indexOf('  function start(opts)'), player.indexOf('  function stop(opts)'));
  let position;
  const ctx = vm.createContext({ available: true, currentPlay: null, selectedVoice: '', currentTrack: 0,
    playerWrap: {}, closeTrailer() {}, updateQualityButtons() {}, loadFiles() {}, loadTracks() {}, notify() {},
    episodeHistoryEntry: () => ({ position: 136 }), maybeSaveProgress() {},
    beginPlayback: (id, magnet, file, track, pos) => { position = pos; },
    window: { dispatchEvent() {} }, Event: function () {} });
  vm.runInContext(source, ctx);
  ctx.start({ id: 'tt1', magnet: 'new', file: 0, season: 4, ep: 21 });
  assert.equal(position, 136);
  ctx.start({ id: 'tt1', magnet: 'new', file: 0, season: 4, ep: 21, pos: 0 });
  assert.equal(position, 0);
});

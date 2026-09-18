const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const read = (file) => fs.readFileSync(path.join(__dirname, '../web', file), 'utf8');
const film = read('film.js');
const restore = film.slice(film.indexOf('function restoreWatchSelection()'), film.indexOf('function initFilmPage()'));
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
test('series progress includes episode and voice and updates local history after success', async () => {
  let sent, remembered;
  const ctx = vm.createContext({ VV: { user: {} }, currentPlay: { id: 'tt1', magnet: entry.magnet },
    lastProgressSend: 0, absTime: () => 125, totalDuration: 1800, keepStreamCache() {},
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
  const ctx = vm.createContext({ VV: { user: {} }, currentPlay: { id: 'tt1' }, lastProgressSend: 0,
    absTime: () => 125, totalDuration: 0, currentFile: 6, lastFiles: [], curSeason: 3, curEpisode: 7,
    selectedVoice: 'LostFilm', seasonEpisodeCount: () => 10, fetch: async () => ({ ok: false, status: 500 }),
    rememberWatchProgress: () => assert.fail('failed save must not update history'), dbg() {} });
  vm.runInContext(save, ctx);
  await ctx.maybeSaveProgress(true);
  assert.equal(ctx.lastProgressSend, 0);
});
test('switching episode saves the previous file before resetting playback and retains voice', () => {
  const source = player.slice(player.indexOf('  function selectEpisode('), player.indexOf('  function episodeNeighbor('));
  let savedFile, savedTime;
  const ctx = vm.createContext({ currentPlay: { id: 'tt1', magnet: entry.magnet }, currentFile: 6,
    selectedVoice: 'LostFilm', streamStart: 125, currentQuality: 'source',
    lastFiles: [{ index: 7, season: 3, episode: 8 }],
    maybeSaveProgress() { savedFile = ctx.currentFile; savedTime = ctx.streamStart; },
    renderEpisodeList() {}, notify() {}, dbg() {}, loadTracks() {}, playHls() {} });
  vm.runInContext(source, ctx);
  ctx.selectEpisode(7);
  assert.equal(savedFile, 6); assert.equal(savedTime, 125);
  assert.equal(ctx.curEpisode, 8); assert.equal(ctx.autoVoice, 'LostFilm');
});

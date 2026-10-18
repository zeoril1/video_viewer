const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const film = fs.readFileSync(path.join(__dirname, '../web/film.js'), 'utf8');
const playEpisode = film.slice(film.indexOf('async function playEpisode('), film.indexOf('\nfunction openWatch('));

function setup(preferred, files) {
  const requested = [], played = [];
  const automatic = Array.from({ length: 6 }, (_, index) => ({ magnet: 'auto' + index, seeds: 100, title: 'LostFilm' }));
  const context = vm.createContext({
    lastSourceItems: automatic, voicePref: { 7: 'LostFilm' }, relPref: {}, playToken: 0, episodeProbeLimit: 6,
    Personal: { preferences: () => ({}), rank: items => items.slice().reverse() },
    PlaybackSources: { preferred: (id, season) => id === 'tt-test' && season === 7 ? preferred : null },
    seasonReleaseSources: items => items, titleVoices: () => ['LostFilm'], probeOrder: items => items,
    seasonEpisodeCount: () => 23, tmdbMappingOn: () => true,
    releaseSeasonFit: items => ({ fits: items.length >= 23, count: items.length }), rememberSeasonFit() {},
    fetchFiles: async (id, magnet) => { requested.push(magnet); return files(magnet); },
    dbg() {}, flashFilmNote() {}, t: text => text,
    renderSeasonChips() {}, renderEpisodeGrid() {}, renderSeasonVoices() {},
    PP: { available: false }, openWatch: (id, source, file, season, ep) => played.push({ source, file, season, ep }),
  });
  vm.runInContext(playEpisode, context);
  return { context, requested, played, automatic };
}

const fullSeason = () => Array.from({ length: 23 }, (_, index) => ({ index, season: 7, episode: index + 1 }));

test('a manual source stays first for the next episode even if ranking or search metadata excludes it', async () => {
  const selected = { magnet: 'manual', title: 'Manually selected source', season: 8, seeds: 0 };
  const s = setup(selected, magnet => magnet === 'manual' ? [{ index: 12, season: 7, episode: 16 }] : fullSeason());
  assert.equal(await s.context.playEpisode('tt-test', 7, 16), true);
  assert.deepEqual(s.requested, ['manual']);
  assert.equal(s.played[0].source.magnet, 'manual');
  assert.equal(s.played[0].file.index, 12);
  assert.equal(s.played[0].ep, 16);
});

test('a manual source without the requested episode retains every existing automatic fallback', async () => {
  const selected = { magnet: 'manual', title: 'Selected source' };
  const s = setup(selected, magnet => magnet === 'auto0' ? fullSeason() : [{ index: 0, season: 7, episode: 15 }]);
  assert.equal(await s.context.playEpisode('tt-test', 7, 16), true);
  assert.deepEqual(s.requested, ['manual', 'auto5', 'auto4', 'auto3', 'auto2', 'auto1', 'auto0']);
  assert.equal(s.played[0].source.magnet, 'auto0');
  assert.equal(s.played[0].ep, 16);
});

test('automatic playback keeps its original source ordering when nothing was manually selected', async () => {
  const s = setup(null, fullSeason);
  assert.equal(await s.context.playEpisode('tt-test', 7, 16), true);
  assert.deepEqual(s.requested, ['auto5']);
});

test('an explicit voice change releases the manually pinned source before choosing a suitable release', async () => {
  const calls = [];
  const context = vm.createContext({
    lastSourceItems: [{ magnet: 'original' }], selectedSeason: 7, selectedEpisode: 16, voicePref: {}, relPref: {},
    PlaybackSources: { clearPreference: (id, season) => calls.push(['clear', id, season]) },
    pickVoiceSource: () => ({ magnet: 'new-voice-source' }), renderSeasonVoices() {},
    playEpisode: async (...args) => calls.push(['play', ...args]),
  });
  vm.runInContext(film.slice(film.indexOf('async function switchVoice('), film.indexOf('// Сетка серий сезона')), context);
  await context.switchVoice('tt-test', 'NewStudio');
  assert.deepEqual(calls, [['clear', 'tt-test', 7], ['play', 'tt-test', 7, 16]]);
  assert.equal(context.voicePref[7], 'NewStudio');
  assert.equal(context.relPref[7], 'new-voice-source');
});

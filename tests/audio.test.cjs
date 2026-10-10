const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const player = fs.readFileSync(path.join(__dirname, '../web/player.js'), 'utf8');
const series = fs.readFileSync(path.join(__dirname, '../web/series.js'), 'utf8');
const offset = series.indexOf('function matchVoiceOrdinal(');
const match = series.slice(offset, series.indexOf('\n}', offset) + 2);
const ctx = vm.createContext({ VOICE_ALIASES: {} });
vm.runInContext(match + '\n' + player.slice(player.indexOf('  function audioOrdinal('), player.indexOf('  async function loadTracks(')), ctx);

test('selected translation survives reordered audio tracks', () => {
  const selected = { ordinal: 1, title: 'LostFilm', language: 'rus', magnet: 'old' };
  const items = [{ ordinal: 0, title: 'LostFilm', language: 'ru' }, { ordinal: 1, title: 'Original', language: 'eng' }];
  assert.equal(ctx.audioOrdinal(items, selected, 'LostFilm', 'new'), 0);
});
test('unnamed audio retains its slot within the same release', () => {
  const items = [{ ordinal: 0, language: 'rus' }, { ordinal: 1, language: 'rus' }];
  assert.equal(ctx.audioOrdinal(items, { ordinal: 1, language: 'rus', magnet: 'same' }, 'Дорожка 2 RUS', 'same'), 1);
});
test('different release uses matching language instead of blindly keeping index', () => {
  const items = [{ ordinal: 0, language: 'rus' }, { ordinal: 1, language: 'eng' }];
  assert.equal(ctx.audioOrdinal(items, { ordinal: 1, language: 'rus', magnet: 'old' }, '', 'new'), 0);
});
test('missing audio falls back to a valid track or video-only', () => {
  assert.equal(ctx.audioOrdinal([], null, '', 'm'), -1);
  assert.equal(ctx.audioOrdinal([{ ordinal: 3 }], null, 'LostFilm', 'm'), 3);
});

function startupHarness() {
  const pending = [], launches = [];
  const context = vm.createContext({
    pendingPlayback: null, hlsPlayer: null, currentQuality: 'source',
    player: { currentTime: 999, pause() {}, removeAttribute() {}, load() {} },
    stage() {}, window: { dispatchEvent() {} }, Event: function () {},
    loadTracks: () => new Promise(resolve => pending.push(resolve)),
    playHls: (...args) => launches.push(args),
  });
  vm.runInContext(player.slice(player.indexOf('  function beginPlayback('), player.indexOf('  function playHls(')), context);
  return { context, pending, launches };
}

test('initial stream waits for audio/subtitles and never inherits old media time', async () => {
  const { context, pending, launches } = startupHarness();
  context.beginPlayback('tt1', 'm', 2, 1, 0, 'source');
  assert.equal(launches.length, 0);
  context.currentTrack = 3;
  context.currentSubs = 2;
  pending[0](true);
  await Promise.resolve();
  assert.deepEqual(launches, [['tt1', 'm', 2, 3, 0, 'source', 2]]);
});

test('late preparation cannot launch an episode after a newer selection', async () => {
  const { context, pending, launches } = startupHarness();
  context.beginPlayback('tt1', 'm', 1, 0, 0, 'source');
  context.beginPlayback('tt1', 'm', 2, 0, 87, 'source');
  pending[0](true);
  await Promise.resolve();
  assert.equal(launches.length, 0);
  pending[1](true);
  await Promise.resolve();
  assert.equal(launches.length, 1);
  assert.equal(launches[0][2], 2);
  assert.equal(launches[0][4], 87);
});

test('failed metadata probe does not start a guessed audio stream', async () => {
  const { context, pending, launches } = startupHarness();
  context.beginPlayback('tt1', 'm', 1, 0, 0, 'source');
  pending[0](false);
  await Promise.resolve();
  assert.equal(launches.length, 0);
  assert.equal(context.pendingPlayback, null);
});

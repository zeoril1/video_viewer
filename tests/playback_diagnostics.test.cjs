const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function harness() {
  const lines = [], timers = [];
  const video = Object.assign(new EventTarget(), {
    currentTime: 10, paused: false, videoWidth: 1920, videoHeight: 1080, playbackRate: 1, readyState: 4,
    buffered: { length: 1, start: () => 0, end: () => 30 },
    quality: { totalVideoFrames: 100, droppedVideoFrames: 2 },
    getVideoPlaybackQuality() { return this.quality; },
  });
  const button = new EventTarget(), window = new EventTarget();
  const wrap = { hidden: false, trailer: false, classList: { contains: () => wrap.trailer } };
  const state = { active: true, preparing: false, id: 'tt1',
    magnet: 'magnet:?xt=urn:btih:' + 'a'.repeat(40) + '&tr=https://tracker.invalid/private-key',
    file: 140, track: 0, stream_start: 0, position: 610, season: 7, episode: 15, video_codec: 'hevc',
    audio: { ordinal: 0, title: 'Kravec', codec: 'ac3', language: 'rus' } };
  const document = Object.assign(new EventTarget(), { visibilityState: 'visible', getElementById: id => ({ player: video, 'player-wrap': wrap, 'ctrl-debug': button })[id] });
  const context = vm.createContext({ PP: { available: true, state: () => state, release: () => 'Release' },
    document, window, performance, dbg: line => lines.push(line), setInterval: fn => timers.push(fn) });
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../web/playback-diagnostics.js'), 'utf8'), context);
  return { lines, video, state, wrap, document, window, button, tick: () => timers[0]() };
}

test('diagnostics identify actual source and track without private tracker URLs', () => {
  const h = harness();
  h.window.dispatchEvent(new Event('playbackchange'));
  h.window.dispatchEvent(new Event('playbackchange'));
  assert.equal(h.lines.length, 1, 'routine state updates do not repeat source details');
  assert.match(h.lines[0], /hash=a{40} file=140 S7E15/);
  assert.match(h.lines[0], /track=0 audio="Kravec" lang=rus acodec=ac3 vcodec=hevc/);
  assert.doesNotMatch(h.lines[0], /tracker|private-key|magnet:/);
});

test('frame samples show dropped frames during playback and when opening diagnostics', () => {
  const h = harness();
  h.video.dispatchEvent(new Event('playing'));
  h.video.quality = { totalVideoFrames: 820, droppedVideoFrames: 38 };
  h.tick();
  assert.match(h.lines.at(-1), /30s.*frames=820 dropped=38 delta=720\/36 drop=5\.0%/);
  assert.match(h.lines.at(-1), /size=1920x1080 codec=hevc rate=1 buffer=20\.0s/);
  h.button.dispatchEvent(new Event('click'));
  assert.match(h.lines.at(-1), /diagnostics/);
});

test('seek, track change and counter resets cannot reuse old decoding deltas', () => {
  const h = harness();
  h.tick();
  h.state.stream_start = 600;
  h.video.quality = { totalVideoFrames: 130, droppedVideoFrames: 3 };
  h.tick();
  assert.doesNotMatch(h.lines.at(-1), /delta=/);
  h.state.track = 1;
  h.state.audio = { ordinal: 1, title: 'LostFilm' };
  h.tick();
  assert.match(h.lines.at(-2), /track=1 audio="LostFilm"/);
  assert.doesNotMatch(h.lines.at(-1), /delta=/);
  h.video.quality = { totalVideoFrames: 0, droppedVideoFrames: 0 };
  h.tick();
  assert.doesNotMatch(h.lines.at(-1), /delta=/);
});

test('preparation, trailer, stopped or hidden playback does not produce frame samples', () => {
  const h = harness();
  h.state.preparing = true; h.tick();
  h.state.preparing = false; h.wrap.trailer = true; h.tick();
  h.wrap.trailer = false; h.wrap.hidden = true; h.tick();
  h.wrap.hidden = false; h.document.visibilityState = 'hidden'; h.tick();
  h.document.visibilityState = 'visible'; h.video.paused = true; h.tick();
  h.state.active = false; h.video.dispatchEvent(new Event('waiting'));
  assert.equal(h.lines.length, 0);
});

test('browsers without decoding counters still report useful playback context', () => {
  const h = harness();
  h.video.getVideoPlaybackQuality = () => { throw new Error('unsupported'); };
  h.tick();
  assert.match(h.lines.at(-1), /frames=unavailable.*size=1920x1080/);
});

test('presented frame diagnostics discard old frames across seeks, pause and hidden tabs', () => {
  const h = harness(), callbacks = new Map();
  let index = 0;
  h.video.requestVideoFrameCallback = callback => { callbacks.set(++index, callback); return index; };
  h.video.cancelVideoFrameCallback = id => callbacks.delete(id);
  h.tick();
  const [id, callback] = [...callbacks][0]; callbacks.delete(id);
  callback(performance.now(), { mediaTime: 7.5 });
  h.tick();
  assert.match(h.lines.at(-1), /frameClockDelta=2\.500s/);
  const stale = [...callbacks.values()][0];
  h.state.stream_start = 600;
  h.tick();
  stale(performance.now(), { mediaTime: 1 });
  h.tick();
  assert.match(h.lines.at(-1), /frameClockDelta=unavailable/);
  h.video.paused = true; h.video.dispatchEvent(new Event('pause'));
  assert.equal(callbacks.size, 0);
  h.video.paused = false; h.video.dispatchEvent(new Event('playing'));
  assert.equal(callbacks.size, 1);
  h.document.visibilityState = 'hidden'; h.document.dispatchEvent(new Event('visibilitychange'));
  assert.equal(callbacks.size, 0);
});

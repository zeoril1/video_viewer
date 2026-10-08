const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../web/player.js'), 'utf8');
const Hls = require('../web/vendor/hls.min.js');

function section(start, end) {
  return source.slice(source.indexOf(start), source.indexOf(end, source.indexOf(start)));
}

function hlsConfig() {
  const context = vm.createContext({ DEBUG: false });
  vm.runInContext(section('  function playbackHlsConfig(', '  // Запуск HLS-потока'), context);
  return context.playbackHlsConfig();
}

test('a fast remux of a downloaded movie starts from zero with real hls.js live controllers', () => {
  const previousSelf = global.self;
  global.self = globalThis;
  const hls = new Hls(hlsConfig());
  try {
    // An hour can be generated while the client is still fetching tracks or
    // joining a room. A finite live window otherwise starts near its tail.
    const fragments = Array.from({ length: 600 }, (_, sn) => ({ sn, start: sn * 6, duration: 6, level: 0, cc: 0 }));
    const details = { edge: 3600, fragmentEnd: 3600, totalduration: 3600, targetduration: 6,
      age: 0, live: true, fragments, startSN: 0, endSN: 599, PTSKnown: false };
    hls.latencyController.levelDetails = details;
    hls.streamController.startPosition = hls.config.startPosition;
    assert.equal(hls.liveSyncPosition, 0);
    assert.equal(hls.streamController.getNextFragment(0, details).sn, 0);
    assert.equal(hls.config.lowLatencyMode, false);
    assert.equal(hls.config.maxBufferLength, 60);
    assert.equal(hls.config.maxMaxBufferLength, 120);
    assert.equal(hls.config.backBufferLength, 30);
  } finally {
    hls.destroy();
    if (previousSelf === undefined) delete global.self; else global.self = previousSelf;
  }
});

function rangesHarness(ranges, streamStart = 0, position = 0) {
  const context = vm.createContext({ streamStart, player: { currentTime: position,
    buffered: { length: ranges.length, start: i => ranges[i][0], end: i => ranges[i][1] } } });
  vm.runInContext(section('  function bufferedContains(', '  // Абсолютная позиция:'), context);
  return context;
}

test('seeks restart a stream for evicted back buffer and gaps instead of waiting for missing media', () => {
  const context = rangesHarness([[300, 330], [334, 390]], 100, 310);
  assert.equal(context.bufferedContains(100), false);
  assert.equal(context.bufferedContains(399), false);
  assert.equal(context.bufferedContains(425), true);
  assert.equal(context.bufferedContains(432), false);
  assert.equal(context.bufferedContains(440), true);
  assert.equal(context.bufferedContains(491), false);
  assert.equal(context.bufferedContains(Infinity), false);
  assert.equal(context.bufferedAhead(), 20, 'later disconnected ranges do not hide a near stall');
});

function playbackHarness() {
  const instances = [], dispatched = [];
  class FakeHls {
    static Events = { MANIFEST_PARSED: 'manifest', SUBTITLE_TRACKS_UPDATED: 'subs',
      LEVEL_SWITCHED: 'level', FRAG_BUFFERED: 'frag', ERROR: 'error' };
    static isSupported() { return true; }
    constructor(config) { this.config = config; this.handlers = new Map(); this.subtitleTracks = []; instances.push(this); }
    on(event, handler) { this.handlers.set(event, handler); }
    loadSource(url) { this.url = url; }
    attachMedia() {}
    destroy() { this.destroyed = true; }
  }
  const player = { currentTime: 0, readyState: 4, paused: false, buffered: { length: 0 },
    pause() {}, removeAttribute() {}, load() { this.currentTime = 0; } };
  const context = vm.createContext({ player, Hls: FakeHls, URLSearchParams, DEBUG: false,
    window: { Hls: FakeHls, dispatchEvent: event => dispatched.push(event.type) },
    Event: function Event(type) { this.type = type; },
    pendingPlayback: null, hlsPlayer: null, currentPlay: { id: 'tt1', magnet: 'magnet:test' },
    currentFile: 2, currentTrack: 0, currentSubs: -1, currentQuality: 'source',
    streamStart: 0, streamRestarts: 0, maxStreamRestarts: 3, playbackSession: 'session',
    playerWrap: { hidden: false }, playerError: { hidden: true },
    stage() {}, closeTrailer() {}, updateQualityButtons() {}, fetchDuration() {},
    dbg() {}, hlsLogSrc: value => value, autoPlay() {}, showDebug() {}, t: value => value,
    bufferedAhead: () => 0,
  });
  vm.runInContext(section('  function playbackHlsConfig(', '  // Запуск HLS-потока') +
    section('  function playHls(', '  // Полная длительность файла'), context);
  context.playHls('tt1', 'magnet:test', 2, 0, 100, 'source', -1);
  return { context, instances, dispatched };
}

const missingFragment = { fatal: true, type: 'networkError', details: 'fragLoadError', response: { code: 404 } };

test('repeated missing fragments stop after three recoveries even when every manifest loads', () => {
  const f = playbackHarness();
  for (let retry = 0; retry < 3; retry++) {
    const active = f.instances.at(-1);
    active.handlers.get('manifest')();
    f.context.player.currentTime = 7;
    active.handlers.get('error')(null, missingFragment);
    assert.equal(f.context.streamRestarts, retry + 1);
    assert.equal(f.dispatched.length, 0, 'transparent recovery keeps prefetch/UI playback alive');
  }
  const active = f.instances.at(-1);
  active.handlers.get('manifest')();
  active.handlers.get('error')(null, missingFragment);
  assert.equal(f.instances.length, 4);
  assert.equal(f.context.hlsPlayer, null);
  assert.deepEqual(f.dispatched, ['playbackfailure']);
  assert.equal(f.context.playerError.hidden, false);
  assert.equal(new URL(f.instances[1].url, 'http://test').searchParams.get('start'), '107');
});

test('buffered media restores the retry budget while init segments and replaced streams do not', () => {
  const f = playbackHarness(), initial = f.instances[0];
  initial.handlers.get('error')(null, missingFragment);
  const active = f.instances.at(-1);
  initial.handlers.get('frag')(null, { frag: { sn: 0, start: 0 } });
  assert.equal(f.context.streamRestarts, 1);
  active.handlers.get('frag')(null, { frag: { sn: 'initSegment', start: 0 } });
  assert.equal(f.context.streamRestarts, 1);
  active.handlers.get('frag')(null, { frag: { sn: 0, start: 0 } });
  assert.equal(f.context.streamRestarts, 0);
});

function continuationHarness(position = 0) {
  const launches = [], dispatched = [];
  const context = vm.createContext({ currentPlay: { id: 'tt1', magnet: 'magnet:test' },
    trailerActive: false, pendingPlayback: null, completedPlayback: false, totalDuration: 1000,
    currentFile: 2, currentTrack: 1, currentSubs: 0, currentQuality: 'source',
    lastContinuationPosition: -1, shortContinuations: 0, maxStreamRestarts: 3,
    absTime: () => position, playerError: {}, stage() {}, dbg() {},
    playHls: (...args) => launches.push(args),
    window: { dispatchEvent: event => dispatched.push(event.type) },
    Event: function Event(type) { this.type = type; },
  });
  vm.runInContext(section('  function continueStream(', '  function toggleFullscreen('), context);
  return { context, launches, dispatched, position: value => { position = value; } };
}

test('an early HLS window end continues the same episode at its absolute position', () => {
  const f = continuationHarness(240);
  assert.equal(f.context.continueStream(), true);
  assert.deepEqual(f.launches, [['tt1', 'magnet:test', 2, 1, 240, 'source', 0]]);
  f.position(600);
  assert.equal(f.context.continueStream(), true);
  assert.equal(f.context.shortContinuations, 0);
  f.position(999);
  assert.equal(f.context.continueStream(), false, 'the real episode end can advance normally');
  f.position(400); f.context.completedPlayback = true;
  assert.equal(f.context.continueStream(), false, 'skipping final credits must not reopen them');
});

test('a server that ends without making progress has a bounded continuation budget', () => {
  const f = continuationHarness();
  for (let i = 0; i < 4; i++) assert.equal(f.context.continueStream(), true);
  assert.equal(f.launches.length, 3);
  assert.deepEqual(f.dispatched, ['playbackfailure']);
  assert.equal(f.context.playerError.hidden, false);
});

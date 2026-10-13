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

test('late follower readiness cannot autoplay or replace a terminal playback error', () => {
  let plays = 0;
  const stages = [];
  const context = vm.createContext({ pendingPlayback: null, mediaRecovery: null,
    playbackFailurePosition: 1199.93, follower: false, remotePaused: false, trailerActive: false,
    hlsPlayer: {}, currentPlay: {}, streamStart: 1139.51,
    player: { play() { plays++; return Promise.resolve(); } }, stage: (...args) => stages.push(args) });
  vm.runInContext(section('  function autoPlay()', '  let lastSavedSample'), context);
  context.autoPlay();
  assert.equal(plays, 0);
  assert.equal(stages.length, 0);
  context.playbackFailurePosition = null;
  context.autoPlay();
  assert.equal(plays, 1, 'a subsequent user restart can autoplay normally');
});

test('an old rejected play promise cannot replace recovery or failure with an autoplay prompt', async () => {
  for (const changed of ['pendingPlayback', 'mediaRecovery', 'playbackFailurePosition', 'hlsPlayer', 'currentPlay', 'streamStart', 'trailerActive', 'hostPause']) {
    let reject;
    const stages = [];
    const context = vm.createContext({ pendingPlayback: null, mediaRecovery: null, playbackFailurePosition: null,
      hlsPlayer: {}, currentPlay: {}, streamStart: 100, follower: false, remotePaused: false, trailerActive: false,
      player: { play() { return new Promise((_, fail) => { reject = fail; }); } },
      stage: (...args) => stages.push(args) });
    vm.runInContext(section('  function autoPlay()', '  let lastSavedSample'), context);
    context.autoPlay();
    if (changed === 'hostPause') { context.follower = true; context.remotePaused = true; }
    else context[changed] = changed === 'streamStart' || changed === 'playbackFailurePosition' ? 120 : {};
    reject(new Error('Playback aborted by replacement'));
    await Promise.resolve();
    assert.equal(stages.length, 0, changed + ' must invalidate a late play rejection');
  }
});

test('a stale host pause can hold autoplay only while the player is a room guest', () => {
  let plays = 0;
  const stages = [];
  const context = vm.createContext({ pendingPlayback: null, mediaRecovery: null, playbackFailurePosition: null,
    follower: false, remotePaused: true, trailerActive: false, hlsPlayer: {}, currentPlay: {}, streamStart: 100,
    player: { play() { plays++; return Promise.resolve(); } }, stage: (...args) => stages.push(args) });
  vm.runInContext(section('  function autoPlay()', '  let lastSavedSample'), context);
  context.autoPlay();
  assert.equal(plays, 1);
  assert.deepEqual(stages, []);
  context.follower = true;
  context.autoPlay();
  assert.equal(plays, 1, 'a real guest must wait for the host to resume');
  assert.equal(stages.at(-1)[0], 'paused');
});

test('host pause commands apply only to guests and leaving a room does not resume a local pause', () => {
  const stages = [], roles = [];
  const player = { paused: false, readyState: 4, pause() { this.paused = true; },
    play() { this.paused = false; this.plays = (this.plays || 0) + 1; } };
  const context = vm.createContext({ follower: false, remotePaused: false, totalDuration: 100, player,
    stage: (...args) => stages.push(args), autoPlay: () => player.play(),
    window: { dispatchEvent: event => roles.push(event.detail.follower) },
    CustomEvent: function(type, options) { this.detail = options.detail; } });
  vm.runInContext('api = {\n' + section('    setFollower: value =>', '    selectSubtitle: ordinal =>') + '\n};', context);
  context.api.setRemotePaused(true);
  assert.equal(player.paused, false);
  assert.equal(context.remotePaused, false);
  assert.deepEqual(stages, []);
  context.api.setFollower(true);
  context.api.setRemotePaused(true);
  assert.equal(player.paused, true);
  assert.equal(context.remotePaused, true);
  assert.match(stages.at(-1)[1], /Ведущий/);
  context.api.setRemotePaused(false);
  assert.equal(player.plays, 1, 'a ready guest resumes when the host resumes');
  context.api.setRemotePaused(true);
  context.api.setFollower(false);
  assert.equal(context.remotePaused, false);
  assert.equal(player.paused, true);
  assert.equal(player.plays, 1, 'leaving a room keeps its current local pause');
  const count = stages.length;
  context.api.setRemotePaused(true, 'Ждём ведущего');
  assert.equal(stages.length, count, 'a late room command must not change the solo overlay');
  assert.equal(context.remotePaused, false);
  assert.deepEqual(roles, [true, false]);
});

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
    assert.equal(hls.config.maxMaxBufferLength, 6 * 60 * 60);
    assert.equal(hls.config.maxBufferSize, 1024 * 1024 * 1024);
    assert.equal(hls.config.abrMaxWithRealBitrate, true);
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
  assert.equal(context.bufferedContains(425, 5), true);
  assert.equal(context.bufferedContains(429, 5), false, 'headroom cannot cross a gap into a later range');
  assert.equal(context.bufferedContains(440, -1), false);
});

function playbackHarness() {
  const instances = [], dispatched = [], eventDetails = [], timers = [], diagnostics = [], stages = [], mediaHandlers = new Map();
  class FakeHls {
    static Events = { MANIFEST_PARSED: 'manifest', SUBTITLE_TRACKS_UPDATED: 'subs',
      LEVEL_SWITCHED: 'level', FRAG_BUFFERED: 'frag', ERROR: 'error' };
    static isSupported() { return true; }
    constructor(config) { this.config = config; this.handlers = new Map(); this.subtitleTracks = []; instances.push(this); }
    on(event, handler) { this.handlers.set(event, handler); }
    loadSource(url) { this.url = url; }
    attachMedia(media) { this.media = media; }
    startLoad(position) { this.restartPosition = position; }
    recoverMediaError() { this.recoveries = (this.recoveries || 0) + 1; this.media.load(); }
    destroy() { this.destroyed = true; }
  }
  const player = { currentTime: 0, readyState: 4, paused: false, buffered: { length: 0 },
    pause() { this.paused = true; }, removeAttribute() {},
    play() { this.paused = false; this.plays = (this.plays || 0) + 1; return Promise.resolve(); },
    load() { this.currentTime = 0; this.buffered = { length: 0 }; },
    addEventListener(event, handler) { if (!mediaHandlers.has(event)) mediaHandlers.set(event, new Set()); mediaHandlers.get(event).add(handler); },
    removeEventListener(event, handler) { mediaHandlers.get(event)?.delete(handler); } };
  const context = vm.createContext({ player, Hls: FakeHls, URLSearchParams, DEBUG: false,
    window: { Hls: FakeHls, dispatchEvent: event => { dispatched.push(event.type); eventDetails.push(event.detail); } },
    Event: function Event(type) { this.type = type; },
    CustomEvent: function CustomEvent(type, options) { this.type = type; this.detail = options.detail; },
    setTimeout(handler) { const timer = { handler, cancelled: false }; timers.push(timer); return timer; },
    clearTimeout(timer) { if (timer) timer.cancelled = true; },
    pendingPlayback: null, mediaRecovery: null, playbackFailurePosition: null, playbackReady: false,
    hlsPlayer: null, currentPlay: { id: 'tt1', magnet: 'magnet:test' },
    currentFile: 2, currentTrack: 0, currentSubs: -1, currentQuality: 'source',
    currentVideoCodec: 'h264', follower: false, remotePaused: false, trailerActive: false,
    streamStart: 0, streamRestarts: 0, maxStreamRestarts: 3, playbackSession: 'session',
    playerWrap: { hidden: false }, playerError: { hidden: true },
    stage: (...args) => stages.push(args), closeTrailer() {}, updateQualityButtons() {}, fetchDuration() {},
    dbg: message => diagnostics.push(message), hlsLogSrc: value => value,
    autoPlay() { if (!context.mediaRecovery && !(context.follower && context.remotePaused) && !context.trailerActive) player.play(); },
    notify() {}, maybeSaveProgress() {}, showDebug() {}, t: value => value,
    bufferedAhead: () => 0,
  });
  vm.runInContext(section('  function clearMediaRecovery(', '  function stage(') +
    section('  function bufferedContains(', '  // Абсолютная позиция:') +
    section('  function playbackHlsConfig(', '  // Запуск HLS-потока') +
    section('  function playHls(', '  // Полная длительность файла'), context);
  context.playHls('tt1', 'magnet:test', 2, 0, 100, 'source', -1);
  return { context, instances, dispatched, eventDetails, diagnostics, stages, timers,
    runTimers() { const current = timers.splice(0); for (const timer of current) if (!timer.cancelled) timer.handler(); },
    media(event) { for (const handler of [...(mediaHandlers.get(event) || [])]) handler(); },
    ranges(ranges) { player.buffered = { length: ranges.length, start: index => ranges[index][0], end: index => ranges[index][1] }; } };
}

const missingFragment = { fatal: true, type: 'networkError', details: 'fragLoadError', response: { code: 404 } };
const failedAppend = { fatal: true, type: 'mediaError', details: 'bufferAppendError', sourceBufferName: 'video',
  error: { name: 'Error', message: 'video SourceBuffer error. MediaSource readyState: open', code: 0 },
  frag: { sn: 45, start: 270, duration: 6 } };

test('a fatal MSE append rebuilds only the browser buffer and resumes the same relative position after its range arrives', () => {
  const f = playbackHarness(), active = f.instances[0], originalURL = active.url;
  f.context.player.currentTime = 13.4; f.context.player.paused = false;
  active.handlers.get('error')(null, failedAppend);
  assert.equal(active.recoveries, undefined, 'Recovery is deferred until the SourceBuffer callback unwinds');
  assert.equal(f.context.mediaRecovery.position, 13.4);
  assert.equal(f.dispatched.length, 0);
  f.runTimers();
  assert.equal(active.recoveries, 1);
  assert.equal(active.restartPosition, 13.4);
  assert.equal(active.config.startPosition, 13.4, 'Late MEDIA_ATTACHED auto-start must not jump to zero');
  assert.equal(active.url, originalURL);
  assert.equal(f.instances.length, 1);
  assert.equal(f.context.streamStart, 100);
  assert.equal(f.context.currentQuality, 'source');
  assert.equal(f.context.player.currentTime, 0, 'Detach genuinely recreates the MSE buffer');
  f.ranges([[20, 30]]); f.media('canplay');
  assert.equal(f.context.player.plays, undefined, 'An unrelated range must not start playback at zero');
  f.ranges([[10, 20]]); f.media('progress');
  assert.equal(f.context.player.currentTime, 13.4);
  assert.equal(f.context.player.plays, 1);
  assert.equal(f.context.mediaRecovery, null);
  assert.equal(active.destroyed, undefined);
  assert.equal(f.eventDetails.length, 0);
});

test('MSE recovery stops after two attempts even when prefetched fragments succeed between failures', () => {
  const f = playbackHarness(), active = f.instances[0];
  f.context.player.currentTime = 13; f.context.player.paused = false;
  for (let attempt = 1; attempt <= 2; attempt++) {
    active.handlers.get('error')(null, failedAppend); f.runTimers();
    f.ranges([[10, 40]]);
    active.handlers.get('frag')(null, { frag: { sn: attempt, start: 10 } });
    assert.equal(active.recoveries, attempt);
    assert.equal(f.context.player.currentTime, 13);
  }
  active.handlers.get('error')(null, failedAppend);
  f.runTimers();
  assert.equal(active.recoveries, 2);
  assert.equal(f.context.hlsPlayer, null);
  assert.equal(f.context.playbackReady, false, 'A failed stream must not remain ready for room synchronization');
  assert.equal(active.destroyed, true);
  assert.deepEqual(f.dispatched, ['playbackfailure']);
  assert.match(f.eventDetails[0].message, /Браузер не смог обработать видеофрагмент.*восстановление не помогло/);
  assert.equal(f.eventDetails[0].title, 'Просмотр прерван');
  assert.match(f.diagnostics.join('\n'), /sourceBuffer=video.*frag=45 start=270 duration=6/);
  assert.match(f.diagnostics.join('\n'), /message=video SourceBuffer error/);
  assert.match(f.diagnostics.join('\n'), /поток перед остановкой: id=tt1 file=2 track=0 subs=-1 start=100 position=113.000 codec=h264 q=source/);
});

test('a failing replacement SourceBuffer cannot reset or lose the original recovery position before media is buffered', () => {
  const f = playbackHarness(), active = f.instances[0];
  f.context.player.currentTime = 29.5; f.context.player.paused = false;
  active.handlers.get('error')(null, failedAppend);
  active.handlers.get('error')(null, failedAppend);
  f.runTimers();
  assert.equal(active.recoveries, 1, 'Duplicate errors before the queued recovery share one attempt');
  active.handlers.get('error')(null, failedAppend); f.runTimers();
  assert.equal(active.recoveries, 2);
  assert.equal(active.restartPosition, 29.5);
  active.handlers.get('error')(null, failedAppend); f.runTimers();
  assert.equal(active.recoveries, 2);
  assert.equal(f.context.playbackFailurePosition, 129.5, 'The retry button retains the original absolute position after another failed reattach');
  assert.deepEqual(f.dispatched, ['playbackfailure']);
});

test('MSE recovery respects both a local pause and a new pause commanded by the room host', () => {
  for (const paused of [true, false]) {
    const f = playbackHarness(), active = f.instances[0];
    f.context.player.currentTime = 13; f.context.player.paused = paused;
    active.handlers.get('error')(null, failedAppend); f.runTimers();
    if (!paused) { f.context.follower = true; f.context.remotePaused = true; }
    f.ranges([[10, 40]]); f.media('canplay');
    assert.equal(f.context.player.currentTime, 13);
    assert.equal(f.context.player.paused, true);
    assert.equal(f.context.player.plays, undefined);
    assert.equal(f.context.mediaRecovery, null);
    assert.equal(f.stages.at(-1)[1], paused ? 'Просмотр на паузе.' : 'Просмотр на паузе у ведущего.');
  }
});

test('solo MSE recovery keeps a real local pause but ignores stale room state', () => {
  for (const paused of [true, false]) {
    const f = playbackHarness(), active = f.instances[0];
    f.context.player.currentTime = 13; f.context.player.paused = paused;
    f.context.remotePaused = true;
    active.handlers.get('error')(null, failedAppend); f.runTimers();
    f.ranges([[10, 40]]); f.media('canplay');
    assert.equal(f.context.player.currentTime, 13);
    assert.equal(f.context.player.paused, paused);
    assert.equal(f.context.player.plays || 0, paused ? 0 : 1);
    assert.equal(f.context.mediaRecovery, null);
    assert.equal(f.stages.some(([, message]) => /ведущ/i.test(message || '')), false);
    if (paused) assert.equal(f.stages.at(-1)[1], 'Просмотр на паузе.');
  }
});

test('a queued MSE recovery from a replaced HLS stream cannot detach its successor', () => {
  const f = playbackHarness(), old = f.instances[0];
  f.context.player.currentTime = 13;
  old.handlers.get('error')(null, failedAppend);
  const staleCallback = f.timers[0].handler;
  f.context.playHls('tt1', 'magnet:test', 2, 0, 500, 'source', -1);
  const active = f.instances.at(-1);
  staleCallback(); f.runTimers();
  assert.equal(old.recoveries, undefined);
  assert.equal(active.recoveries, undefined);
  assert.equal(f.context.hlsPlayer, active);
  assert.equal(f.context.mediaRecovery, null);
  assert.equal(f.context.streamStart, 500);
  assert.equal(f.dispatched.length, 0);
});

test('fragment timeouts retry the same stream at its relative position, with a bounded budget', () => {
  const f = playbackHarness(), active = f.instances[0];
  const timeout = { fatal: true, type: 'networkError', details: 'fragLoadTimeOut' };
  f.context.player.currentTime = 13;
  active.handlers.get('error')(null, { ...timeout, fatal: false });
  assert.equal(f.context.streamRestarts, 0);
  for (let retry = 0; retry < 3; retry++) {
    active.handlers.get('error')(null, timeout);
    assert.equal(active.restartPosition, 13);
    assert.equal(f.context.streamRestarts, retry + 1);
    assert.equal(f.instances.length, 1);
    assert.equal(active.destroyed, undefined);
    assert.equal(f.dispatched.length, 0);
  }
  active.handlers.get('error')(null, timeout);
  assert.equal(active.destroyed, true);
  assert.deepEqual(f.dispatched, ['playbackfailure']);
});

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
    CustomEvent: function CustomEvent(type, options) { this.type = type; this.detail = options.detail; },
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

test('legacy playback quality cannot request a transcoded stream', () => {
  const f = playbackHarness();
  f.context.playHls('tt1', 'magnet:test', 2, 1, 157, '720', -1);
  const query = new URL(f.instances.at(-1).url, 'http://test').searchParams;
  assert.equal(query.has('quality'), false);
  assert.equal(query.get('start'), '157');
  assert.equal(query.get('track'), '1');
  assert.equal(f.context.currentQuality, 'source');
});

test('the loading overlay preserves the actual playback failure instead of replacing it with a source outage', () => {
  const extras = fs.readFileSync(path.join(__dirname, '../web/playback-extras.js'), 'utf8');
  const elements = new Map(), listeners = new Map();
  const context = vm.createContext({ stage: '', since: 0, wrap: { hidden: false },
    el(id) { if (!elements.has(id)) elements.set(id, {}); return elements.get(id); },
    PP: { isFollower: () => false },
    window: { addEventListener(event, handler) { listeners.set(event, handler); } },
    video: { addEventListener() {} },
  });
  vm.runInContext(extras.slice(extras.indexOf('  const titles ='), extras.indexOf('  setInterval(')), context);
  const message = 'Браузер не смог обработать видеофрагмент. Автоматическое восстановление не помогло.';
  listeners.get('playbackfailure')({ detail: { title: 'Просмотр прерван', message } });
  assert.equal(elements.get('loading-title').textContent, 'Просмотр прерван');
  assert.equal(elements.get('loading-detail').textContent, message);
  listeners.get('playbackfailure')({ detail: { title: 'Не удалось начать просмотр', message: 'Не удалось получить аудиодорожки.' } });
  assert.equal(elements.get('loading-title').textContent, 'Не удалось начать просмотр');
  assert.equal(elements.get('loading-detail').textContent, 'Не удалось получить аудиодорожки.');
  listeners.get('playbackfailure')({});
  assert.equal(elements.get('loading-title').textContent, 'Просмотр прерван');
  assert.match(elements.get('loading-detail').textContent, /Воспроизведение прервалось/);
});

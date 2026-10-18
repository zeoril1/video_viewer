'use strict';

// Frame counters describe browser decoding, not the audible A/V offset. Keep
// these samples alongside the selected source so a growing delay can be traced.
(() => {
  if (!PP.available || typeof dbg !== 'function') return;
  const video = document.getElementById('player');
  const wrap = document.getElementById('player-wrap');
  let contextKey = '', mediaKey = '', previousFrames = null;
  let frameCallback = null, frameEpoch = 0, presentedFrame = null;

  function stopFrames() {
    frameEpoch++;
    if (frameCallback != null && video.cancelVideoFrameCallback) video.cancelVideoFrameCallback(frameCallback);
    frameCallback = null;
    presentedFrame = null;
  }

  function watchFrames() {
    if (!video.requestVideoFrameCallback || frameCallback != null || video.paused || video.seeking
        || document.visibilityState === 'hidden' || !active(PP.state())) return;
    const epoch = frameEpoch;
    frameCallback = video.requestVideoFrameCallback((now, metadata) => {
      if (epoch !== frameEpoch) return;
      frameCallback = null;
      if (Number.isFinite(metadata.mediaTime)) {
        presentedFrame = { delta: video.currentTime - metadata.mediaTime, at: performance.now() };
      }
      watchFrames();
    });
  }

  function frames() {
    try {
      const quality = video.getVideoPlaybackQuality && video.getVideoPlaybackQuality();
      const total = quality ? quality.totalVideoFrames : video.webkitDecodedFrameCount;
      const dropped = quality ? quality.droppedVideoFrames : video.webkitDroppedFrameCount;
      return Number.isFinite(total) && Number.isFinite(dropped) ? { total, dropped } : null;
    } catch (_) { return null; }
  }

  function active(state) {
    return state.active && !state.preparing && !wrap.hidden && !wrap.classList.contains('trailer-mode');
  }

  function context() {
    const state = PP.state();
    if (!state.active) { contextKey = ''; mediaKey = ''; previousFrames = null; stopFrames(); return; }
    if (!active(state)) return;
    const hash = (String(state.magnet).match(/btih:([a-z0-9]{32,40})/i) || [])[1] || '?';
    const media = [state.id, hash, state.file, state.track, state.stream_start].join('|');
    if (media !== mediaKey) { mediaKey = media; previousFrames = null; stopFrames(); }
    watchFrames();
    const audio = state.audio || {};
    const release = PP.release() || '';
    const key = JSON.stringify([media, audio, state.video_codec, release]);
    if (key === contextKey) return;
    contextKey = key;
    dbg('playback: выбранный источник | id=' + state.id + ' hash=' + hash + ' file=' + state.file
      + ' S' + state.season + 'E' + state.episode + ' release=' + JSON.stringify(release)
      + ' track=' + state.track + ' audio=' + JSON.stringify(audio.title || audio.label || '')
      + ' lang=' + (audio.language || '?') + ' acodec=' + (audio.codec || '?')
      + ' vcodec=' + (state.video_codec || '?'));
  }

  function sample(reason) {
    const state = PP.state();
    if (!active(state)) return;
    context();
    const quality = frames();
    let counters = 'frames=unavailable';
    if (quality) {
      counters = 'frames=' + quality.total + ' dropped=' + quality.dropped;
      if (previousFrames && quality.total >= previousFrames.total && quality.dropped >= previousFrames.dropped) {
        const total = quality.total - previousFrames.total, dropped = quality.dropped - previousFrames.dropped;
        counters += ' delta=' + total + '/' + dropped
          + ' drop=' + (total ? 100 * dropped / total : 0).toFixed(1) + '%';
      }
      previousFrames = quality;
    }
    let buffer = 0;
    for (let i = 0; i < video.buffered.length; i++) {
      if (video.currentTime >= video.buffered.start(i) && video.currentTime < video.buffered.end(i)) {
        buffer = video.buffered.end(i) - video.currentTime; break;
      }
    }
    const frameAge = presentedFrame ? Math.max(0, performance.now() - presentedFrame.at) : Infinity;
    const frameClock = frameAge < 1500 ? presentedFrame.delta.toFixed(3) + 's' : 'unavailable';
    dbg('player: кадры @ ' + Number(state.position || 0).toFixed(1) + 's | ' + reason
      + ' | ' + counters + ' | size=' + video.videoWidth + 'x' + video.videoHeight
      + ' codec=' + (state.video_codec || '?') + ' rate=' + video.playbackRate
      + ' buffer=' + buffer.toFixed(1) + 's readyState=' + video.readyState
      + ' frameClockDelta=' + frameClock);
  }

  window.addEventListener('playbackchange', context);
  window.addEventListener('playbackstop', () => { contextKey = ''; mediaKey = ''; previousFrames = null; stopFrames(); });
  window.addEventListener('pagehide', stopFrames);
  window.addEventListener('pageshow', watchFrames);
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') stopFrames(); else watchFrames();
  });
  video.addEventListener('pause', stopFrames);
  video.addEventListener('seeking', stopFrames);
  video.addEventListener('seeked', watchFrames);
  video.addEventListener('playing', () => sample('playing'));
  video.addEventListener('waiting', () => sample('waiting'));
  const debug = document.getElementById('ctrl-debug');
  if (debug) debug.addEventListener('click', () => sample('diagnostics'));
  setInterval(() => { if (!video.paused && document.visibilityState !== 'hidden') sample('30s'); }, 30000);
})();

'use strict';

// Report actual playback, including pauses and buffering, independently of the
// saved resume position. Identity is always supplied by the authenticated server.
(() => {
  if (typeof PP === 'undefined' || !PP.available) return;
  const video = document.getElementById('player');
  let session = '', identity = '', blocked = false, closed = false;
  const queue = [];
  let sending = false;
  const number = value => Number.isFinite(+value) ? Math.max(0, +value) : 0;
  const newSession = () => Array.from(crypto.getRandomValues(new Uint8Array(16)),
    value => value.toString(16).padStart(2, '0')).join('');

  async function send(method, body, keepalive = false) {
    const options = { method, headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body), keepalive };
    const controller = new AbortController();
    options.signal = controller.signal;
    let timeout;
    try {
      await Promise.race([
        fetch('/api/stream/viewing', options),
        new Promise((_, reject) => {
          timeout = setTimeout(() => { controller.abort(); reject(new Error('Viewing heartbeat timed out')); }, 8000);
        }),
      ]);
    } catch (_) {
      // The next heartbeat retries with the current playback position.
    } finally {
      clearTimeout(timeout);
    }
  }

  function drain() {
    if (sending) return;
    sending = true;
    Promise.resolve().then(async () => {
      try {
        while (queue.length) {
          const item = queue.shift();
          if (item.method === 'POST' && (closed || item.body.session !== session)) continue;
          await send(item.method, item.body);
        }
      } finally {
        sending = false;
        if (queue.length) drain();
      }
    });
  }

  function request(method, body, keepalive = false) {
    if (keepalive) { send(method, body, true); return; }
    // A slow request must not queue minutes of stale playback snapshots.
    const pending = method === 'POST' && queue.find(item => item.method === 'POST' && item.body.session === body.session);
    if (pending) pending.body = body;
    else queue.push({ method, body });
    drain();
  }

  function end(keepalive = false) {
    if (session) request('DELETE', { session }, keepalive);
    session = identity = '';
  }

  function report() {
    if (closed) return;
    const state = PP.state();
    if (!state.active || !state.id || !state.magnet) { end(); return; }
    const file = Number.isInteger(state.file) ? state.file : -1;
    const key = [state.id, state.magnet, file, state.season, state.episode].join('|');
    if (key !== identity) {
      end();
      identity = key;
      session = newSession();
    }
    request('POST', {
      session, film_id: state.id, magnet: state.magnet, file,
      season: number(state.season), episode: number(state.episode),
      position: number(state.position), duration: number(state.duration),
      hls_session: PP.session ? PP.session() : '', stream_start: number(state.stream_start),
      track: state.track ?? 0, subs: state.subs ?? -1, quality: state.quality || 'source',
      playing: !!(!blocked && PP.ready() && PP.playing() && !video.paused &&
        !video.ended && !video.seeking && video.readyState >= 3),
    });
  }

  for (const event of ['waiting', 'seeking', 'emptied']) {
    video.addEventListener(event, () => { blocked = true; report(); });
  }
  // A stalled download can still leave enough buffered data to keep playing.
  video.addEventListener('stalled', () => { if (video.readyState < 3) blocked = true; report(); });
  video.addEventListener('playing', () => { blocked = false; report(); });
  video.addEventListener('seeked', () => {
    blocked = video.readyState < 3;
    report();
  });
  for (const event of ['pause', 'ended']) video.addEventListener(event, report);
  window.addEventListener('playbackchange', report);
  window.addEventListener('playbackstop', () => end());
  if (typeof VV !== 'undefined' && VV.onAuth) VV.onAuth(report);
  setInterval(report, 10000);
  document.addEventListener('visibilitychange', report);
  window.addEventListener('focus', report);
  window.addEventListener('pagehide', () => { closed = true; end(true); });
  window.addEventListener('pageshow', () => { closed = false; report(); });
})();

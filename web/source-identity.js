'use strict';

// Search results and saved playback use the same catalog identity check. A
// catalog outage is not evidence of a wrong release: only a confirmed mismatch
// blocks playback. Older links can obtain their missing title from torrent metadata.
const SourceIdentity = (() => {
  const rejected = new Set();
  function key(id, magnet) {
    try {
      const value = new URL(magnet).searchParams.getAll('xt').find(xt => /^urn:btih:/i.test(xt));
      return id + '|' + String(value || magnet).toLowerCase();
    } catch (_) { return id + '|' + magnet; }
  }
  function remember(id, magnet, verdict) {
    const idKey = key(id, magnet);
    if (verdict.status === 'mismatch') rejected.add(idKey);
    if (verdict.status === 'matched') rejected.delete(idKey);
    return rejected.has(idKey) ? { ...verdict, status: 'mismatch' } : verdict;
  }
  async function request(path, body, signal, timeout) {
    const controller = new AbortController(), abort = () => controller.abort();
    if (signal) { signal.addEventListener('abort', abort, { once: true }); if (signal.aborted) abort(); }
    const timer = setTimeout(abort, timeout);
    try {
      const response = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body), signal: controller.signal });
      if (!response.ok) throw new Error('HTTP ' + response.status);
      return await response.json();
    } finally {
      clearTimeout(timer);
      if (signal) signal.removeEventListener('abort', abort);
    }
  }
  async function check({ id, magnet, title = '', signal }) {
    const endpoint = '/api/films/' + encodeURIComponent(id) + '/source-check';
    try {
      let verdict = await request(endpoint, { magnet, title }, signal, 10000);
      if (verdict.status === 'unknown' && !title && !signal?.aborted) {
        const metadata = await request('/api/films/' + encodeURIComponent(id) + '/files', { magnet }, signal, 45000);
        if (metadata.torrent_title && !signal?.aborted) {
          verdict = await request(endpoint, { magnet, torrent_title: metadata.torrent_title }, signal, 10000);
        }
      }
      if (signal?.aborted) return { status: 'unknown' };
      return remember(id, magnet, verdict);
    } catch (_) {
      if (signal?.aborted) return { status: 'unknown' };
      if (typeof dbg === 'function' && !signal?.aborted) dbg('source: проверка названия временно недоступна; сохранённый источник не заменяем');
      return remember(id, magnet, { status: 'unknown' });
    }
  }
  return { check, isRejected: (id, magnet) => rejected.has(key(id, magnet)) };
})();

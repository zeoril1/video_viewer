'use strict';

// Source changes are explicit: listing releases never opens or plays a torrent.
const PlaybackSources = (() => {
  if (typeof PP === 'undefined' || !PP.available) return { open: () => false };
  const playerWrap = document.getElementById('player-wrap');
  const sourceBar = document.createElement('div');
  sourceBar.id = 'playback-source';
  sourceBar.className = 'playback-source';
  sourceBar.hidden = true;
  sourceBar.innerHTML = `<div class="playback-source-copy"><span id="playback-source-label" class="playback-source-label"></span><span id="playback-source-title" class="playback-source-title"></span></div>
    <button id="playback-source-change" class="auth-btn" type="button" aria-haspopup="dialog" aria-controls="source-picker"></button>`;
  playerWrap?.after(sourceBar);
  const sourceTitle = sourceBar.querySelector('#playback-source-title');
  const sourceButton = sourceBar.querySelector('#playback-source-change');
  const knownTitles = new Map();
  const preferredSources = new Map();
  const preferenceKey = (id, season) => JSON.stringify([id, Number(season) || 0]);
  const dialog = document.createElement('dialog');
  dialog.id = 'source-picker';
  dialog.className = 'source-picker';
  dialog.setAttribute('aria-labelledby', 'source-picker-title');
  dialog.setAttribute('aria-describedby', 'source-picker-position');
  dialog.innerHTML = `<header class="source-picker-head">
    <div><h2 id="source-picker-title">Выбрать источник</h2><p id="source-picker-position"></p></div>
    <button id="source-picker-close" class="auth-btn" type="button" aria-label="Закрыть выбор источника" autofocus>✕</button>
  </header>
  <div class="source-picker-body">
    <p id="source-picker-status" role="status" aria-live="polite"></p>
    <div id="source-picker-list" role="radiogroup" aria-label="Источники видео"></div>
    <label id="source-picker-file-wrap" class="source-picker-file" hidden>Файл фильма<select id="source-picker-file"></select></label>
  </div>
  <footer class="source-picker-footer">
    <button id="source-picker-retry" class="auth-btn" type="button">Обновить список</button>
    <button id="source-picker-play" class="auth-btn auth-btn-primary" type="button" disabled>Продолжить просмотр</button>
  </footer>`;
  document.body.append(dialog);
  const el = id => dialog.querySelector('#source-picker-' + id);
  const list = el('list'), status = el('status'), play = el('play'), refresh = el('retry');
  const fileSelect = el('file'), fileWrap = el('file-wrap');
  let active = null;

  function sourceKey(magnet) {
    try {
      const xt = new URL(magnet).searchParams.getAll('xt').find(value => /^urn:btih:/i.test(value));
      if (xt) return xt.toLowerCase();
    } catch (_) {}
    return String(magnet || '');
  }
  const rejected = (id, magnet) => typeof SourceIdentity !== 'undefined' && SourceIdentity.isRejected(id, magnet);
  function updateSourceBar() {
    const state = PP.state();
    const visible = !!state.active && !!playerWrap && !playerWrap.hidden && !playerWrap.classList.contains('trailer-mode');
    sourceBar.hidden = !visible;
    if (!visible) return;
    const english = typeof lang !== 'undefined' && lang === 'en';
    const release = PP.release();
    if (release && state.magnet) knownTitles.set(sourceKey(state.magnet), release);
    sourceBar.querySelector('#playback-source-label').textContent = rejected(state.id, state.magnet)
      ? (english ? 'Source belongs to another title' : 'Источник относится к другому произведению')
      : (english ? 'Current source' : 'Текущий источник');
    sourceTitle.textContent = release || knownTitles.get(sourceKey(state.magnet))
      || (english ? 'Open the source list to see its title' : 'Откройте список, чтобы увидеть название');
    sourceButton.textContent = english ? 'Choose source' : 'Выбрать источник';
    sourceButton.disabled = PP.isFollower();
    sourceButton.title = PP.isFollower() ? (english ? 'The host chooses the source' : 'Источник выбирает ведущий') : '';
  }
  sourceButton.onclick = () => open();
  const playbackKey = state => JSON.stringify([state.id, state.magnet, state.file, state.season || 0, state.episode || 0]);
  const valid = current => active === current && dialog.open && !current.controller.signal.aborted && !PP.isFollower();
  function controls(current) {
    const selected = current.items.find(item => sourceKey(item.magnet) === current.selected);
    play.disabled = current.choosing || !selected || current.selected === sourceKey(current.origin.magnet)
      || (!fileWrap.hidden && fileSelect.value === '');
    refresh.disabled = current.fetching || current.choosing;
    for (const input of list.querySelectorAll('input')) input.disabled = current.choosing;
  }
  function close() {
    if (dialog.open) dialog.close();
    if (!active) return;
    const current = active;
    active = null;
    current.controller.abort();
    clearTimeout(current.poll);
    const opener = current.opener;
    if (opener && opener.isConnected && !opener.hidden) opener.focus({ preventScroll: true });
  }
  // Native close events are queued; an earlier event must not close a reopened picker.
  dialog.addEventListener('close', () => { if (!dialog.open) close(); });
  el('close').onclick = close;
  dialog.addEventListener('cancel', event => { event.preventDefault(); close(); });
  // Stop playback/page shortcuts while focus belongs to the dialog.
  dialog.addEventListener('keydown', event => event.stopPropagation());
  document.addEventListener('keydown', event => {
    if (!dialog.open || !['Escape', 'BrowserBack', 'GoBack', 'Backspace'].includes(event.key)) return;
    if (event.key === 'Backspace' && event.target?.closest?.('textarea, input:not([type=radio])')) return;
    event.preventDefault(); event.stopImmediatePropagation(); close();
  }, true);
  dialog.addEventListener('click', event => {
    if (event.target !== dialog) return;
    const rect = dialog.getBoundingClientRect();
    if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) close();
  });

  function render(current, items) {
    const unique = new Map();
    for (const item of items) {
      if (item && typeof item.magnet === 'string' && item.magnet && !rejected(current.id, item.magnet)) unique.set(sourceKey(item.magnet), item);
    }
    const currentKey = sourceKey(current.origin.magnet);
    if (currentKey && !unique.has(currentKey) && !rejected(current.id, current.origin.magnet)) unique.set(currentKey, { magnet: current.origin.magnet, title: PP.release() || 'Текущий источник' });
    const all = [...unique.values()];
    for (const item of all) if (item.title) knownTitles.set(sourceKey(item.magnet), item.title);
    updateSourceBar();
    // Preferences affect ordering, never hide a release in manual selection.
    const ranked = typeof Personal !== 'undefined' ? Personal.rank(all, current.origin.voice) : all;
    const ordered = [...ranked, ...all.filter(item => !ranked.includes(item))];
    ordered.sort((a, b) => Number(sourceKey(b.magnet) === currentKey) - Number(sourceKey(a.magnet) === currentKey));
    const signature = JSON.stringify(ordered);
    current.items = ordered;
    if (current.signature === signature) { controls(current); return; }
    current.signature = signature;
    const focused = dialog.contains(document.activeElement) && document.activeElement.dataset.source;
    list.replaceChildren();
    for (const item of ordered) {
      const key = sourceKey(item.magnet), row = document.createElement('label');
      row.className = 'source-picker-option';
      const input = document.createElement('input');
      input.type = 'radio'; input.name = 'playback-source'; input.value = key; input.dataset.source = key;
      input.checked = key === current.selected;
      input.onchange = () => {
        if (!valid(current)) return;
        current.selected = key; current.files = null; fileWrap.hidden = true;
        controls(current);
      };
      const copy = document.createElement('span'); copy.className = 'source-picker-copy';
      const title = document.createElement('strong'); title.textContent = item.title || 'Источник';
      const meta = document.createElement('span'); meta.className = 'source-picker-meta';
      const parts = [item.quality, item.size, Number.isFinite(item.seeds) ? item.seeds + ' сидов' : ''].filter(Boolean);
      meta.textContent = parts.join(' · ');
      copy.append(title, meta); row.append(input, copy);
      if (key === currentKey) {
        const badge = document.createElement('span'); badge.className = 'source-picker-current'; badge.textContent = 'Сейчас выбран'; row.append(badge);
      }
      list.append(row);
      if (focused === key) input.focus({ preventScroll: true });
    }
    controls(current);
  }

  async function load(current) {
    if (!valid(current) || current.fetching || current.choosing) return;
    clearTimeout(current.poll);
    current.fetching = true; controls(current);
    status.textContent = current.items.length ? 'Обновляем список источников…' : 'Ищем доступные источники…';
    const request = new AbortController();
    const abort = () => request.abort();
    current.controller.signal.addEventListener('abort', abort, { once: true });
    const timeout = setTimeout(abort, 30000);
    try {
      const response = await fetch('/api/films/' + encodeURIComponent(current.id) + '/sources', { signal: request.signal });
      if (!response.ok) throw new Error('Source search unavailable');
      const data = await response.json();
      if (!valid(current) || current.choosing) return;
      const found = data.items || data.sources || [];
      render(current, Array.isArray(found) ? found : []);
      status.textContent = data.status === 'searching'
        ? 'Поиск продолжается. Можно выбрать уже найденный источник.'
        : current.items.some(item => sourceKey(item.magnet) !== sourceKey(current.origin.magnet))
          ? 'Выберите раздачу и нажмите «Продолжить просмотр».' : 'Другие источники пока не найдены. Попробуйте обновить список.';
      if (data.status === 'searching' && Date.now() < current.deadline) current.poll = setTimeout(() => load(current), 2500);
    } catch (_) {
      if (valid(current) && !current.choosing) status.textContent = 'Не удалось загрузить источники. Нажмите «Обновить список», чтобы повторить поиск.';
    } finally {
      clearTimeout(timeout); current.controller.signal.removeEventListener('abort', abort);
      current.fetching = false;
      if (valid(current)) controls(current);
    }
  }
  refresh.onclick = () => {
    if (!active) return;
    active.deadline = Date.now() + 90000;
    load(active);
  };
  fileSelect.onchange = () => { if (active) controls(active); };

  play.onclick = async () => {
    const current = active;
    if (!current || !valid(current) || play.disabled) return;
    const source = current.items.find(item => sourceKey(item.magnet) === current.selected);
    if (!source) return;
    current.choosing = true; clearTimeout(current.poll); controls(current);
    status.textContent = 'Проверяем файлы выбранного источника…';
    const request = new AbortController(), abort = () => request.abort();
    current.controller.signal.addEventListener('abort', abort, { once: true });
    const timeout = setTimeout(abort, 45000);
    try {
      if (!current.files) {
        const files = await fetchFiles(current.id, source.magnet, source.title, { signal: request.signal });
        if (!valid(current)) return;
        if (!Array.isArray(files)) throw new Error('Не удалось получить файлы этого источника. Повторите попытку или выберите другой.');
        current.files = files.filter(file => Number.isInteger(file.index) && file.index >= 0);
      }
      const series = current.origin.season > 0 && current.origin.episode > 0;
      const files = series ? current.files.filter(file => Number(file.season) === current.origin.season && Number(file.episode) === current.origin.episode) : current.files;
      if (!files.length) throw new Error(series ? 'В этом источнике нет выбранной серии. Выберите другую раздачу.' : 'В этом источнике не найдено видео. Выберите другую раздачу.');
      if (!series && files.length > 1 && (fileWrap.hidden || fileSelect.value === '')) {
        fileSelect.replaceChildren();
        const placeholder = document.createElement('option'); placeholder.value = ''; placeholder.textContent = 'Выберите файл фильма'; fileSelect.append(placeholder);
        for (const file of files) {
          const option = document.createElement('option'); option.value = String(file.index); option.textContent = file.name || 'Видео ' + (file.index + 1); fileSelect.append(option);
        }
        fileWrap.hidden = false; status.textContent = 'В раздаче несколько видео. Выберите файл фильма.';
        return;
      }
      const file = series || files.length === 1 ? files[0] : files.find(item => item.index === Number(fileSelect.value));
      if (!file) throw new Error('Выберите файл фильма.');
      const latest = PP.state();
      if (!valid(current) || playbackKey(latest) !== current.originKey) { close(); return; }
      const position = Number.isFinite(latest.position) && latest.id ? Math.max(0, latest.position) : current.origin.position;
      const params = playbackPageParams();
      params.set('id', current.id); params.set('magnet', source.magnet); params.set('file', String(file.index));
      params.set('rt', source.title || ''); params.set('pos', String(position)); params.set('autoplay', '1');
      for (const [key, value] of [['season', current.origin.season], ['ep', current.origin.episode], ['voice', current.origin.voice]]) {
        if (value) params.set(key, String(value)); else params.delete(key);
      }
      params.delete('track'); params.delete('subs');
      if (PP.start({ id: current.id, magnet: source.magnet, release: source.title, file: file.index,
        season: current.origin.season, ep: current.origin.episode, voice: current.origin.voice,
        pos: position, quality: 'source' }) === false) throw new Error('Не удалось запустить выбранный источник. Повторите попытку.');
      preferredSources.set(preferenceKey(current.id, current.origin.season), source);
      try { savePlaybackPage(params); } catch (_) {}
      close();
    } catch (error) {
      if (valid(current)) status.textContent = error.message || 'Не удалось открыть источник. Повторите попытку.';
    } finally {
      clearTimeout(timeout); current.controller.signal.removeEventListener('abort', abort);
      current.choosing = false;
      if (valid(current)) controls(current);
    }
  };

  function open() {
    if (PP.isFollower()) return false;
    if (dialog.open) { el('close').focus(); return true; }
    // A TV/native close may have happened before its asynchronous close event.
    if (active) close();
    const original = PP.state();
    const params = playbackPageParams();
    const id = original.id || params.get('id');
    if (!id) return false;
    const position = original.id ? original.position : Number(params.get('pos')) || 0;
    active = { id, origin: { ...original, position: Math.max(0, Number(position) || 0) }, originKey: playbackKey(original),
      selected: sourceKey(original.magnet), items: [], signature: '', controller: new AbortController(),
      opener: document.activeElement, deadline: Date.now() + 90000, fetching: false, choosing: false, files: null };
    list.replaceChildren(); fileWrap.hidden = true; status.textContent = 'Ищем доступные источники…';
    const episode = original.season && original.episode ? ' · Сезон ' + original.season + ', серия ' + original.episode : '';
    el('position').textContent = 'Продолжим с ' + fmtTime(active.origin.position) + episode
      + (original.voice ? ' · ' + original.voice : '');
    dialog.showModal(); controls(active); load(active);
    return true;
  }
  window.addEventListener('requestsourcechange', open);
  window.addEventListener('playbackstart', close);
  window.addEventListener('playbackstop', close);
  window.addEventListener('playbackrole', () => { if (PP.isFollower()) close(); });
  window.addEventListener('sourceidentityfailure', event => {
    const detail = event.detail || {};
    for (const [key, source] of preferredSources) {
      if (JSON.parse(key)[0] === detail.id && sourceKey(source.magnet) === sourceKey(detail.magnet)) preferredSources.delete(key);
    }
    if (active) render(active, active.items);
    updateSourceBar();
  });
  for (const event of ['playbackstart', 'playbackstop', 'playbackchange', 'playbackrole']) {
    window.addEventListener(event, updateSourceBar);
  }
  if (playerWrap) new MutationObserver(updateSourceBar).observe(playerWrap, { attributes: true, attributeFilter: ['hidden', 'class'] });
  if (typeof onLang === 'function') onLang(updateSourceBar);
  updateSourceBar();
  window.addEventListener('pagehide', close);
  return { open,
    preferred: (id, season) => {
      const source = preferredSources.get(preferenceKey(id, season));
      return source && rejected(id, source.magnet) ? null : source;
    },
    clearPreference: (id, season) => preferredSources.delete(preferenceKey(id, season)),
  };
})();

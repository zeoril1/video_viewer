'use strict';

const StorageUI = (() => {
  const el = id => document.getElementById(id);
  let timer = null, authorized = false, loading = false, deleting = false;
  let lastData = null, successMessage = '';
  const finite = value => Number.isFinite(+value) ? Math.max(0, +value) : 0;

  function bytes(value) {
    const size = finite(value);
    const units = ['Б', 'КиБ', 'МиБ', 'ГиБ', 'ТиБ'];
    let n = size, unit = 0;
    while (n >= 1024 && unit < units.length - 1) { n /= 1024; unit++; }
    return n.toLocaleString('ru-RU', { maximumFractionDigits: unit ? 2 : 0 }) + ' ' + units[unit];
  }
  function duration(value) {
    const seconds = Math.floor(finite(value));
    const hours = Math.floor(seconds / 3600), minutes = Math.floor(seconds / 60) % 60;
    const tail = String(seconds % 60).padStart(2, '0');
    return hours ? hours + ':' + String(minutes).padStart(2, '0') + ':' + tail : minutes + ':' + tail;
  }
  function episode(path) {
    const text = String(path || '');
    const match = /(?:\b|[_ .-])s(\d{1,3})[ ._-]*e(\d{1,3})(?:\b|[_ .-])/i.exec(' ' + text + ' ')
      || /(?:\b|[_ .-])(\d{1,3})x(\d{1,3})(?:\b|[_ .-])/i.exec(' ' + text + ' ');
    return match ? 'Сезон ' + Number(match[1]) + ', серия ' + Number(match[2]) : '';
  }
  function node(tag, text, className) {
    const item = document.createElement(tag);
    if (text !== undefined) item.textContent = text;
    if (className) item.className = className;
    return item;
  }
  function status(text, kind = '') {
    el('storage-status').textContent = text;
    el('storage-status').className = 'storage-status' + (kind ? ' ' + kind : '');
  }
  function deny() {
    authorized = false;
    clearTimeout(timer);
    el('storage-content').hidden = true;
    el('storage-deny').hidden = false;
  }
  async function errorText(response) {
    const message = (await response.text()).trim();
    return message || 'Ошибка ' + response.status;
  }
  function card(title) {
    const item = node('article', undefined, 'storage-card');
    item.append(node('h2', title));
    el('storage-summary').append(item);
    return item;
  }
  function diskLines(item, disk) {
    if (!disk || !(finite(disk.total_bytes) > 0)) {
      item.append(node('p', 'Данные о диске недоступны.', 'storage-note'));
      return;
    }
    item.append(node('p', 'Всего: ' + bytes(disk.total_bytes)));
    item.append(node('p', 'Занято: ' + bytes(finite(disk.total_bytes) - finite(disk.free_bytes))));
    item.append(node('p', 'Свободно: ' + bytes(disk.free_bytes)));
    if (disk.available_bytes !== undefined && +disk.available_bytes !== +disk.free_bytes) {
      item.append(node('p', 'Доступно серверу: ' + bytes(disk.available_bytes), 'storage-note'));
    }
    if (disk.error) item.append(node('p', disk.error, 'storage-note'));
  }
  function table(headers) {
    const result = node('table', undefined, 'storage-table');
    const head = node('thead'), row = node('tr'), body = node('tbody');
    headers.forEach(text => row.append(node('th', text)));
    head.append(row); result.append(head, body);
    return { table: result, body };
  }
  function renderViewers(viewers, torrents) {
    const count = el('storage-viewer-count');
    if (count) count.textContent = '· открытых просмотров: ' + viewers.length;
    const wrap = el('storage-viewers');
    wrap.replaceChildren();
    if (!viewers.length) { wrap.append(node('p', 'Сейчас никто не смотрит.', 'storage-empty')); return; }
    const grid = table(['Пользователь', 'Фильм / серия', 'Время просмотра сейчас', 'Позиция', 'Состояние']);
    for (const viewer of viewers) {
      const torrent = torrents.find(item => item.hash === viewer.hash);
      const file = (torrent && torrent.files || []).find(item => item.index === viewer.file);
      const row = node('tr'), name = node('td', viewer.username || 'Гость', 'storage-viewer-name');
      const media = node('td', file && file.path || torrent && torrent.name || viewer.film_id || 'Видео', 'storage-file-name');
      if (finite(viewer.season) && finite(viewer.episode)) {
        media.append(node('small', 'Сезон ' + viewer.season + ', серия ' + viewer.episode));
      }
      const position = duration(viewer.position) + (finite(viewer.duration) ? ' / ' + duration(viewer.duration) : '');
      row.append(name, media, node('td', duration(viewer.watched_seconds), 'storage-numeric'),
        node('td', position, 'storage-numeric'), node('td', viewer.playing ? 'Смотрит' : 'Пауза / ожидание'));
      grid.body.append(row);
    }
    wrap.append(grid.table);
  }
  function removeButton(label, torrent, file, busy) {
    const button = node('button', label, 'auth-btn storage-delete');
    button.type = 'button'; button.disabled = busy || deleting;
    if (busy) button.title = 'Файл используется для просмотра или подготовки. Дождитесь завершения.';
    button.addEventListener('click', () => remove(torrent, file));
    return button;
  }
  function renderFiles(storage, viewers) {
    const wrap = el('storage-files');
    wrap.replaceChildren();
    const torrents = Array.isArray(storage.torrents) ? storage.torrents : [];
    let fileCount = 0, downloading = 0;
    for (const torrent of torrents) {
      const files = (torrent.files || []).filter(file => file.available || finite(file.downloaded) || finite(file.stored_bytes) || file.downloading);
      fileCount += files.length;
      downloading += files.filter(file => file.downloading).length;
      const release = node('article', undefined, 'storage-release');
      const header = node('div', undefined, 'storage-release-head'), description = node('div');
      description.append(node('h3', torrent.name || 'Раздача ' + torrent.hash));
      const meta = bytes(torrent.downloaded) + ' / ' + bytes(torrent.total) + ' · В кеше: ' + bytes(torrent.stored_bytes);
      description.append(node('p', meta, 'storage-release-meta'));
      const busy = !!(torrent.busy || finite(torrent.active_readers) || viewers.some(viewer => viewer.hash === torrent.hash));
      header.append(description, removeButton('Удалить всю раздачу', torrent, null, busy));
      release.append(header);
      if (!torrent.metadata_ready) {
        release.append(node('p', 'Получаем список файлов…', 'storage-note'));
      } else if (!files.length) {
        release.append(node('p', 'В этой раздаче пока нет загруженных файлов.', 'storage-note'));
      } else {
        const grid = table(['Файл', 'Загрузка', 'Загружено / размер', 'Скорость', 'Кто смотрит', 'Действия']);
        for (const file of files) {
          const row = node('tr'), name = node('td', file.path || 'Файл ' + file.index, 'storage-file-name');
          const coords = episode(file.path);
          if (coords) name.append(node('small', coords));
          const progress = node('td'), percent = Math.min(100, finite(file.percent));
          const isComplete = finite(file.size) > 0 && finite(file.downloaded) >= finite(file.size);
          progress.append(node('span', isComplete ? 'Загружено' : file.downloading ? 'Загружается' : 'Частично загружено'));
          const bar = node('progress', undefined, 'storage-progress');
          bar.max = 100; bar.value = percent;
          bar.setAttribute('aria-label', 'Загружено ' + percent.toFixed(1) + '%');
          progress.append(bar, node('small', percent.toLocaleString('ru-RU', { maximumFractionDigits: 1 }) + '%'));
          const size = node('td', bytes(file.downloaded) + ' / ' + bytes(file.size), 'storage-numeric');
          size.append(node('small', 'В кеше: ' + bytes(file.stored_bytes), 'storage-note'));
          const watching = viewers.filter(viewer => viewer.hash === torrent.hash && viewer.file === file.index);
          const who = node('td');
          if (!watching.length) who.textContent = '—';
          else for (const viewer of watching) who.append(node('div', (viewer.username || 'Гость') + ' · ' + duration(viewer.watched_seconds)));
          const actions = node('td');
          if (storage.mode === 'disk') actions.append(removeButton('Удалить файл', torrent, file, busy));
          else actions.textContent = 'В составе раздачи';
          row.append(name, progress, size, node('td', bytes(file.download_rate) + '/с', 'storage-numeric'), who, actions);
          grid.body.append(row);
        }
        const scroll = node('div', undefined, 'storage-table-wrap');
        scroll.append(grid.table); release.append(scroll);
      }
      wrap.append(release);
    }
    el('storage-count').textContent = '· файлов: ' + fileCount + ', загружается: ' + downloading;
    if (!torrents.length) wrap.append(node('p', 'На сервере пока нет загруженных фильмов и серий.', 'storage-empty'));
    el('storage-cache-note').textContent = storage.mode === 'memory'
      ? 'Файлы хранятся в оперативной памяти. Можно удалить всю раздачу. После удаления просмотр загрузит её заново.'
      : 'Здесь показаны полностью и частично загруженные файлы. После удаления просмотр загрузит файл заново. Используемые файлы защищены от удаления.';
  }
  function render(data) {
    const storage = data.storage || {}, viewers = Array.isArray(data.viewers) ? data.viewers : [];
    const torrents = Array.isArray(storage.torrents) ? storage.torrents : [];
    el('storage-summary').replaceChildren();
    const cache = card(storage.mode === 'memory' ? 'Видео в памяти' : 'Кеш видео');
    cache.append(node('p', bytes(storage.used_bytes), 'storage-value'));
    cache.append(node('p', 'Занято загруженными данными', 'storage-note'));
    if (storage.mode === 'disk') diskLines(card('Диск с видео'), storage.disk);
    const hls = card('Подготовленные потоки');
    hls.append(node('p', bytes(data.hls && data.hls.used_bytes), 'storage-value'));
    diskLines(hls, data.hls);
    hls.append(node('p', 'Свободное место относится к этому диску; если кеш расположен на нём же, это одно и то же место.', 'storage-note'));
    renderViewers(viewers, torrents);
    renderFiles(storage, viewers);
  }
  async function load() {
    if (!authorized || loading || deleting) return;
    loading = true; clearTimeout(timer);
    try {
      const response = await fetch('/api/admin/storage', { cache: 'no-store' });
      if (response.status === 401 || response.status === 403) { deny(); return; }
      if (!response.ok) throw new Error(await errorText(response));
      lastData = await response.json();
      render(lastData);
      status(successMessage || 'Обновлено: ' + new Date().toLocaleTimeString('ru-RU'), successMessage ? 'success' : '');
      successMessage = '';
    } catch (error) {
      status('Не удалось обновить состояние: ' + error.message + (lastData ? '. Показаны последние полученные данные.' : ''), 'error');
    } finally {
      loading = false;
      if (authorized) timer = setTimeout(load, 3000);
    }
  }
  async function remove(torrent, file) {
    if (deleting) return;
    const name = file ? file.path || 'Файл ' + file.index : torrent.name || torrent.hash;
    const prompt = file ? 'Удалить загруженный файл «' + name + '» с сервера?' : 'Удалить все загруженные файлы раздачи «' + name + '» с сервера?';
    if (!confirm(prompt + '\nДля следующего просмотра потребуется загрузка заново.')) return;
    deleting = true; clearTimeout(timer);
    if (lastData) render(lastData);
    status('Удаляем «' + name + '»…');
    try {
      const url = '/api/admin/storage/' + encodeURIComponent(torrent.hash) + (file ? '?file=' + encodeURIComponent(file.index) : '');
      const response = await fetch(url, { method: 'DELETE' });
      if (response.status === 401 || response.status === 403) { deny(); return; }
      if (!response.ok) throw new Error(response.status === 409
        ? 'Файл сейчас используется для просмотра или подготовки. Дождитесь завершения.' : await errorText(response));
      successMessage = 'Удалено: ' + name;
    } catch (error) { status('Не удалось удалить: ' + error.message, 'error'); }
    finally {
      deleting = false;
      if (lastData) render(lastData);
      if (successMessage) load();
      else if (authorized) timer = setTimeout(load, 3000);
    }
  }
  async function init() {
    el('storage-reload').addEventListener('click', load);
    el('storage-logout').addEventListener('click', async () => {
      await fetch('/api/auth/logout', { method: 'POST' }).catch(() => {});
      location.href = '/';
    });
    try {
      const response = await fetch('/api/auth/me', { cache: 'no-store' });
      const data = response.ok ? await response.json() : {};
      if (!data.user || data.user.role !== 'admin') { deny(); return; }
      authorized = true;
      el('storage-user').textContent = data.user.username + ' (админ)';
      el('storage-content').hidden = false;
      await load();
    } catch (error) {
      el('storage-content').hidden = false;
      status('Не удалось проверить доступ. Обновите страницу.', 'error');
    }
  }
  window.addEventListener('pagehide', () => { clearTimeout(timer); authorized = false; });
  init();
  return { bytes, duration, episode, render };
})();

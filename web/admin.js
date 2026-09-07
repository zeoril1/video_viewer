'use strict';

const meEl = document.getElementById('admin-user');
const denyEl = document.getElementById('admin-deny');
const table = document.getElementById('films-table');
const bodyEl = document.getElementById('films-body');
const countEl = document.getElementById('admin-count');
const logEl = document.getElementById('admin-log');
const reloadBtn = document.getElementById('reload-list');
const refreshAllBtn = document.getElementById('refresh-all');
const clearLogBtn = document.getElementById('clear-log');
const logoutBtn = document.getElementById('admin-logout');

// Вкладки: «Пустые поля» и «Не найдены на TMDB».
const tabMissing = document.getElementById('tab-missing');
const tabNotfound = document.getElementById('tab-notfound');
const notfoundWrap = document.getElementById('notfound-wrap');
const notfoundBody = document.getElementById('notfound-body');

let lastSeq = 0;
let currentTab = 'missing'; // 'missing' | 'notfound'
let missingCount = 0;
let notFoundCount = 0;
let reloadTimer = null;

// ---- Авторизация (доступ только для роли admin) ----
async function init() {
  let me = null;
  try {
    const res = await fetch('/api/auth/me');
    if (res.ok) {
      const d = await res.json();
      me = d.user || null;
    }
  } catch (e) {}
  if (!me || me.role !== 'admin') {
    denyEl.hidden = false;
    table.hidden = true;
    table.closest('.admin-table-wrap').hidden = true;
    notfoundWrap.hidden = true;
    tabMissing.hidden = true;
    tabNotfound.hidden = true;
    refreshAllBtn.hidden = true;
    reloadBtn.hidden = true;
    document.querySelector('.admin-log-wrap').hidden = true;
    return;
  }
  meEl.textContent = me.username + ' (админ)';
  tabMissing.addEventListener('click', () => showTab('missing'));
  tabNotfound.addEventListener('click', () => showTab('notfound'));
  loadFilms();
  loadNotFound();
  pollLogs();
}

logoutBtn.addEventListener('click', async () => {
  await fetch('/api/auth/logout', { method: 'POST' }).catch(() => {});
  location.href = '/';
});

// ---- Лог прогресса ----
function logLine(kind, msg) {
  const div = document.createElement('div');
  div.className = 'log-line log-' + (kind === 'ok' ? 'ok' : kind === 'error' ? 'error' : 'info');
  div.textContent = msg;
  logEl.appendChild(div);
  while (logEl.children.length > 300) logEl.firstChild.remove();
  logEl.scrollTop = logEl.scrollHeight;
}

async function pollLogs() {
  try {
    const res = await fetch('/api/admin/logs?after=' + lastSeq);
    if (res.ok) {
      const d = await res.json();
      for (const ln of d.lines || []) {
        logLine(ln.kind, '[' + ln.at + '] ' + ln.msg);
        lastSeq = ln.seq;
        // Завершилось обновление отдельного фильма — обновим таблицы,
        // чтобы отметки «не найден» и пустые поля отразились сразу.
        if (/обновление \S+: (готово|error)/.test(ln.msg)) scheduleReload();
      }
    }
  } catch (e) {}
  setTimeout(pollLogs, 2000);
}

// ---- Список записей с пустыми полями ----
async function loadFilms() {
  logLine('info', 'Загружаю список записей с пустыми полями...');
  try {
    const res = await fetch('/api/admin/films/missing?limit=200');
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const d = await res.json();
    renderFilms(d.items || []);
    missingCount = d.total || 0;
    updateCount();
    logLine('ok', 'Список загружен: ' + (d.items || []).length + ' записей');
  } catch (err) {
    logLine('error', 'Не удалось загрузить список: ' + err.message);
  }
}

function renderFilms(items) {
  bodyEl.innerHTML = '';
  if (!items.length) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="17" class="admin-empty">Нет записей с пустыми полями.</td>';
    bodyEl.appendChild(tr);
    return;
  }
  for (const it of items) bodyEl.appendChild(makeRow(it));
}

function makeRow(it) {
  const tr = document.createElement('tr');
  tr.dataset.id = it.imdb_id;
  tr.innerHTML = `
    <td class="admin-id">${attr(it.imdb_id || '')}</td>
    <td><input data-f="title" value="${attr(it.title)}" /></td>
    <td><input data-f="title_ru" value="${attr(it.title_ru)}" /></td>
    <td><input data-f="kind" value="${attr(it.kind)}" /></td>
    <td><input data-f="release_date" value="${attr(it.release_date)}" placeholder="YYYY-MM-DD" /></td>
    <td><input data-f="rating" type="number" step="0.1" value="${it.rating || ''}" /></td>
    <td><input data-f="rating_tmdb" type="number" step="0.1" value="${it.rating_tmdb || ''}" /></td>
    <td><input data-f="tmdb_id" value="${attr(it.tmdb_id)}" /></td>
    <td><input data-f="genres" value="${attr((it.genres || []).join(', '))}" /></td>
    <td><input data-f="poster_url" value="${attr(it.poster_url)}" /></td>
    <td><input data-f="movie_length" type="number" value="${it.movie_length || ''}" /></td>
    <td><input data-f="director" value="${attr(it.director)}" /></td>
    <td><input data-f="actors" value="${attr((it.actors || []).join(', '))}" /></td>
    <td><input data-f="seasons" type="number" value="${it.seasons || ''}" /></td>
    <td><textarea data-f="plot_ru" rows="2">${escapeHtml(it.plot_ru || '')}</textarea></td>
    <td><textarea data-f="plot" rows="2">${escapeHtml(it.plot || '')}</textarea></td>
    <td class="admin-actions">
      <button class="btn-save" type="button">Сохранить</button>
      <button class="btn-refresh" type="button">Обновить</button>
    </td>
  `;
  tr.querySelector('.btn-save').addEventListener('click', () => saveRow(tr));
  tr.querySelector('.btn-refresh').addEventListener('click', () => refreshRow(tr));
  return tr;
}

function collectRow(tr) {
  const d = {};
  tr.querySelectorAll('[data-f]').forEach((el) => {
    const key = el.dataset.f;
    if (key === 'genres' || key === 'actors') {
      d[key] = splitList(el.value);
    } else if (key === 'rating' || key === 'rating_tmdb' || key === 'movie_length' || key === 'seasons') {
      d[key] = el.value === '' ? 0 : Number(el.value);
    } else {
      d[key] = el.value.trim();
    }
  });
  return d;
}

function splitList(s) {
  return s.split(',').map((x) => x.trim()).filter(Boolean);
}

// ---- Вкладки ----
function showTab(name) {
  currentTab = name;
  const isMissing = name === 'missing';
  table.closest('.admin-table-wrap').hidden = !isMissing;
  notfoundWrap.hidden = isMissing;
  tabMissing.classList.toggle('active', isMissing);
  tabNotfound.classList.toggle('active', !isMissing);
  updateCount();
}

function updateCount() {
  if (currentTab === 'notfound') {
    countEl.textContent = 'Не найдены на TMDB: ' + notFoundCount;
  } else {
    countEl.textContent = 'Записей с пустыми полями: ' + missingCount;
  }
}

// ---- Список «не найдены на TMDB» ----
async function loadNotFound() {
  try {
    const res = await fetch('/api/admin/films/notfound?limit=200');
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const d = await res.json();
    notFoundCount = d.total || 0;
    renderNotFound(d.items || []);
    updateCount();
  } catch (err) {
    logLine('error', 'Не удалось загрузить список «не найдены»: ' + err.message);
  }
}

function renderNotFound(items) {
  notfoundBody.innerHTML = '';
  if (!items.length) {
    const tr = document.createElement('tr');
    tr.innerHTML = '<td colspan="8" class="admin-empty">Нет записей, помеченных как не найденные на TMDB.</td>';
    notfoundBody.appendChild(tr);
    return;
  }
  for (const it of items) notfoundBody.appendChild(makeNotFoundRow(it));
}

function makeNotFoundRow(it) {
  const tr = document.createElement('tr');
  tr.dataset.id = it.imdb_id;
  tr.innerHTML = `
    <td class="admin-id">${attr(it.imdb_id || '')}</td>
    <td>${attr(it.title)}</td>
    <td>${attr(it.title_ru)}</td>
    <td>${attr(it.kind)}</td>
    <td>${attr(it.release_date)}</td>
    <td>${it.year || ''}</td>
    <td>${attr(it.tmdb_id)}</td>
    <td class="admin-actions">
      <button class="btn-refresh" type="button">Обновить</button>
      <button class="btn-unmark" type="button">Снять отметку</button>
    </td>
  `;
  tr.querySelector('.btn-refresh').addEventListener('click', () => refreshRow(tr));
  tr.querySelector('.btn-unmark').addEventListener('click', () => unmarkRow(tr));
  return tr;
}

async function unmarkRow(tr) {
  const id = tr.dataset.id;
  try {
    const res = await fetch('/api/admin/films/' + encodeURIComponent(id) + '/notfound', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ not_found: false }),
    });
    if (!res.ok) throw new Error('HTTP ' + res.status);
    logLine('ok', 'Снята отметка «не найден на TMDB»: ' + id);
    tr.remove();
    notFoundCount = Math.max(0, notFoundCount - 1);
    updateCount();
    loadFilms();
  } catch (err) {
    logLine('error', 'Не удалось снять отметку ' + id + ': ' + err.message);
  }
}

// Перезагрузка обоих списков после завершения обновления (debounce).
function scheduleReload() {
  if (reloadTimer) clearTimeout(reloadTimer);
  reloadTimer = setTimeout(() => {
    reloadTimer = null;
    loadFilms();
    loadNotFound();
  }, 400);
}

// ---- Действия со строкой ----
async function saveRow(tr) {
  const id = tr.dataset.id;
  const data = collectRow(tr);
  try {
    const res = await fetch('/api/admin/films/' + encodeURIComponent(id), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(data),
    });
    if (!res.ok) throw new Error('HTTP ' + res.status);
    logLine('ok', 'Сохранено: ' + id);
    flash(tr, true);
  } catch (err) {
    logLine('error', 'Ошибка сохранения ' + id + ': ' + err.message);
    flash(tr, false);
  }
}

function refreshRow(tr) {
  const id = tr.dataset.id;
  const btn = tr.querySelector('.btn-refresh');
  btn.disabled = true;
  logLine('info', 'Запущено обновление ' + id + ' из TMDB');
  fetch('/api/admin/films/' + encodeURIComponent(id) + '/refresh', { method: 'POST' })
    .catch((e) => logLine('error', 'Не удалось запустить обновление ' + id + ': ' + e.message))
    .finally(() => { btn.disabled = false; });
}

refreshAllBtn.addEventListener('click', () => {
  refreshAllBtn.disabled = true;
  logLine('info', 'Запущено массовое обновление всех записей из TMDB');
  fetch('/api/admin/refresh-all', { method: 'POST' })
    .catch((e) => logLine('error', 'Не удалось запустить массовое обновление: ' + e.message))
    .finally(() => { refreshAllBtn.disabled = false; });
});

reloadBtn.addEventListener('click', () => { loadFilms(); loadNotFound(); });
clearLogBtn.addEventListener('click', () => { logEl.innerHTML = ''; });

function flash(tr, ok) {
  tr.classList.add(ok ? 'flash-ok' : 'flash-err');
  setTimeout(() => tr.classList.remove('flash-ok', 'flash-err'), 1200);
}

function escapeHtml(s) {
  return String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}
function attr(s) {
  return escapeHtml(s == null ? '' : s);
}

init();

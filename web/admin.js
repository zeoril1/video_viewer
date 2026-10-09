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

// Вкладки: «Пустые поля» и «Не удалось обновить».
const tabMissing = document.getElementById('tab-missing');
const tabNotfound = document.getElementById('tab-notfound');
const tabUsers = document.getElementById('tab-users');
const usersPanel = document.getElementById('users-panel');
const usersBody = document.getElementById('users-body');
const usersStatus = document.getElementById('users-status');
const filmsLayout = document.getElementById('admin-films-layout');
const filmNote = document.getElementById('admin-film-note');
const notfoundWrap = document.getElementById('notfound-wrap');
const notfoundBody = document.getElementById('notfound-body');

let lastSeq = 0;
let currentTab = 'missing'; // 'missing' | 'notfound' | 'users'
let missingCount = 0;
let notFoundCount = 0;
let reloadTimer = null;
let adminUserID = 0;
let usersCount = 0;

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
    tabUsers.hidden = true;
    usersPanel.hidden = true;
    filmsLayout.hidden = true;
    refreshAllBtn.hidden = true;
    reloadBtn.hidden = true;
    document.querySelector('.admin-log-wrap').hidden = true;
    return;
  }
  meEl.textContent = me.username + ' (админ)';
  adminUserID = me.id;
  tabMissing.addEventListener('click', () => showTab('missing'));
  tabNotfound.addEventListener('click', () => showTab('notfound'));
  tabUsers.addEventListener('click', () => { showTab('users'); loadUsers(); });
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
      refreshAllBtn.disabled = !!d.running;
      for (const ln of d.lines || []) {
        logLine(ln.kind, '[' + ln.at + '] ' + ln.msg);
        lastSeq = ln.seq;
        // После обновления фильма сразу обновляем таблицы — отметки «не найден»/пустые поля.
        if ((/^обновление /.test(ln.msg) && ln.kind !== 'info') || /массовое обновление: (завершено|прервано)/.test(ln.msg)) scheduleReload();
      }
    }
  } catch (e) {}
  setTimeout(pollLogs, 2000);
}

// ---- Список записей с пустыми полями ----
async function loadFilms() {
  logLine('info', 'Загружаю список записей с пустыми полями...');
  try {
    const res = await fetch('/api/admin/films/missing');
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
    tr.innerHTML = '<td colspan="16" class="admin-empty">Нет записей с пустыми полями.</td>';
    bodyEl.appendChild(tr);
    return;
  }
  const fragment = document.createDocumentFragment();
  for (const it of items) fragment.appendChild(makeRow(it));
  bodyEl.appendChild(fragment);
}

function makeRow(it) {
  const tr = document.createElement('tr');
  tr.dataset.id = it.imdb_id;
  tr.innerHTML = `
    <td class="admin-id">${attr(it.imdb_id || '')}<input type="hidden" data-f="rating" value="${Number(it.rating) || 0}" /></td>
    <td><input data-f="title" value="${attr(it.title)}" /></td>
    <td><input data-f="title_ru" value="${attr(it.title_ru)}" /></td>
    <td><input data-f="kind" value="${attr(it.kind)}" /></td>
    <td><input data-f="release_date" value="${attr(it.release_date)}" placeholder="YYYY-MM-DD" /></td>
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
  const isUsers = name === 'users';
  filmsLayout.hidden = isUsers;
  usersPanel.hidden = !isUsers;
  filmNote.hidden = isUsers;
  refreshAllBtn.hidden = isUsers;
  table.closest('.admin-table-wrap').hidden = !isMissing;
  notfoundWrap.hidden = name !== 'notfound';
  tabMissing.classList.toggle('active', isMissing);
  tabNotfound.classList.toggle('active', name === 'notfound');
  tabUsers.classList.toggle('active', isUsers);
  updateCount();
}

function updateCount() {
  if (currentTab === 'users') {
    countEl.textContent = 'Пользователей: ' + usersCount;
  } else if (currentTab === 'notfound') {
    countEl.textContent = 'Не удалось обновить: ' + notFoundCount;
  } else {
    countEl.textContent = 'Записей с пустыми полями: ' + missingCount;
  }
}

// ---- Пользователи ----
function userStatus(text, kind = '') {
  usersStatus.textContent = text;
  usersStatus.className = 'users-status' + (kind ? ' ' + kind : '');
}

async function loadUsers() {
  userStatus('Загружаем пользователей…');
  try {
    const response = await fetch('/api/admin/users', { cache: 'no-store' });
    if (!response.ok) throw new Error((await response.text()).trim() || 'Ошибка ' + response.status);
    const data = await response.json();
    const users = Array.isArray(data.items) ? data.items : [];
    usersBody.replaceChildren();
    usersCount = users.length;
    for (const user of users) usersBody.appendChild(makeUserRow(user));
    if (!users.length) {
      const row = document.createElement('tr'), cell = document.createElement('td');
      cell.colSpan = 4; cell.textContent = 'Пользователей пока нет.'; cell.className = 'admin-empty';
      row.appendChild(cell); usersBody.appendChild(row);
    }
    updateCount(); userStatus('');
  } catch (error) { userStatus('Не удалось загрузить пользователей: ' + error.message, 'error'); }
}

function makeUserRow(user) {
  const row = document.createElement('tr');
  const name = document.createElement('td'), registered = document.createElement('td');
  name.textContent = user.username + (user.id === adminUserID ? ' (вы)' : '');
  const date = user.created_at ? new Date(user.created_at) : null;
  registered.textContent = date && !Number.isNaN(date.getTime()) ? date.toLocaleDateString('ru-RU') : '—';
  const roleCell = document.createElement('td'), select = document.createElement('select');
  select.setAttribute('aria-label', 'Роль пользователя ' + user.username);
  for (const [role, label] of [['user', 'Пользователь'], ['moderator', 'Модератор'], ['admin', 'Администратор']]) {
    const option = document.createElement('option');
    option.value = role; option.textContent = label; select.appendChild(option);
  }
  select.value = user.role;
  roleCell.appendChild(select);
  const actions = document.createElement('td'), save = document.createElement('button');
  save.type = 'button'; save.className = 'auth-btn'; save.textContent = 'Сохранить';
  save.disabled = true;
  select.addEventListener('change', () => { save.disabled = select.value === user.role; });
  save.addEventListener('click', async () => {
    const role = select.value;
    if (user.id === adminUserID && role !== 'admin' && !confirm('Изменить свою роль? После сохранения доступ к администрированию будет закрыт.')) return;
    save.disabled = select.disabled = true;
    try {
      const response = await fetch('/api/admin/users/' + encodeURIComponent(user.id) + '/role', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ role }),
      });
      if (!response.ok) throw new Error((await response.text()).trim() || 'Ошибка ' + response.status);
      user.role = role;
      userStatus('Права пользователя ' + user.username + ' сохранены.', 'success');
      if (user.id === adminUserID && role !== 'admin') location.href = '/';
    } catch (error) { userStatus('Не удалось сохранить права: ' + error.message, 'error'); }
    finally { select.disabled = false; save.disabled = select.value === user.role; }
  });
  actions.appendChild(save);
  row.appendChild(name); row.appendChild(registered); row.appendChild(roleCell); row.appendChild(actions);
  return row;
}

// ---- Список «не найдены на TMDB» ----
async function loadNotFound() {
  try {
    const res = await fetch('/api/admin/films/notfound');
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
    tr.innerHTML = '<td colspan="10" class="admin-empty">Нет записей с отложенным обновлением.</td>';
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
    <td>${attr(it.refresh_error)}</td>
    <td>${attr(it.retry_at ? new Date(it.retry_at).toLocaleString('ru-RU') : '')}</td>
    <td class="admin-actions">
      <button class="btn-refresh" type="button">Обновить</button>
      <button class="btn-unmark" type="button">Вернуть в очередь</button>
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
    logLine('ok', 'Возвращено в очередь: ' + id);
    tr.remove();
    notFoundCount = Math.max(0, notFoundCount - 1);
    updateCount();
    loadFilms();
  } catch (err) {
    logLine('error', 'Не удалось снять отметку ' + id + ': ' + err.message);
  }
}

function scheduleReload() {
  if (reloadTimer) return;
  reloadTimer = setTimeout(() => {
    reloadTimer = null;
    loadFilms();
    loadNotFound();
  }, 5000);
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
    .then(async (res) => { if (!res.ok) throw new Error(await res.text()); })
    .catch((e) => logLine('error', 'Не удалось запустить обновление ' + id + ': ' + e.message))
    .finally(() => { btn.disabled = false; });
}

refreshAllBtn.addEventListener('click', () => {
  refreshAllBtn.disabled = true;
  logLine('info', 'Запущено массовое обновление всех записей из TMDB');
  fetch('/api/admin/refresh-all', { method: 'POST' })
    .then(async (res) => { if (!res.ok) throw new Error(await res.text()); })
    .catch((e) => { logLine('error', 'Не удалось запустить массовое обновление: ' + e.message); refreshAllBtn.disabled = false; });
});

reloadBtn.addEventListener('click', () => { if (currentTab === 'users') loadUsers(); else { loadFilms(); loadNotFound(); } });
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

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
const tabVPN = document.getElementById('tab-vpn');
const tabProxies = document.getElementById('tab-proxies');
const usersPanel = document.getElementById('users-panel');
const usersBody = document.getElementById('users-body');
const usersStatus = document.getElementById('users-status');
const vpnPanel = document.getElementById('vpn-panel');
const vpnForm = document.getElementById('vpn-form');
const vpnFile = document.getElementById('vpn-config');
const vpnSubmit = document.getElementById('vpn-submit');
const vpnName = document.getElementById('vpn-name');
const vpnUpdated = document.getElementById('vpn-updated');
const vpnState = document.getElementById('vpn-state');
const vpnStatusEl = document.getElementById('vpn-status');
const proxiesPanel = document.getElementById('proxies-panel');
const proxiesForm = document.getElementById('proxies-form');
const proxiesEnabled = document.getElementById('proxies-enabled');
const proxiesSources = document.getElementById('proxies-sources');
const proxiesStatic = document.getElementById('proxies-static');
const proxiesSave = document.getElementById('proxies-save');
const proxiesRefresh = document.getElementById('proxies-refresh');
const proxiesBody = document.getElementById('proxies-body');
const proxiesEmpty = document.getElementById('proxies-empty');
const proxiesStatusEl = document.getElementById('proxies-status');
const proxiesDirtyNote = document.getElementById('proxies-dirty');
const filmsLayout = document.getElementById('admin-films-layout');
const filmNote = document.getElementById('admin-film-note');
const notfoundWrap = document.getElementById('notfound-wrap');
const notfoundBody = document.getElementById('notfound-body');

let lastSeq = 0;
let currentTab = 'missing'; // 'missing' | 'notfound' | 'users' | 'vpn' | 'proxies'
let missingCount = 0;
let notFoundCount = 0;
let reloadTimer = null;
let adminUserID = 0;
let usersCount = 0;
let vpnConfigured = false;
let vpnSaving = false;
let vpnOperation = 0;
let proxiesData = null;
let proxiesLoaded = false;
let proxiesDirty = false;
let proxiesEditVersion = 0;
let proxiesRequest = null;
let proxiesPollTimer = null;
let proxiesAccessDenied = false;
let proxiesMessageOrigin = '';

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
    tabVPN.hidden = true;
    tabProxies.hidden = true;
    usersPanel.hidden = true;
    vpnPanel.hidden = true;
    proxiesPanel.hidden = true;
    proxiesAccessDenied = true;
    stopProxyPolling(true);
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
  tabVPN.addEventListener('click', () => { showTab('vpn'); loadVPN(); });
  tabProxies.addEventListener('click', () => showTab('proxies'));
  loadFilms();
  loadNotFound();
  pollLogs();
}

logoutBtn.addEventListener('click', async () => {
  proxiesAccessDenied = true;
  stopProxyPolling(true);
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
  const isVPN = name === 'vpn';
  const isProxies = name === 'proxies';
  filmsLayout.hidden = isUsers || isVPN || isProxies;
  usersPanel.hidden = !isUsers;
  vpnPanel.hidden = !isVPN;
  proxiesPanel.hidden = !isProxies;
  filmNote.hidden = isUsers || isVPN || isProxies;
  refreshAllBtn.hidden = isUsers || isVPN || isProxies;
  countEl.hidden = isVPN || isProxies;
  reloadBtn.textContent = isVPN || isProxies ? 'Обновить статус' : 'Обновить список';
  table.closest('.admin-table-wrap').hidden = !isMissing;
  notfoundWrap.hidden = name !== 'notfound';
  tabMissing.classList.toggle('active', isMissing);
  tabNotfound.classList.toggle('active', name === 'notfound');
  tabUsers.classList.toggle('active', isUsers);
  tabVPN.classList.toggle('active', isVPN);
  tabProxies.classList.toggle('active', isProxies);
  stopProxyPolling(true);
  if (isProxies && !document.hidden) loadProxies();
  updateCount();
}

function updateCount() {
  if (currentTab === 'vpn' || currentTab === 'proxies') {
    countEl.textContent = '';
  } else if (currentTab === 'users') {
    countEl.textContent = 'Пользователей: ' + usersCount;
  } else if (currentTab === 'notfound') {
    countEl.textContent = 'Не удалось обновить: ' + notFoundCount;
  } else {
    countEl.textContent = 'Записей с пустыми полями: ' + missingCount;
  }
}

// ---- Конфигурация VPN ----
function vpnStatus(text, kind = '') {
  vpnStatusEl.textContent = text;
  vpnStatusEl.className = 'vpn-status' + (kind ? ' ' + kind : '');
}

function updateVPNSubmit() {
  vpnFile.disabled = vpnSaving;
  vpnSubmit.disabled = vpnSaving || !vpnFile.files || !vpnFile.files.length;
  vpnSubmit.textContent = vpnSaving ? 'Применяем конфиг…'
    : vpnConfigured ? 'Заменить конфиг' : 'Загрузить и применить';
}

function renderVPN(data) {
  vpnConfigured = data.configured === true;
  vpnName.textContent = vpnConfigured ? (data.name || 'Конфиг загружен') : 'Конфиг не загружен';
  const date = data.updated_at ? new Date(data.updated_at) : null;
  vpnUpdated.textContent = date && !Number.isNaN(date.getTime()) ? date.toLocaleString('ru-RU') : '—';
  vpnState.textContent = data.status === 'ready' ? 'Конфиг применён'
    : data.status === 'error' ? 'Ошибка VPN' : vpnConfigured ? 'Конфиг сохранён' : 'Не настроен';
  updateVPNSubmit();
}

async function vpnResponseError(response) {
  const text = (await response.text()).trim();
  try {
    const data = JSON.parse(text);
    return typeof data.message === 'string' ? data.message
      : typeof data.error === 'string' ? data.error : 'Ошибка ' + response.status;
  } catch (error) { return text || 'Ошибка ' + response.status; }
}

async function loadVPN() {
  if (vpnSaving) return;
  const operation = ++vpnOperation;
  vpnStatus('Загружаем состояние VPN…');
  try {
    const response = await fetch('/api/admin/vpn', { cache: 'no-store' });
    if (!response.ok) throw new Error(await vpnResponseError(response));
    const data = await response.json();
    if (operation !== vpnOperation) return;
    renderVPN(data);
    vpnStatus(data.message || '', data.status === 'error' ? 'error' : '');
  } catch (error) {
    if (operation === vpnOperation) vpnStatus('Не удалось загрузить состояние VPN: ' + error.message, 'error');
  }
}

vpnFile.addEventListener('change', () => { vpnStatus(''); updateVPNSubmit(); });
vpnForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  if (vpnSaving) return;
  const file = vpnFile.files && vpnFile.files[0];
  if (!file) { vpnStatus('Выберите конфиг WireGuard (.conf).', 'error'); return; }
  if (!/\.conf$/i.test(file.name)) { vpnStatus('Выберите файл WireGuard с расширением .conf.', 'error'); return; }
  if (!file.size || file.size > 64 * 1024) { vpnStatus('Размер конфига должен быть от 1 байта до 64 КБ.', 'error'); return; }
  ++vpnOperation;
  vpnSaving = true;
  updateVPNSubmit();
  vpnStatus('Загружаем и применяем конфиг…');
  try {
    const form = new FormData();
    form.append('config', file);
    const response = await fetch('/api/admin/vpn', { method: 'PUT', body: form });
    if (!response.ok) throw new Error(await vpnResponseError(response));
    const data = await response.json();
    renderVPN(data);
    vpnFile.value = '';
    vpnStatus(data.message || (response.status === 202 ? 'Конфиг загружен и применяется.' : 'Конфиг загружен и применён.'), data.status === 'error' ? 'error' : 'success');
  } catch (error) { vpnStatus('Не удалось заменить конфиг: ' + error.message, 'error'); }
  finally { vpnSaving = false; updateVPNSubmit(); }
});

// ---- Резервные прокси ----
function proxyStatus(text, kind = '', origin = 'action') {
  proxiesStatusEl.textContent = text;
  proxiesStatusEl.className = 'proxy-status' + (kind ? ' ' + kind : '');
  proxiesMessageOrigin = origin;
}

function proxyDate(value) {
  const date = value ? new Date(value) : null;
  return date && !Number.isNaN(date.getTime()) ? date.toLocaleString('ru-RU') : '—';
}

function proxyNumber(value) {
  const n = Number(value);
  return Number.isFinite(n) && n >= 0 ? Math.floor(n) : 0;
}

function proxyPanelVisible() {
  return currentTab === 'proxies' && !proxiesPanel.hidden && !document.hidden && !proxiesAccessDenied;
}

function updateProxyControls() {
  const action = proxiesRequest && proxiesRequest.kind !== 'status';
  const disabled = !proxiesLoaded || proxiesAccessDenied || (proxiesRequest && proxiesRequest.kind === 'save');
  for (const input of [proxiesEnabled, proxiesSources, proxiesStatic]) input.disabled = !!disabled;
  proxiesSave.disabled = !proxiesLoaded || proxiesAccessDenied || !!action || !proxiesDirty;
  proxiesRefresh.disabled = !proxiesLoaded || proxiesAccessDenied || !!action || !!(proxiesData && proxiesData.running);
  proxiesSave.textContent = proxiesRequest && proxiesRequest.kind === 'save' ? 'Сохраняем…' : 'Сохранить настройки';
  proxiesRefresh.textContent = proxiesRequest && proxiesRequest.kind === 'refresh' ? 'Запускаем проверку…'
    : proxiesData && proxiesData.running ? 'Проверяем прокси…' : 'Найти и проверить сейчас';
  proxiesDirtyNote.hidden = !proxiesDirty;
}

function fillProxyForm(config) {
  proxiesEnabled.checked = !!config.enabled;
  proxiesSources.value = Array.isArray(config.sources) ? config.sources.join('\n') : '';
  proxiesStatic.value = Array.isArray(config.static) ? config.static.join('\n') : '';
}

function renderProxies(data) {
  if (!data || !data.config || typeof data.config !== 'object') throw new Error('Сервер вернул неполные настройки прокси.');
  proxiesData = data;
  proxiesLoaded = true;
  if (!proxiesDirty) fillProxyForm(data.config);
  const routes = { vpn: 'VPN (основной)', proxy: 'Резервные прокси', unavailable: 'Нет доступного маршрута' };
  document.getElementById('proxies-route').textContent = routes[data.active_route] || '—';
  document.getElementById('proxies-count').textContent = proxyNumber(data.available) + ' / ' + proxyNumber(data.candidates);
  document.getElementById('proxies-state').textContent = data.running ? 'Идёт поиск и проверка'
    : data.config.enabled ? 'Ожидает следующей проверки' : 'Резервные прокси выключены';
  document.getElementById('proxies-started').textContent = proxyDate(data.last_started_at);
  document.getElementById('proxies-finished').textContent = proxyDate(data.last_finished_at);
  document.getElementById('proxies-next').textContent = proxyDate(data.next_run_at);
  const seconds = proxyNumber(data.interval_seconds) || 300;
  const interval = seconds % 60 === 0 ? (seconds / 60) + ' мин.' : seconds + ' сек.';
  document.getElementById('proxies-interval').textContent = 'Автопроверка выполняется на сервере каждые ' + interval + ', даже если админка закрыта.';
  const lastError = document.getElementById('proxies-check-error');
  lastError.textContent = data.last_error ? 'Последняя проверка: ' + data.last_error : '';
  lastError.hidden = !data.last_error;
  proxiesBody.replaceChildren();
  const items = Array.isArray(data.items) ? data.items : [];
  for (const item of items) {
    const row = document.createElement('tr');
    const latency = item.latency_ms === null || item.latency_ms === '' ? NaN : Number(item.latency_ms);
    for (const text of [item.address || '—', Number.isFinite(latency) && latency >= 0 ? Math.round(latency) + ' мс' : '—',
      proxyDate(item.checked_at), item.source === 'static' || item.source === 'manual' ? 'Вручную' : item.source || '—']) {
      const cell = document.createElement('td');
      cell.textContent = text;
      row.appendChild(cell);
    }
    proxiesBody.appendChild(row);
  }
  proxiesEmpty.hidden = items.length > 0;
  proxiesEmpty.textContent = data.running ? 'Идёт поиск и проверка прокси…'
    : !data.config.enabled ? 'Резервные прокси выключены. Включите их в настройках, чтобы использовать резервный маршрут.'
    : data.candidates ? 'Доступных прокси не найдено. Проверьте источники или добавьте адреса вручную.'
    : 'Проверенных прокси пока нет. Добавьте источники или адреса и запустите проверку.';
  updateProxyControls();
}

function stopProxyPolling(cancelStatus = false) {
  if (proxiesPollTimer !== null) clearTimeout(proxiesPollTimer);
  proxiesPollTimer = null;
  if (cancelStatus && proxiesRequest && proxiesRequest.kind === 'status') {
    const request = proxiesRequest;
    proxiesRequest = null;
    clearTimeout(request.timeout);
    if (request.controller) request.controller.abort();
  }
}

function scheduleProxyPoll() {
  stopProxyPolling();
  if (!proxyPanelVisible() || proxiesRequest) return;
  proxiesPollTimer = setTimeout(() => {
    proxiesPollTimer = null;
    if (proxyPanelVisible()) loadProxies(true);
  }, proxiesData && proxiesData.running ? 2000 : 10000);
}

function beginProxyRequest(kind) {
  if (proxiesAccessDenied || (proxiesRequest && (kind === 'status' || proxiesRequest.kind !== 'status'))) return null;
  stopProxyPolling(kind !== 'status');
  const controller = typeof AbortController === 'function' ? new AbortController() : null;
  const request = { kind, controller, timeout: null, timedOut: false };
  request.timeout = setTimeout(() => { request.timedOut = true; if (controller) controller.abort(); }, 20000);
  proxiesRequest = request;
  updateProxyControls();
  return request;
}

function finishProxyRequest(request) {
  clearTimeout(request.timeout);
  if (proxiesRequest !== request) return;
  proxiesRequest = null;
  updateProxyControls();
  scheduleProxyPoll();
}

async function checkProxyResponse(response) {
  if (response.status === 401 || response.status === 403) {
    const error = new Error(response.status === 401 ? 'Сессия истекла. Войдите в аккаунт снова.' : 'Нет доступа. Требуется роль администратора.');
    error.accessDenied = true;
    throw error;
  }
  if (!response.ok) throw new Error(await vpnResponseError(response));
}

function proxyRequestError(request, error, prefix) {
  if (proxiesRequest !== request) return;
  if (error.accessDenied) { proxiesAccessDenied = true; stopProxyPolling(); }
  const message = request.timedOut ? 'Превышено время ожидания ответа сервера.' : error.message;
  proxyStatus(prefix + message, 'error', request.kind === 'status' ? 'load' : 'action');
}

async function loadProxies(quiet = false) {
  const request = beginProxyRequest('status');
  if (!request) return;
  if (!quiet) proxyStatus('Загружаем состояние прокси…', '', 'load');
  try {
    const response = await fetch('/api/admin/proxies', { cache: 'no-store', signal: request.controller && request.controller.signal });
    if (proxiesRequest !== request) return;
    await checkProxyResponse(response);
    const data = await response.json();
    if (proxiesRequest !== request) return;
    renderProxies(data);
    if (proxiesMessageOrigin === 'load') proxyStatus('', '', 'load');
  } catch (error) { proxyRequestError(request, error, 'Не удалось обновить состояние прокси: '); }
  finally { finishProxyRequest(request); }
}

function proxyLines(value) {
  return [...new Set(String(value || '').split(/\r?\n/).map(line => line.trim()).filter(Boolean))];
}

function proxyConfigFromForm() {
  const sources = proxyLines(proxiesSources.value), addresses = proxyLines(proxiesStatic.value);
  sources.forEach((source, index) => {
    let url;
    try { url = new URL(source); } catch (error) {}
    if (!url || url.protocol !== 'https:' || url.username || url.password || url.hash) {
      throw new Error('Источник ' + (index + 1) + ': укажите HTTPS-ссылку без логина и пароля.');
    }
  });
  addresses.forEach((address, index) => {
    let url;
    try { url = new URL(address.includes('://') ? address : 'http://' + address); } catch (error) {}
    if (!url || !['http:', 'https:'].includes(url.protocol) || !url.hostname || url.username || url.password ||
      (url.pathname && url.pathname !== '/') || url.search || url.hash) {
      throw new Error('Прокси ' + (index + 1) + ': укажите HTTP(S)-адрес IP:порт без логина и пароля.');
    }
  });
  return { enabled: !!proxiesEnabled.checked, sources, static: addresses };
}

function proxyFormChanged() {
  proxiesDirty = true;
  proxiesEditVersion++;
  updateProxyControls();
}
proxiesEnabled.addEventListener('change', proxyFormChanged);
proxiesSources.addEventListener('input', proxyFormChanged);
proxiesStatic.addEventListener('input', proxyFormChanged);

proxiesForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (!proxiesLoaded || proxiesAccessDenied || (proxiesRequest && proxiesRequest.kind !== 'status')) return;
  let config;
  try { config = proxyConfigFromForm(); }
  catch (error) { proxyStatus(error.message, 'error'); return; }
  const request = beginProxyRequest('save');
  if (!request) return;
  const editVersion = proxiesEditVersion;
  proxyStatus('Сохраняем настройки прокси…');
  let saved = false;
  try {
    const response = await fetch('/api/admin/proxies', { method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(config), signal: request.controller && request.controller.signal });
    if (proxiesRequest !== request) return;
    await checkProxyResponse(response);
    const data = response.status === 204 ? null : await response.json().catch(() => null);
    if (proxiesRequest !== request) return;
    if (editVersion === proxiesEditVersion) proxiesDirty = false;
    if (data && data.config) renderProxies(data);
    else {
      proxiesData = { ...(proxiesData || {}), config };
      if (!proxiesDirty) fillProxyForm(config);
    }
    proxyStatus('Настройки резервных прокси сохранены.', 'success');
    saved = true;
  } catch (error) { proxyRequestError(request, error, 'Не удалось сохранить настройки: '); }
  finally { finishProxyRequest(request); }
  if (saved && proxyPanelVisible()) await loadProxies(true);
});

proxiesRefresh.addEventListener('click', async () => {
  if (!proxiesLoaded || proxiesAccessDenied || (proxiesData && proxiesData.running)) return;
  const request = beginProxyRequest('refresh');
  if (!request) return;
  proxyStatus('Запускаем поиск и проверку прокси…');
  let started = false;
  try {
    const response = await fetch('/api/admin/proxies/refresh', { method: 'POST', signal: request.controller && request.controller.signal });
    if (proxiesRequest !== request) return;
    await checkProxyResponse(response);
    const data = response.status === 204 ? null : await response.json().catch(() => null);
    if (proxiesRequest !== request) return;
    if (data && data.config) renderProxies(data);
    else {
      proxiesData = { ...(proxiesData || {}), running: true };
      document.getElementById('proxies-state').textContent = 'Идёт поиск и проверка';
    }
    proxyStatus('Проверка запущена. Результаты появятся после завершения.', 'success');
    started = true;
  } catch (error) { proxyRequestError(request, error, 'Не удалось запустить проверку: '); }
  finally { finishProxyRequest(request); }
  if (started && proxyPanelVisible()) await loadProxies(true);
});

if (document.addEventListener) document.addEventListener('visibilitychange', () => {
  stopProxyPolling(true);
  if (proxyPanelVisible()) loadProxies(true);
});

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

reloadBtn.addEventListener('click', () => {
  if (currentTab === 'users') loadUsers();
  else if (currentTab === 'vpn') loadVPN();
  else if (currentTab === 'proxies') loadProxies();
  else { loadFilms(); loadNotFound(); }
});
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

'use strict';

/* Страница просмотра (/watch.html?id=…&magnet=…&file=…&pos=…): отдельная страница
 * для ФИЛЬМОВ. Сериалы играются прямо на карточке (/film.html) — там тот же плеер
 * (player.js), только без перехода на отдельную страницу. */

const wParams = new URLSearchParams(location.search);
const watchId = (wParams.get('id') || '').trim();
const magnet = wParams.get('magnet') || '';
const releaseTitle = wParams.get('rt') || '';
let wantSeason = parseInt(wParams.get('season') || '0', 10) || 0;
let wantEp = parseInt(wParams.get('ep') || '0', 10) || 0;
const wantVoice = wParams.get('voice') || '';
const startPos = wParams.has('pos') ? (parseFloat(wParams.get('pos')) || 0) : undefined;
// Файл (серия) в торренте: его выбрала страница фильма; -1 — авто (крупнейший видеофайл).
const startFile = wParams.get('file') !== null && wParams.get('file') !== '' ? parseInt(wParams.get('file'), 10) : -1;

const filmLink = document.getElementById('film-link');
const watchTitle = document.getElementById('watch-title');

function setFilmLink() {
  if (!filmLink || !watchId) return;
  const p = new URLSearchParams({ id: watchId });
  if (wantSeason) p.set('season', String(wantSeason));
  if (wantEp) p.set('ep', String(wantEp));
  if (wantVoice) p.set('voice', wantVoice);
  filmLink.href = '/film.html?' + p.toString();
}

function setWatchTitle() {
  if (!watchTitle) return;
  const name = currentItem ? dispTitle(currentItem) : watchId;
  const parts = [name];
  if (wantSeason > 0) {
    parts.push(t('seasonLabel') + ' ' + wantSeason + (wantEp > 0 ? ' · ' + t('episodeLabel') + ' ' + wantEp : ''));
  }
  watchTitle.textContent = parts.filter(Boolean).join(' — ');
  document.title = parts.filter(Boolean).join(' — ') + ' — Video Viewer';
}

// Адрес страницы держим в актуальном виде: перезагрузка (F5) откроет ту же серию.
function syncUrl() {
  if (!watchId) return;
  const p = new URLSearchParams(location.search);
  const st = PP.state();
  if (st.magnet) p.set('magnet', st.magnet);
  if (PP.release()) p.set('rt', PP.release());
  if (st.file >= 0) p.set('file', String(st.file)); else p.delete('file');
  if (st.season) p.set('season', String(st.season));
  if (st.episode) p.set('ep', String(st.episode));
  p.delete('pos'); // позиция уже учтена потоком — в адресе она только мешала бы
  try {
    history.replaceState(null, '', location.pathname + '?' + p.toString());
  } catch (e) { /* file:// или запрет history — не критично */ }
}

function onPlayerState(st) {
  if (st.season) wantSeason = st.season;
  if (st.episode) wantEp = st.episode;
  setFilmLink();
  setWatchTitle();
  syncUrl();
}

// Раздача кончилась: за следующей серией идём на карточку фильма — там подбор
// раздачи по сезону/озвучке и запуск (?autoplay=1).
function nextFromFilmPage() {
  if (!isSeriesKind(currentItem && currentItem.kind) || !wantSeason || !wantEp) {
    PP.showNote(t('endOfSeries'));
    return;
  }
  const want = seasonEpisodeCount(watchId, wantSeason);
  let nextSeason = wantSeason, nextEp = wantEp + 1;
  if (want && wantEp >= want) { nextSeason = wantSeason + 1; nextEp = 1; }
  dbg('серия: раздача кончилась — ищу S' + nextSeason + 'E' + nextEp + ' на карточке фильма');
  go('/film.html?id=' + encodeURIComponent(watchId)
    + '&season=' + nextSeason + '&ep=' + nextEp
    + (wantVoice ? '&voice=' + encodeURIComponent(wantVoice) : '')
    + '&autoplay=1');
}

// Escape — назад к карточке фильма (поток при этом гасится в pagehide).
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  go(filmLink ? filmLink.getAttribute('href') : '/');
});

async function initWatchPage() {
  await loadEpisodeHistory(watchId);
  PP.init({ onStateChange: onPlayerState, onReleaseEnd: nextFromFilmPage });
  if (!watchId || !magnet) {
    PP.showNote(t('noSources'));
    return;
  }
  currentItem = loadStoredItem(watchId) || { id: watchId, imdb_id: watchId.indexOf('tt') === 0 ? watchId : '' };
  // tmdb_id нужен для раскладки серий по сезонам (иначе серия «убегает» за границы сезона).
  if (watchId.indexOf('tt') === 0 && !currentItem.tmdb_id) {
    try {
      const r = await fetch('/api/films/' + encodeURIComponent(watchId));
      if (r.ok) {
        const f = await r.json();
        if (f && f.tmdb_id) currentItem.tmdb_id = f.tmdb_id;
        if (f && f.kind && !currentItem.kind) currentItem.kind = f.kind;
      }
    } catch (e) { /* без tmdb — сервер разложит серии приблизительно */ }
  }
  setFilmLink();
  setWatchTitle();
  // Сериал (переход по старой ссылке) — показываем список серий текущей раздачи.
  Personal.request('/api/films/' + encodeURIComponent(watchId) + '/explore')
    .then(data => PP.setTrailer(data.trailer || ''))
    .catch(() => {});
  if (isSeriesKind(currentItem.kind)) PP.setEpisodeList(document.getElementById('ep-list'));
  PP.start({
    id: watchId,
    magnet: magnet,
    release: releaseTitle,
    file: startFile,
    season: wantSeason,
    ep: wantEp,
    voice: wantVoice,
    pos: startPos,
  });
  if (DEBUG) showDebug();
}

onLang(() => {
  setWatchTitle();
  // Сам плеер перерисовывает подписи дорожек/субтитров/серий (PP.relabel).
});

applyLang();
initAuth().then(() => Personal.load()).catch(() => {}).then(initWatchPage);

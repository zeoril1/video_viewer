'use strict';

/* Плеер (HLS-поток раздачи): звуковые дорожки, субтитры, качество, перемотка,
 * серии текущей раздачи, сохранение прогресса. Используется и страницей фильма
 * (/film.html — сериалы играются прямо там, «все серии + просмотр»), и отдельной
 * страницей просмотра (/watch.html — фильмы).
 *
 * Требует на странице разметку плеера с теми же id, что в watch.html
 * (#player-wrap/#player/#player-error/#player-controls/#ctrl-*), а также
 * series.js (currentItem, fetchFiles, seasonEpisodeCount) и shared.js (t, VV).
 * Список серий раздачи выводится, только если страница передала PP.setEpisodeList(). */

const PP = (() => {
  const player = document.getElementById('player');
  const playerWrap = document.getElementById('player-wrap');
  const playerError = document.getElementById('player-error');
  const playerDebug = document.getElementById('player-debug');
  const ctrlPlay = document.getElementById('ctrl-play');
  const ctrlBar = document.getElementById('ctrl-bar');
  const ctrlBuffered = document.getElementById('ctrl-buffered');
  const ctrlPosition = document.getElementById('ctrl-position');
  const ctrlThumb = document.getElementById('ctrl-thumb');
  const ctrlTime = document.getElementById('ctrl-time');
  const ctrlMute = document.getElementById('ctrl-mute');
  const ctrlVolume = document.getElementById('ctrl-volume');
  const ctrlFullscreen = document.getElementById('ctrl-fullscreen');
  const ctrlPrev = document.getElementById('ctrl-prev');
  const ctrlNext = document.getElementById('ctrl-next');
  const ctrlSeekB = document.getElementById('ctrl-seek-b');
  const ctrlSeekF = document.getElementById('ctrl-seek-f');
  const ctrlMiniFill = document.getElementById('ctrl-mini-fill');
  const ctrlDebug = document.getElementById('ctrl-debug');
  const tracksEl = document.getElementById('tracks');
  const tracksTitle = document.getElementById('tracks-title');
  const tracksList = document.getElementById('tracks-list');
  const subsEl = document.getElementById('subs');
  const subsTitle = document.getElementById('subs-title');
  const subsList = document.getElementById('subs-list');
  const episodesEl = document.getElementById('episodes');
  const episodesTitle = document.getElementById('episodes-title');
  const epList = document.getElementById('ep-list');

  // Плеер работает, только если на странице есть вся его разметка (иначе страница
  // сама решает, что делать: film.js уводит сериал на /watch.html).
  const available = !!(player && playerWrap && playerError && ctrlPlay && ctrlBar &&
    ctrlTime && ctrlVolume && ctrlMute);
  const playbackSession = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, '0')).join('');

  // ---- Состояние ----
  let hlsPlayer = null;
  let currentPlay = null;      // { id, magnet } активной раздачи
  let releaseTitle = '';       // заголовок раздачи (уходит в /files — раскладка по сезонам TMDB)
  let totalDuration = 0;       // полная длительность файла (из /tracks)
  let durationFetch = { key: '', inflight: false };
  let streamStart = 0;         // смещение потока от начала файла (после перемотки)
  let currentTrack = 0;        // ordinal активной звуковой дорожки
  let currentSubs = -1;        // ordinal активной субтитр-дорожки (-1 — без субтитров)
  let currentSubtitles = [];   // субтитры активного файла (из /tracks)
  let currentQuality = 'source';
  let currentVideoCodec = '';
  let currentVideoHeight = 0;
  let h264FallbackDone = false; // автофолбэк H.265→H.264 — один раз на источник
  let pendingCodecNote = false;
  const maxStreamRestarts = 3; // перезапуск после простоя ffmpeg (>90 с → 404 на сегмент)
  let streamRestarts = 0;
  let autoVoice = '';          // выбранная озвучка: loadTracks включит её дорожку
  let lastTracksItems = [];
  let currentFile = -1;        // индекс файла (серии) в торренте; -1 — авто
  let lastFiles = [];          // файлы текущей раздачи
  let curSeason = 0;           // сезон/серия текущего файла (для URL, истории, прогресса)
  let curEpisode = 0;
  let autoNextFired = false;   // защита от повторного автоперехода
  let lastProgressSend = 0;
  let cacheKeepKey = '';       // раздача#серия, для которых уже запрошен тёплый кеш
  let noteTimer = null;
  let listEl = null;           // куда рисовать список серий раздачи (необязательно)
  let hooks = {};

  // ---- Мелкие помощники ----
  function showNote(text) {
    if (!playerError) return;
    if (!text) {
      playerError.hidden = true;
      playerError.textContent = '';
      return;
    }
    playerError.textContent = text;
    playerError.hidden = false;
    if (noteTimer) clearTimeout(noteTimer);
    noteTimer = setTimeout(() => { playerError.hidden = true; }, 2600);
  }

  function state() {
    return {
      id: currentPlay ? currentPlay.id : '',
      magnet: currentPlay ? currentPlay.magnet : '',
      file: currentFile,
      season: curSeason,
      episode: curEpisode,
      playing: !!hlsPlayer && !playerWrap.hidden,
    };
  }

  function notify() {
    if (hooks.onStateChange) {
      try { hooks.onStateChange(state()); } catch (e) { dbg('player hook: ' + e.message); }
    }
  }

  // ---- Прогресс просмотра ----
  // Отправка позиции в историю (не чаще раза в 5 с; final=true — принудительно).
  function maybeSaveProgress(final, beacon) {
    if (!VV.user || !currentPlay) return null;
    const now = Date.now();
    if (!final && now - lastProgressSend < 5000) return null;
    const pos = Math.round(absTime());
    if (pos < 5) return null; // не сохраняем случайные клики в самом начале
    // Просмотрено >5% — просим stream держать раздачу 24 ч (другие зрители той же озвучки скачают без повторов).
    if (totalDuration > 0 && pos / totalDuration > 0.05) keepStreamCache(true);
    lastProgressSend = now;
    const ep = currentFile >= 0 ? (lastFiles.find((f) => f.index === currentFile) || {}) : {};
    // Серия «убежала» за границы сезона (раздачу открывали без tmdb) — считаем номер сквозным,
    // как «088 seriya» в сборнике, и переводим в канонические сезон/серию.
    let sea = ep.season || curSeason || 0;
    let num = ep.episode || curEpisode || 0;
    const want = seasonEpisodeCount(currentPlay.id, sea);
    if (want && num > want) {
      const canon = canonicalByNumber(currentPlay.id, num);
      sea = canon ? canon.season : 0;
      num = canon ? canon.episode : 0;
    }
    const body = {
      film_id: currentPlay.id,
      magnet: currentPlay.magnet || '',
      file: currentFile >= 0 ? currentFile : -1,
      season: sea,
      episode: num,
      position: pos,
      duration: Math.round(totalDuration || 0),
    };
    return fetch('/api/history/progress', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      keepalive: !!beacon,
    }).catch(() => {});
  }

  // Тёплый кеш (24 ч) для текущей раздачи — не чаще раза на (раздача, серия).
  function keepStreamCache() {
    if (!currentPlay || !currentPlay.magnet) return;
    const key = currentPlay.magnet + '#' + currentFile;
    if (key === cacheKeepKey) return;
    cacheKeepKey = key;
    let q = 'magnet=' + encodeURIComponent(currentPlay.magnet);
    if (currentFile >= 0) q += '&file=' + currentFile;
    // keepalive — досылаем даже если вкладку закрывают сразу после события.
    fetch('/api/stream/keep?' + q, { method: 'POST', keepalive: true }).catch(() => {});
  }

  // ---- Запуск потока ----

  // URL манифеста для лога: magnet заменяем на info_hash, чтобы строка читалась.
  function hlsLogSrc(src) {
    return src.replace(/magnet=([^&]*)/, (all, val) => {
      let v = val;
      try { v = decodeURIComponent(val); } catch (e) { /* логируем как есть */ }
      const h = (v.match(/btih:([0-9a-fA-F]{40})/) || [])[1];
      return 'magnet=' + encodeURIComponent(h ? 'magnet:?xt=urn:btih:' + h : 'magnet');
    });
  }

  // Запуск HLS-потока файла: дорожка track, смещение start, качество
  // (source|2160|1080|720|480), субтитр subs (-1 — без субтитров). Смена дорожки/качества/
  // субтитра перезапускает ffmpeg; вкл/выкл текущей дорожки делает hls.js (subtitleTrack).
  function playHls(id, magnetSrc, file, track, start, quality, subs) {
    if (hlsPlayer) {
      hlsPlayer.destroy();
      hlsPlayer = null;
    }
    player.pause();
    player.removeAttribute('src');
    player.load();
    playerError.hidden = true;
    // Автофолбэк H.265→H.264: уведомление держим до старта потока (перекодирование не мгновенно).
    const keepCodecNote = pendingCodecNote;
    pendingCodecNote = false;
    if (keepCodecNote) {
      playerError.textContent = t('codecFallback');
      playerError.hidden = false;
    }
    // Плеер показываем при любом запуске — иначе после ошибки он останется скрытым.
    playerWrap.hidden = false;

    currentTrack = track || 0;
    currentSubs = (typeof subs === 'number' && subs >= 0) ? subs : -1;
    currentQuality = quality || 'source';
    streamStart = start || 0;
    currentFile = (typeof file === 'number' && file >= 0) ? file : -1;
    autoNextFired = false; // новый поток — автопереход можно снова
    updateQualityButtons();
    // Длительность запрашиваем при старте с любой позиции — иначе шкала/время её не покажут.
    fetchDuration(id, magnetSrc, currentFile);

    const p = new URLSearchParams({ magnet: magnetSrc });
    p.set('session', playbackSession);
    p.set('track', String(currentTrack));
    if (currentFile >= 0) p.set('file', String(currentFile));
    if (currentSubs >= 0) p.set('subs', String(currentSubs));
    if (streamStart > 0) p.set('start', String(streamStart));
    if (currentQuality !== 'source') p.set('quality', currentQuality);
    const src = `/api/films/${encodeURIComponent(id)}/hls.m3u8?` + p.toString();
    dbg('playHls: id=' + id + ' file=' + currentFile + ' track=' + currentTrack + ' subs=' + currentSubs + ' start=' + streamStart + ' q=' + currentQuality);

    if (window.Hls && Hls.isSupported()) {
      const hlsConfig = {
        liveDurationInfinity: true,
        debug: DEBUG,
        manifestLoadingTimeOut: 60000,
        manifestLoadingMaxRetry: 2,
        manifestLoadingRetryDelay: 2000,
        manifestLoadingMaxRetryTimeout: 60000,
        levelLoadingTimeOut: 30000,
        levelLoadingMaxRetry: 4,
        levelLoadingRetryDelay: 2000,
        levelLoadingMaxRetryTimeout: 60000,
        fragLoadingTimeOut: 20000,
        fragLoadingMaxRetry: 6,
        fragLoadingRetryDelay: 1500,
        fragLoadingMaxRetryTimeout: 90000,
        // Поток — прогрессивный файл БЕЗ #EXT-X-ENDLIST, поэтому hls.js считает его «живым»:
        // ставит позицию у края буфера и делает «догоняющий» seek вперёд, из-за чего видео
        // «перепрыгивает» от точки старта/перемотки. Для нас это VOD — живую синхронизацию
        // отключаем: liveSyncDurationCount большой (старт = начало потока),
        // liveMaxLatencyDurationCount=Infinity, maxLiveSyncPlaybackRate=1 (не ускорять).
        liveSyncDurationCount: 100,
        liveMaxLatencyDurationCount: Infinity,
        maxLiveSyncPlaybackRate: 1,
      };
      dbg('hls.js: создаём поток v' + (Hls.version || '?') + ' [' + hlsLogSrc(src) + ']'
        + ' | live=' + hlsConfig.liveDurationInfinity
        + ' manifestTO=' + hlsConfig.manifestLoadingTimeOut
        + ' levelTO=' + hlsConfig.levelLoadingTimeOut
        + ' fragTO=' + hlsConfig.fragLoadingTimeOut
        + ' liveSync=' + hlsConfig.liveSyncDurationCount + '/' + hlsConfig.liveMaxLatencyDurationCount
        + ' | плеер readyState=' + player.readyState + ' t=' + Math.round(player.currentTime || 0));
      const hls = new Hls(hlsConfig);
      hlsPlayer = hls;
      hls.loadSource(src);
      hls.attachMedia(player);
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        // Игнорируем событие заменённого плеера: старый HEVC-манифест скрыл бы уведомление.
        if (hlsPlayer !== hls) return;
        streamRestarts = 0; // поток успешно стартовал — свежие попытки перезапуска
        dbg('hls: манифест получен (уровней: ' + (hls.levels ? hls.levels.length : 0) + ' субтитров: ' + (hls.subtitleTracks ? hls.subtitleTracks.length : 0) + ')');
        // Субтитры включаем, если поток запущен с выбранной дорожкой и hls.js видит SUBTITLES.
        if (hls.subtitleTracks) {
          hls.subtitleTrack = (currentSubs >= 0 && hls.subtitleTracks.length) ? 0 : -1;
          hls.subtitleDisplay = true;
        }
        playerError.hidden = true; // поток стартовал — убираем уведомление о перекодировании
        player.play().catch((err) => dbg('player.play(): ' + err));
      });
      hls.on(Hls.Events.LEVEL_SWITCHED, (_e, d) => dbg('hls: уровень переключён: ' + d.level));
      hls.on(Hls.Events.FRAG_BUFFERED, (_e, d) => dbg('hls: фрагмент ' + d.frag.sn + ' @ ' + Math.round(d.frag.start) + 's'));
      hls.on(Hls.Events.ERROR, (_evt, data) => {
        // Устаревший mediaError от HEVC-потока не должен перекрывать работающий H.264-фолбэк.
        if (hlsPlayer !== hls) return;
        const resp = data && data.response ? ' (http ' + data.response.code + ')' : '';
        dbg('hls: ERROR type=' + (data && data.type) + ' details=' + (data && data.details) + resp);
        // Фатальная ошибка: на сырой поток не фолбэчим — там AC3/DTS, звука не будет.
        // Ошибку показываем, НО плеер не скрываем: селекторы дорожек/качества остаются
        // доступными — можно выбрать другую дорожку или вариант с H.264.
        if (data && data.fatal) {
          dbg('hls: ФАТАЛЬНАЯ ошибка, воспроизведение остановлено');
          // Автофолбэк: кодек (H.265/HEVC) не поддержан браузером — вместо ошибки перезапускаем
          // поток с перекодированием в H.264 (1080p); один раз на источник.
          if (data.type === 'mediaError' && currentQuality === 'source' && !h264FallbackDone && currentPlay) {
            h264FallbackDone = true;
            dbg('hls: H.265/HEVC — автофолбэк на H.264 (1080p)');
            if (hlsPlayer === hls) hlsPlayer = null;
            hls.destroy();
            pendingCodecNote = true;
            playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, streamStart, '1080', currentSubs);
            return;
          }
          // Простой >90с (пауза/свёрнутая вкладка) — cleanup убил ffmpeg-сессию и файлы, hls.js
          // получает 404 на сегмент: вместо фатальной ошибки прозрачно перезапускаем поток с текущей
          // позиции (servePlaylist поднимет ffmpeg заново, при необходимости создаст новый поток).
          if (data.type === 'networkError' && data.response && data.response.code === 404
              && currentPlay && streamRestarts < maxStreamRestarts) {
            streamRestarts++;
            let restartPos = Math.floor(streamStart + (player.currentTime || 0));
            if (!isFinite(restartPos) || restartPos < 0) restartPos = 0;
            dbg('hls: сессия ffmpeg остановлена по простою — перезапуск с ' + restartPos + 's (попытка ' + streamRestarts + '/' + maxStreamRestarts + ')');
            if (hlsPlayer === hls) hlsPlayer = null;
            hls.destroy();
            playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, restartPos, currentQuality, currentSubs);
            return;
          }
          if (hlsPlayer === hls) hlsPlayer = null;
          hls.destroy();
          const detail = [data.type, data.details].filter(Boolean).join(' / ') + resp;
          // Показываем и технические детали ошибки — видно реальную причину (кодек, 502, таймаут).
          playerError.textContent = t('playbackError') + (detail ? ' — ' + detail : '');
          playerError.hidden = false;
          showDebug();
          // Сообщение идёт перед селекторами дорожек — доскроллим до него.
          if (typeof playerError.scrollIntoView === 'function') {
            playerError.scrollIntoView({ block: 'nearest' });
          }
        }
      });
    } else if (player.canPlayType && player.canPlayType('application/vnd.apple.mpegurl')) {
      // Нативные HLS (Safari).
      dbg('HLS: нативный (Safari)');
      player.src = src;
      player.play().catch(() => {});
    } else {
      // HLS не поддерживается браузером — сырой поток (без транскодинга).
      dbg('HLS: не поддерживается — сырой поток');
      const qs = new URLSearchParams({ magnet: magnetSrc });
      if (currentFile >= 0) qs.set('file', String(currentFile));
      player.src = `/api/stream/${encodeURIComponent(id)}?` + qs.toString();
      player.play().catch(() => {});
    }
  }

  // Полная длительность файла (/tracks): при просмотре с любой позиции шкала и время
  // показывают общую длительность; повторный запрос для того же файла не идёт.
  async function fetchDuration(id, magnetSrc, file) {
    const key = id + '|' + (magnetSrc || '') + '|' + (typeof file === 'number' ? file : -1);
    if (durationFetch.key === key && durationFetch.inflight) return; // уже идёт
    if (durationFetch.key === key && !durationFetch.inflight && totalDuration > 0) return; // уже известна
    durationFetch.key = key;
    durationFetch.inflight = true;
    try {
      const q = new URLSearchParams({ magnet: magnetSrc });
      if (typeof file === 'number' && file >= 0) q.set('file', String(file));
      const ctrl = new AbortController();
      const timer = setTimeout(() => ctrl.abort(), 30000);
      let res;
      try {
        res = await fetch(`/api/films/${encodeURIComponent(id)}/tracks?` + q.toString(), { signal: ctrl.signal });
      } finally {
        clearTimeout(timer);
      }
      if (!res.ok) throw new Error('HTTP ' + res.status);
      const data = await res.json();
      if (data.duration > 0) {
        totalDuration = data.duration;
        updatePlayerUI();
        dbg('tracks: длительность ' + id + ' = ' + data.duration + 's');
      }
    } catch (e) {
      durationFetch.key = '';
      dbg('tracks: длительность ' + id + ': ' + e.message);
    } finally {
      durationFetch.inflight = false;
    }
  }

  // Дорожки/субтитры/длительность текущего файла; включает выбранную озвучку (autoVoice).
  async function loadTracks(id, magnetSrc, file) {
    // Заявляем ключ длительности, чтобы fetchDuration не дублировал этот же запрос.
    durationFetch.key = id + '|' + (magnetSrc || '') + '|' + (typeof file === 'number' ? file : -1);
    durationFetch.inflight = true;
    tracksEl.hidden = false;
    tracksList.innerHTML = '';
    tracksTitle.textContent = t('tracksLoading');
    try {
      const q = new URLSearchParams({ magnet: magnetSrc });
      if (typeof file === 'number' && file >= 0) q.set('file', String(file));
      // Таймаут чуть больше серверного (ffprobe ждёт данные до ~90с), чтобы запрос не висел вечно.
      const ctrl = new AbortController();
      const timer = setTimeout(() => ctrl.abort(), 100000);
      let res;
      try {
        res = await fetch(`/api/films/${encodeURIComponent(id)}/tracks?` + q.toString(), { signal: ctrl.signal });
      } finally {
        clearTimeout(timer);
      }
      if (!res.ok) throw new Error('HTTP ' + res.status);
      const data = await res.json();
      totalDuration = data.duration || 0;
      updatePlayerUI();
      // Запрос длительности завершён — fetchDuration в playHls больше не нужен.
      durationFetch.inflight = false;
      currentVideoCodec = (data.codec || '').toLowerCase();
      // По реальному разрешению показываем только доступные кнопки качества (1080p не «повысить» до 4K).
      currentVideoHeight = data.height || 0;
      updateQualityButtons();
      const items = data.items || [];
      lastTracksItems = items;
      const subtitles = data.subtitles || [];
      dbg('tracks: id=' + id + ' duration=' + (data.duration || '?') + 's дорожек=' + items.length + ' субтитров=' + subtitles.length + ' video=' + currentVideoCodec + '/' + currentVideoHeight + 'p');
      // Субтитры рендерим ДО раннего возврата по аудио-дорожкам — иначе при одной
      // (или нулевой) звуковой дорожке блок субтитров не появился бы.
      currentSubtitles = subtitles;
      renderSubtitles(subtitles, id, magnetSrc);
      // track — ПОРЯДКОВЫЙ номер аудио (ordinal), а не index потока: у MKV index=0 это
      // видео, и track=0 дал бы два видеопотока без звука → bufferAppendError. Дефолт —
      // первый аудио; аудио нет — track=-1 (video-only).
      if (items.length === 0) {
        currentTrack = -1;
      } else if (!items.some((tr) => tr.ordinal === currentTrack)) {
        currentTrack = items[0].ordinal;
      }
      // Выбранная озвучка (autoVoice): включаем её дорожку, перезапуская поток.
      const av = autoVoice;
      autoVoice = '';
      if (av && items.length > 1) {
        const vo = matchVoiceOrdinal(items, av);
        if (vo != null && vo !== currentTrack && currentPlay) {
          currentTrack = vo;
          dbg('tracks: авто-озвучка «' + av + '» -> дорожка #' + vo);
          renderTracks(items, id, magnetSrc);
          playHls(currentPlay.id, currentPlay.magnet, currentFile, vo, streamStart, currentQuality, currentSubs);
          return;
        }
      }
      // HEVC/H.265 через MSE не играет — в исходном качестве сразу фолбэчим на H.264 (1080p).
      if (isHevcCodec(currentVideoCodec) && currentQuality === 'source' && !h264FallbackDone && currentPlay) {
        h264FallbackDone = true;
        dbg('tracks: H.265/HEVC — автофолбэк на H.264 (1080p)');
        pendingCodecNote = true;
        playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, streamStart, '1080', currentSubs);
      }
      if (items.length <= 1) {
        tracksEl.hidden = true;
        return;
      }
      renderTracks(items, id, magnetSrc);
    } catch (e) {
      dbg('tracks: ошибка ' + id + ': ' + e.message);
      // Длительность не получена — ключ сбрасываем, следующий старт запросит её заново.
      durationFetch.inflight = false;
      durationFetch.key = '';
      currentSubtitles = [];
      lastTracksItems = [];
      subsEl.hidden = true;
      subsList.innerHTML = '';
      // Явно показываем, что дорожки недоступны (нет пиров), — плеер не выглядит зависшим,
      // и есть подсказка выбрать другой вариант.
      tracksEl.hidden = false;
      tracksTitle.textContent = t('audioTracks');
      tracksList.innerHTML = '<div class="tracks-note">' + t('tracksUnavailable') + '</div>';
    }
  }

  // Тип перевода в начале заголовка дорожки («Дубляж Red Head Sound», «MVO LostFilm»,
  // «AVO Юрий Сербин», «Original») → короткая пометка и студия.
  function splitAudioTrackTitle(title) {
    const raw = (title || '').trim();
    const m = raw.match(/^(дубляж|дублирован|продубляж|dub|mvo|многоголос\w*|multi|avo|vo|авторск\w*|одноголос\w*|двухголос\w*|2vo|two|original|оригинал\w*|ost)\s*[:\-–|]?\s*(.*)$/i);
    if (!m) return { type: '', studio: raw };
    const head = m[1].toLowerCase();
    let type = '';
    if (head === 'dub' || head.indexOf('дубляж') === 0 || head.indexOf('дублирован') === 0 || head.indexOf('продубляж') === 0) type = 'д';
    else if (head === 'mvo' || head === 'multi' || head.indexOf('многоголос') === 0) type = 'М';
    else if (head === 'avo' || head === 'vo' || head.indexOf('авторск') === 0 || head.indexOf('одноголос') === 0) type = 'а';
    else if (head === 'two' || head === '2vo' || head.indexOf('двухголос') === 0) type = '2';
    else if (head === 'original' || head === 'ost' || head.indexOf('оригинал') === 0) type = 'orig';
    // Студия: без пояснения после «/» (дубль уже выведенного типа) и лишней пунктуации.
    const studio = (m[2] || '').split(/\s*\/\s*/)[0].replace(/^[\s(:\-–|]+/, '').replace(/[)\s]+$/, '');
    return { type, studio };
  }

  // Подпись дорожки: «Студия (тип) ЯЗЫК» — «Red Head Sound (д) RUS», «Оригинал ENG».
  function trackButtonLabel(tr) {
    const { type, studio } = splitAudioTrackTitle(tr.title);
    const lang = (tr.language || '').toUpperCase();
    let label;
    if (type === 'orig') {
      label = t('audioOriginal'); // «Оригинал»
    } else {
      const name = studio || (t('trackFallback') + ' ' + ((tr.ordinal != null ? tr.ordinal : 0) + 1));
      label = type ? name + ' (' + type + ')' : name;
    }
    return lang ? label + ' ' + lang : label;
  }

  function renderTracks(items, id, magnetSrc) {
    tracksTitle.textContent = t('audioTracks');
    tracksList.innerHTML = '';
    const seen = {}; // одинаковые переводы (например TrueHD и его AC3-core) не дублируем
    items.forEach((tr) => {
      const label = trackButtonLabel(tr);
      if (label in seen) return; // тот же перевод (студия/тип/язык) — пропускаем
      seen[label] = true;
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'track-btn' + (tr.ordinal === currentTrack ? ' active' : '');
      btn.textContent = label;
      btn.addEventListener('click', () => {
        document.querySelectorAll('.track-btn').forEach((b) => b.classList.remove('active'));
        btn.classList.add('active');
        // Смена дорожки сохраняет позицию, серию, качество и субтитры (ordinal аудио).
        playHls(id, magnetSrc, currentFile, tr.ordinal, streamStart, currentQuality, currentSubs);
      });
      tracksList.appendChild(btn);
    });
  }

  // Кнопки субтитров: «Выкл» + дорожки файла. Выбор другой перезапускает поток с subs
  // (сервер добавляет её в HLS как WebVTT, hls.js показывает по subtitleTrack).
  function renderSubtitles(items, id, magnetSrc) {
    if (!items.length) {
      subsEl.hidden = true;
      return;
    }
    subsEl.hidden = false;
    subsTitle.textContent = t('subtitles');
    subsList.innerHTML = '';
    const mkBtn = (ordinal, label) => {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'sub-btn' + (ordinal === currentSubs ? ' active' : '');
      btn.textContent = label;
      btn.addEventListener('click', () => {
        if (ordinal === currentSubs) return;
        subsSelect(id, magnetSrc, ordinal);
      });
      return btn;
    };
    subsList.appendChild(mkBtn(-1, t('subsOff')));
    items.forEach((tr) => {
      const lang = tr.language ? tr.language.toUpperCase() : '';
      const label = [tr.title, lang].filter(Boolean).join(' · ') || (t('subsFallback') + ' ' + (tr.ordinal + 1));
      subsList.appendChild(mkBtn(tr.ordinal, label));
    });
  }

  // Выбор субтитр-дорожки (ordinal; -1 — выкл) с сохранением позиции/дорожки/качества.
  function subsSelect(id, magnetSrc, subs) {
    if (!currentPlay || subs === currentSubs) return;
    dbg('субтитры: ' + id + ' -> ' + (subs >= 0 ? subs : 'выкл'));
    currentSubs = subs;
    renderSubtitles(currentSubtitles, id, magnetSrc);
    playHls(id, magnetSrc, currentFile, currentTrack, streamStart, currentQuality, subs);
  }

  // ---- Серии текущей раздачи ----

  // Файлы раздачи: prev/next, список серий и (через хук onFiles) сетка на странице.
  async function loadFiles() {
    if (!currentPlay) return;
    const files = await fetchFiles(currentPlay.id, currentPlay.magnet, releaseTitle);
    lastFiles = files || [];
    dbg('files: файлов=' + lastFiles.length + ' (id=' + currentPlay.id + ')');
    if (hooks.onFiles) {
      try { hooks.onFiles(lastFiles, state()); } catch (e) { dbg('files hook: ' + e.message); }
    }
    renderEpisodeList();
  }

  // Список серий раздачи (только если страница передала элемент через setEpisodeList).
  function renderEpisodeList() {
    if (!episodesEl || !listEl) return;
    const series = isSeriesKind(currentItem && currentItem.kind);
    const multi = series && lastFiles.length > 1;
    episodesEl.hidden = !multi;
    listEl.innerHTML = '';
    if (!multi) return;
    if (episodesTitle) episodesTitle.textContent = t('episodesTitle');
    lastFiles.forEach((f) => {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'ep-btn' + (f.index === currentFile ? ' active' : '');
      const label = (f.season || 0) > 0 && (f.episode || 0) > 0
        ? 'S' + f.season + 'E' + f.episode
        : (f.name || '#' + f.index);
      btn.textContent = label;
      btn.title = f.name || '';
      btn.addEventListener('click', () => selectEpisode(f.index));
      listEl.appendChild(btn);
    });
  }

  // Запуск указанной серии текущей раздачи (prev/next, список серий, автопереход).
  function selectEpisode(index) {
    if (!currentPlay || currentFile === index) return;
    currentFile = index;
    streamStart = 0;
    currentTrack = 0;
    // Субтитры новой серии могут отличаться — сброс (селектор перерисует loadTracks).
    currentSubs = -1;
    // Старая длительность могла бы спровоцировать преждевременный автопереход.
    totalDuration = 0;
    // Кеш длительности привязан к файлу — новый запрос обязателен.
    durationFetch = { key: '', inflight: false };
    const sel = lastFiles.find((f) => f.index === index);
    if (sel && (sel.season || 0) > 0) curSeason = sel.season;
    if (sel && (sel.episode || 0) > 0) curEpisode = sel.episode;
    h264FallbackDone = false;
    autoNextFired = false;
    renderEpisodeList();
    notify();
    dbg('серия: файл #' + index);
    loadTracks(currentPlay.id, currentPlay.magnet, currentFile);
    playHls(currentPlay.id, currentPlay.magnet, currentFile, 0, 0, currentQuality);
  }

  // Соседний файл серии в текущем источнике (dir=1 — следующая, -1 — предыдущая).
  function episodeNeighbor(dir) {
    if (!Array.isArray(lastFiles) || lastFiles.length === 0) return null;
    const idx = lastFiles.findIndex((f) => f.index === currentFile);
    if (idx < 0) return dir > 0 ? lastFiles[0] : lastFiles[lastFiles.length - 1];
    return lastFiles[idx + dir] || null;
  }

  // Следующая/предыдущая серия; auto=true — автопереход: раздача кончилась — спрашиваем
  // страницу (film.js ищет следующую серию в других раздачах, watch.js уводит на карточку).
  function playNeighbor(dir, auto) {
    if (!currentPlay) return;
    const next = episodeNeighbor(dir);
    if (next) {
      dbg('серия: ' + (dir > 0 ? 'следующая' : 'предыдущая') + ' -> файл #' + next.index);
      selectEpisode(next.index);
      return;
    }
    if (dir > 0) {
      if (auto && hooks.onReleaseEnd) {
        maybeSaveProgress(true, true);
        hooks.onReleaseEnd(true);
        return;
      }
      showNote(isSeriesKind(currentItem && currentItem.kind) ? t('endOfSeason') : t('endOfEpisodes'));
    } else {
      showNote(t('prevEpisode'));
    }
  }

  function updateEpisodeButtons() {
    if (!ctrlPrev || !ctrlNext) return;
    const series = isSeriesKind(currentItem && currentItem.kind);
    const multi = series && Array.isArray(lastFiles) && lastFiles.length > 1;
    ctrlPrev.hidden = ctrlNext.hidden = !multi;
    if (!multi) return;
    const idx = lastFiles.findIndex((f) => f.index === currentFile);
    ctrlPrev.disabled = idx <= 0;
    ctrlNext.disabled = idx < 0 || idx >= lastFiles.length - 1;
  }

  // ---- Контролы плеера ----

  function bufferedEnd() {
    try {
      const b = player.buffered;
      return b.length ? b.end(b.length - 1) : 0;
    } catch (e) {
      return 0;
    }
  }

  // Абсолютная позиция: streamStart + currentTime (при перемотке ffmpeg стартует с позиции).
  function absTime() {
    return streamStart + (player.currentTime || 0);
  }

  function absBufEnd() {
    return streamStart + bufferedEnd();
  }

  function updatePlayerUI() {
    const dur = totalDuration || streamStart + (player.duration || 0);
    const pos = absTime();
    const bufEnd = absBufEnd();
    const pctPos = dur ? Math.min(100, Math.max(0, (pos / dur) * 100)) : 0;
    const pctBuf = dur ? Math.min(100, Math.max(0, (bufEnd / dur) * 100)) : 0;
    ctrlPosition.style.width = pctPos + '%';
    ctrlBuffered.style.width = pctBuf + '%';
    ctrlThumb.style.left = pctPos + '%';
    if (ctrlMiniFill) ctrlMiniFill.style.width = pctPos + '%';
    ctrlTime.textContent = fmtTime(pos) + ' / ' + fmtTime(dur);
    ctrlPlay.textContent = player.paused ? '▶' : '⏸';
    ctrlVolume.value = player.volume;
    ctrlMute.textContent = player.muted || player.volume === 0 ? '🔇' : '🔊';
    updateEpisodeButtons();
    // Автопереход: поток прогрессивный, ended не приходит — конец отслеживаем по позиции.
    if (!player.paused && totalDuration > 0 && pos >= totalDuration - 1.5 && !autoNextFired) {
      autoNextFired = true;
      playNeighbor(1, true);
    }
    maybeSaveProgress(false);
  }

  // Клик по полосе — перемотка: внутри [streamStart, буфер] двигаем currentTime,
  // иначе (раньше streamStart или за переданной границей) перезапускаем ffmpeg с позиции.
  if (available && ctrlBar) {
    ctrlBar.addEventListener('click', (e) => {
      const rect = ctrlBar.getBoundingClientRect();
      const frac = (e.clientX - rect.left) / rect.width;
      const dur = totalDuration || streamStart + (player.duration || 0);
      const target = Math.max(0, Math.min(dur, frac * dur));
      if (target >= streamStart && target <= absBufEnd() + 5) {
        player.currentTime = target - streamStart;
      } else if (isFinite(target) && currentPlay) {
        seekTo(target);
      }
      updatePlayerUI();
    });
  }

  // Перезапуск HLS-потока с указанной позиции (даже если ffmpeg ещё не дошёл до неё).
  function seekTo(target) {
    // Защита от Infinity/NaN (когда длительность не получена) — иначе ffmpeg получит -ss +Inf.
    if (!currentPlay || !isFinite(target) || target < 0) return;
    dbg('seek: перезапуск потока с ' + target + 's');
    streamStart = target;
    playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, target, currentQuality, currentSubs);
  }

  // Перемотка на delta секунд: внутри переданных данных двигаем currentTime, иначе перезапуск ffmpeg.
  function seekBy(delta) {
    if (!currentPlay || playerWrap.hidden) return;
    const dur = totalDuration || streamStart + (player.duration || 0);
    const target = Math.max(0, isFinite(dur) ? Math.min(dur, absTime() + delta) : absTime() + delta);
    if (target >= streamStart && target <= absBufEnd() + 5) {
      player.currentTime = target - streamStart;
    } else if (isFinite(target)) {
      seekTo(target);
    }
    updatePlayerUI();
  }

  function toggleFullscreen() {
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => {});
    } else if (playerWrap.requestFullscreen) {
      playerWrap.requestFullscreen().catch(() => {});
    }
  }

  function qualityHeightNum(q) {
    return { '2160': 2160, '1080': 1080, '720': 720, '480': 480 }[q] || 0;
  }

  // Отображает качество и скрывает недоступные кнопки: выше исходного разрешения не поднять
  // (высота неизвестна (0) — показываем все).
  function updateQualityButtons() {
    document.querySelectorAll('#ctrl-quality .q-btn').forEach((b) => {
      const q = b.dataset.quality;
      const availableQ = q === 'source' || currentVideoHeight <= 0 || qualityHeightNum(q) <= currentVideoHeight;
      b.hidden = !availableQ;
      b.classList.toggle('active', availableQ && q === currentQuality);
    });
  }

  // Всплывающая панель управления в полном экране: показывается на движение мыши.
  let controlsTimer = null;

  function showControls() {
    playerWrap.classList.add('controls-visible');
    playerWrap.classList.remove('controls-hidden');
    if (controlsTimer) clearTimeout(controlsTimer);
    controlsTimer = setTimeout(hideControls, 2500);
  }

  function hideControls() {
    playerWrap.classList.remove('controls-visible');
    playerWrap.classList.add('controls-hidden');
  }

  // Разметка плеера уже на странице (иначе плеер не инициализируется вовсе) —
  // все обработчики вешаем один раз при загрузке модуля.
  (function bindControls() {
    if (!available) return;

    if (ctrlSeekB) ctrlSeekB.addEventListener('click', () => seekBy(-5));
    if (ctrlSeekF) ctrlSeekF.addEventListener('click', () => seekBy(5));

    ctrlPlay.addEventListener('click', (e) => {
      e.stopPropagation();
      if (player.paused) player.play().catch(() => {}); else player.pause();
    });

    // Клик — play/pause, двойной — fullscreen; дебаунс, чтобы двойной не переключал play дважды.
    let lastClickAt = 0;
    let clickTimer = null;
    player.addEventListener('click', () => {
      const now = Date.now();
      if (now - lastClickAt < 300) {
        lastClickAt = 0;
        if (clickTimer) { clearTimeout(clickTimer); clickTimer = null; }
        toggleFullscreen();
        return;
      }
      lastClickAt = now;
      if (clickTimer) clearTimeout(clickTimer);
      clickTimer = setTimeout(() => {
        clickTimer = null;
        lastClickAt = 0;
        if (player.paused) player.play().catch(() => {}); else player.pause();
        if (playerWrap.classList.contains('fullscreen')) showControls();
      }, 300);
    });

    ctrlMute.addEventListener('click', (e) => {
      e.stopPropagation();
      player.muted = !player.muted;
      updatePlayerUI();
    });

    ctrlVolume.addEventListener('input', () => {
      player.volume = parseFloat(ctrlVolume.value);
      player.muted = player.volume === 0;
      updatePlayerUI();
    });

    // Серверное понижение: перезапуск HLS с quality (ffmpeg перекодирует), позиция/дорожка сохраняются.
    document.querySelectorAll('#ctrl-quality .q-btn').forEach((b) => {
      b.addEventListener('click', () => {
        if (!currentPlay) return;
        const q = b.dataset.quality;
        if (q === currentQuality) return;
        dbg('качество: ' + currentQuality + ' -> ' + q);
        currentQuality = q;
        updateQualityButtons();
        playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, streamStart, q, currentSubs);
      });
    });

    ctrlFullscreen.addEventListener('click', (e) => {
      e.stopPropagation();
      toggleFullscreen();
    });

    playerWrap.addEventListener('mousemove', () => {
      if (playerWrap.classList.contains('fullscreen')) showControls();
    });
    playerWrap.addEventListener('mouseleave', () => {
      if (playerWrap.classList.contains('fullscreen')) hideControls();
    });
    playerWrap.addEventListener('fullscreenchange', () => {
      const fs = !!document.fullscreenElement;
      playerWrap.classList.toggle('fullscreen', fs);
      if (fs) {
        showControls();
      } else {
        if (controlsTimer) clearTimeout(controlsTimer);
        playerWrap.classList.remove('controls-visible', 'controls-hidden');
      }
      updatePlayerUI();
    });

    if (ctrlDebug) {
      ctrlDebug.addEventListener('click', (e) => {
        e.stopPropagation();
        playerDebug.hidden = !playerDebug.hidden;
        if (!playerDebug.hidden) renderDebug();
      });
    }

    player.addEventListener('ended', () => {
      if (autoNextFired) return;
      autoNextFired = true;
      playNeighbor(1, true);
    });

    if (ctrlPrev) {
      ctrlPrev.addEventListener('click', (e) => {
        e.stopPropagation();
        playNeighbor(-1, false);
      });
    }
    if (ctrlNext) {
      ctrlNext.addEventListener('click', (e) => {
        e.stopPropagation();
        playNeighbor(1, false);
      });
    }

    player.addEventListener('timeupdate', updatePlayerUI);
    player.addEventListener('progress', updatePlayerUI);
    player.addEventListener('play', updatePlayerUI);
    player.addEventListener('pause', updatePlayerUI);
    player.addEventListener('volumechange', updatePlayerUI);
    player.addEventListener('durationchange', updatePlayerUI);

    // ←/→ — перемотка на 5 секунд (когда курсор не в поле ввода).
    document.addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      const tag = ((e.target && e.target.tagName) || '').toLowerCase();
      if (tag === 'input' || tag === 'textarea' || tag === 'select') return;
      if (playerWrap.hidden || !currentPlay) return;
      e.preventDefault();
      seekBy(e.key === 'ArrowLeft' ? -5 : 5);
    });
  })();

  // Уход со страницы (Back ТВ-пульта, переход по ссылке, закрытие вкладки): сохраняем
  // позицию и гасим ffmpeg-сессию — иначе поток живёт до серверного таймаута.
  function leave() {
    if (!currentPlay) return;
    maybeSaveProgress(true, true);
    fetch(`/api/films/${encodeURIComponent(currentPlay.id)}/hls/stop?session=${playbackSession}`, { keepalive: true }).catch(() => {});
  }

  if (available) {
    window.addEventListener('pagehide', leave);
    window.addEventListener('beforeunload', () => {
      if (hlsPlayer) stop();
    });
  }

  // ---- Публичный API ----

  function init(opts) {
    hooks = opts || {};
    return available;
  }

  // Перерисовка подписей (дорожки, субтитры, список серий) — нужна при смене языка:
  // работающий поток при этом НЕ перезапускается.
  function relabel() {
    if (!currentPlay) return;
    if (lastTracksItems.length) renderTracks(lastTracksItems, currentPlay.id, currentPlay.magnet);
    if (currentSubtitles.length) renderSubtitles(currentSubtitles, currentPlay.id, currentPlay.magnet);
    renderEpisodeList();
  }

  // Список серий раздачи (необязательный элемент страницы); null — страница рисует
  // серии сама (карточка фильма рисует сетку сезона через series.js).
  function setEpisodeList(el) {
    listEl = el || null;
    renderEpisodeList();
  }

  // Запуск раздачи: id/magnet обязательны, file — индекс серии (-1 — авто),
  // pos — позиция старта (продолжить просмотр), voice — озвучка для авто-выбора дорожки.
  function start(opts) {
    if (!available) return false;
    const o = opts || {};
    if (!o.id || !o.magnet) return false;
    currentPlay = { id: o.id, magnet: o.magnet };
    releaseTitle = o.release || '';
    h264FallbackDone = false;
    pendingCodecNote = false;
    streamRestarts = 0;
    autoNextFired = false;
    lastFiles = [];
    curSeason = o.season || 0;
    curEpisode = o.ep || 0;
    autoVoice = o.voice || '';
    currentFile = (typeof o.file === 'number' && o.file >= 0) ? o.file : -1;
    durationFetch = { key: '', inflight: false };
    totalDuration = 0;
    playerWrap.hidden = false;
    updateQualityButtons();
    // Серии текущей раздачи — для prev/next (фоном: плеер стартует сразу).
    loadFiles();
    loadTracks(o.id, o.magnet, currentFile);
    playHls(o.id, o.magnet, currentFile, 0, o.pos || 0, 'source', -1);
    notify();
    return true;
  }

  // Остановка: гасим поток и прячем плеер (карточка фильма при этом остаётся на месте).
  function stop(opts) {
    const o = opts || {};
    if (currentPlay && !o.keepProgress) maybeSaveProgress(true);
    if (hlsPlayer) {
      hlsPlayer.destroy();
      hlsPlayer = null;
    }
    if (player) {
      player.pause();
      player.removeAttribute('src');
      player.load();
    }
    if (playerWrap) playerWrap.hidden = true;
    if (tracksEl) {
      tracksEl.hidden = true;
      tracksList.innerHTML = '';
    }
    if (subsEl) {
      subsEl.hidden = true;
      subsList.innerHTML = '';
    }
    if (episodesEl) episodesEl.hidden = true;
    lastTracksItems = [];
    currentSubtitles = [];
    totalDuration = 0;
    currentPlay = null;
    currentFile = -1;
    lastFiles = [];
    notify();
  }

  return {
    available,
    init,
    setEpisodeList,
    start,
    stop,
    relabel,
    playing: () => !!currentPlay && !!playerWrap && !playerWrap.hidden,
    state,
    season: () => curSeason,
    episode: () => curEpisode,
    fileIndex: () => currentFile,
    files: () => lastFiles,
    magnet: () => (currentPlay ? currentPlay.magnet : ''),
    release: () => releaseTitle,
    selectEpisode,
    playNeighbor,
    showNote,
    saveProgress: maybeSaveProgress,
    hide: () => { if (playerWrap) playerWrap.hidden = true; },
    version: 1,
  };
})();

// Смена языка — перерисовываем подписи плеера (поток не трогаем).
if (typeof onLang === 'function') onLang(() => PP.relabel());

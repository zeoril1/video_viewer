'use strict';

/* Плеер (HLS-поток раздачи): звуковые дорожки, субтитры, перемотка,
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
  const tracksCurrent = tracksEl ? document.createElement('p') : null;
  if (tracksCurrent) {
    tracksCurrent.id = 'tracks-current';
    tracksCurrent.className = 'tracks-current';
    tracksCurrent.setAttribute('role', 'status');
    tracksCurrent.setAttribute('aria-live', 'polite');
    tracksCurrent.hidden = true;
    tracksEl.insertBefore(tracksCurrent, tracksList);
    tracksList.setAttribute('role', 'group');
    tracksList.setAttribute('aria-labelledby', 'tracks-title');
  }
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
  const currentQuality = 'source';
  let currentVideoCodec = '';
  const maxStreamRestarts = 3; // восстановление потерянной серверной сессии
  let streamRestarts = 0;
  let lastContinuationPosition = -1;
  let shortContinuations = 0;
  let selectedVoice = '';
  let selectedAudio = null; // Track identity survives reordered/unnamed tracks across episodes.
  let tracksGeneration = 0;
  let sourceCheckController = null;
  let pendingPlayback = null;
  let mediaRecovery = null;
  let playbackFailurePosition = null;
  let autoVoice = '';          // выбранная озвучка: loadTracks включит её дорожку
  let lastTracksItems = [];
  let currentFile = -1;        // индекс файла (серии) в торренте; -1 — авто
  let lastFiles = [];          // файлы текущей раздачи
  let curSeason = 0;           // сезон/серия текущего файла (для URL, истории, прогресса)
  let curEpisode = 0;
  let autoNextFired = false;   // защита от повторного автоперехода
  let completedPlayback = false;
  let lastProgressSend = 0;
  let playbackReady = false;
  let follower = false, remotePaused = false;
  function clearMediaRecovery() {
    const recovery = mediaRecovery;
    mediaRecovery = null;
    if (recovery) recovery.cancel();
  }
  function stage(stage, message) {
    window.dispatchEvent(new CustomEvent('playbackstage', { detail: { stage, message } }));
  }
  function autoPlay() {
    if (pendingPlayback || mediaRecovery || playbackFailurePosition != null) return;
    if (follower && remotePaused) {
      stage('paused', 'Просмотр на паузе.');
      return;
    }
    if (!trailerActive) {
      const attemptedHls = hlsPlayer, attemptedPlay = currentPlay, attemptedStart = streamStart;
      player.play().catch(() => {
        if (pendingPlayback || mediaRecovery || playbackFailurePosition != null || trailerActive
            || (follower && remotePaused)
            || attemptedHls !== hlsPlayer || attemptedPlay !== currentPlay || attemptedStart !== streamStart) return;
        stage('paused', 'Нажмите ▶, чтобы начать просмотр.');
      });
    }
  }
  let lastSavedSample = '';
  let cacheKeepKey = '';       // раздача#серия, для которых уже запрошен тёплый кеш
  let noteTimer = null;
  let listEl = null;           // куда рисовать список серий раздачи (необязательно)
  let hooks = {};
  let trailerActive = false;
  let trailerWasHidden = true;
  const trailerFrame = document.getElementById('trailer-player');
  const trailerToggle = document.getElementById('trailer-toggle');

  function closeTrailer() {
    if (!trailerActive) return;
    trailerActive = false;
    trailerFrame.removeAttribute('src');
    trailerFrame.hidden = true;
    playerWrap.classList.remove('trailer-mode');
    playerWrap.hidden = trailerWasHidden;
    trailerToggle.textContent = '▶ Трейлер';
    trailerToggle.setAttribute('aria-pressed', 'false');
  }

  function setTrailer(url) {
    if (!trailerFrame || !trailerToggle) return;
    closeTrailer();
    let key = '';
    try {
      const parsed = new URL(url);
      if (parsed.hostname === 'www.youtube.com' || parsed.hostname === 'youtube.com') key = parsed.searchParams.get('v') || '';
    } catch (_) { /* No trailer available. */ }
    const valid = /^[a-zA-Z0-9_-]{11}$/.test(key);
    document.getElementById('trailer-actions').hidden = !valid;
    trailerToggle.onclick = () => {
      if (trailerActive) { closeTrailer(); return; }
      if (!valid) return;
      maybeSaveProgress(true);
      player.pause();
      trailerWasHidden = playerWrap.hidden;
      trailerActive = true;
      playerWrap.hidden = false;
      playerWrap.classList.add('trailer-mode');
      trailerFrame.hidden = false;
      trailerFrame.src = 'https://www.youtube-nocookie.com/embed/' + key + '?autoplay=1';
      trailerToggle.textContent = currentPlay ? 'Вернуться к просмотру' : 'Закрыть трейлер';
      trailerToggle.setAttribute('aria-pressed', 'true');
    };
  }

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
      position: absTime(),
      duration: totalDuration,
      quality: currentQuality,
      video_codec: currentVideoCodec,
      stream_start: streamStart,
      track: currentTrack,
      audio: pendingPlayback ? null : currentAudio(),
      subs: currentSubs,
      season: curSeason,
      episode: curEpisode,
      voice: selectedVoice,
      paused: player.paused,
      active: !!currentPlay,
      preparing: !!pendingPlayback,
      playing: !!hlsPlayer && !playerWrap.hidden && !trailerActive,
    };
  }

  function notify() {
    if (hooks.onStateChange) {
      try { hooks.onStateChange(state()); } catch (e) { dbg('player hook: ' + e.message); }
    }
    window.dispatchEvent(new CustomEvent('playbackchange', { detail: state() }));
  }

  // ---- Прогресс просмотра ----
  // Отправка позиции в историю (не чаще раза в 5 с; final=true — принудительно).
  function maybeSaveProgress(final, beacon) {
    if (!VV.user || !currentPlay || !playbackReady) return null;
    const userID = VV.user.id, authVersion = VV.authGeneration;
    const sameUser = () => VV.user && VV.user.id === userID && VV.authGeneration === authVersion;
    const now = Date.now();
    if (!final && now - lastProgressSend < 5000) return null;
    const pos = Math.round(absTime());
    if (!Number.isFinite(pos) || pos < 0) return null;
    const sample = userID + '|' + authVersion + '|' + currentPlay.id + '|' + currentPlay.magnet + '|' + currentFile + '|' + pos + '|' + curSeason + '|' + curEpisode;
    if (sample === lastSavedSample) return null;
    lastSavedSample = sample;
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
      voice: selectedVoice,
      position: pos,
      duration: Math.round(totalDuration || 0),
    };
    return fetch('/api/history/progress', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      keepalive: !!beacon,
    }).then((res) => {
      if (!sameUser()) return;
      if (!res.ok) throw new Error('HTTP ' + res.status);
      rememberWatchProgress(body);
    }).catch((err) => {
      if (!sameUser()) return;
      if (lastSavedSample === sample) { lastSavedSample = ''; lastProgressSend = 0; }
      dbg('history: ' + err.message);
    });
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

  function playbackHlsConfig() {
    const android = typeof navigator !== 'undefined' && /Android|VVTV\//i.test(navigator.userAgent || '');
    return {
      // ffmpeg already applies the absolute seek; every returned HLS timeline
      // starts at zero, even when a cached file has been remuxed far ahead.
      startPosition: 0,
      lowLatencyMode: false,
      liveDurationInfinity: true,
      // Android/WebView uses the pinned hls.js defaults instead of the 1 GiB target.
      maxBufferLength: android ? 30 : 60,
      // A byte target needs a long time ceiling for low-bitrate files. Keep it
      // finite so hls.js can reduce it when the browser's MSE quota is smaller.
      maxMaxBufferLength: android ? 600 : 6 * 60 * 60,
      maxBufferSize: android ? 60 * 1000 * 1000 : 1024 * 1024 * 1024,
      // Plain media playlists have no BANDWIDTH attribute. Learn the encoded
      // bitrate from loaded fragments so the byte target also works for them.
      abrMaxWithRealBitrate: !android,
      backBufferLength: 30,
      // The growing playlist represents a file, rather than a live broadcast.
      // Keep its live start at the first fragment regardless of generated length.
      // max latency retains hls.js's Infinity default; explicitly setting both
      // values to Infinity fails its strict max > sync config validation.
      liveSyncDurationCount: Infinity,
      maxLiveSyncPlaybackRate: 1,
      debug: DEBUG,
      manifestLoadingTimeOut: 60000,
      manifestLoadingMaxRetry: 2,
      manifestLoadingRetryDelay: 2000,
      manifestLoadingMaxRetryTimeout: 60000,
      levelLoadingTimeOut: 30000,
      levelLoadingMaxRetry: 4,
      levelLoadingRetryDelay: 2000,
      levelLoadingMaxRetryTimeout: 60000,
      // Large source-quality fragments can take more than 20s on a slow link.
      fragLoadingTimeOut: 60000,
      fragLoadingMaxRetry: 6,
      fragLoadingRetryDelay: 1500,
      fragLoadingMaxRetryTimeout: 90000,
    };
  }

  // Запуск HLS-потока файла: дорожка track, смещение start, исходное качество,
  // субтитр subs (-1 — без субтитров). Смена дорожки/
  // субтитра перезапускает ffmpeg; вкл/выкл текущей дорожки делает hls.js (subtitleTrack).
  // Resolve tracks before opening HLS. Starting a provisional stream and then
  // restarting it at currentTime can turn an episode transition into a seek.
  function beginPlayback(id, magnet, file, track, position, quality) {
    if (sourceCheckController) sourceCheckController.abort();
    sourceCheckController = null;
    clearMediaRecovery();
    playbackFailurePosition = null;
    const pending = { id, magnet, file, position, quality: currentQuality };
    pendingPlayback = pending;
    playbackReady = false;
    autoNextFired = true;
    if (hlsPlayer) { hlsPlayer.destroy(); hlsPlayer = null; }
    player.pause();
    player.removeAttribute('src');
    player.load();
    streamStart = position;
    currentTrack = track;
    currentSubs = -1;
    stage('preparing', 'Определяем аудиодорожку и субтитры следующего потока.');
    let metadata;
    if (typeof SourceIdentity !== 'undefined') {
      tracksGeneration++;
      lastTracksItems = [];
      currentVideoCodec = '';
      tracksEl.hidden = true;
      subsEl.hidden = true;
      const controller = new AbortController();
      sourceCheckController = controller;
      metadata = SourceIdentity.check({ id, magnet, title: releaseTitle, signal: controller.signal }).then(verdict => {
        if (pendingPlayback !== pending) return false;
        sourceCheckController = null;
        if (verdict.status === 'mismatch') {
          pending.sourceMismatch = true;
          window.dispatchEvent(new CustomEvent('sourceidentityfailure', { detail: { id, magnet } }));
          return false;
        }
        return loadTracks(id, magnet, file);
      });
    } else metadata = loadTracks(id, magnet, file);
    metadata.then(ready => {
      if (pendingPlayback !== pending) return;
      pendingPlayback = null;
      if (!ready) {
        if (pending.sourceMismatch) playbackFailurePosition = pending.position;
        const message = pending.sourceMismatch
          ? 'Этот источник относится к другому фильму или сериалу. Выберите подходящий источник — позиция просмотра сохранена.'
          : 'Не удалось получить аудиодорожки и сведения о видео с сервера. Повторите запуск или выберите другой источник.';
        stage('error', message);
        window.dispatchEvent(new CustomEvent('playbackfailure', { detail: { title: pending.sourceMismatch ? 'Неподходящий источник' : 'Не удалось начать просмотр',
          reason: pending.sourceMismatch ? 'source_mismatch' : 'metadata_unavailable', message } }));
        return;
      }
      playHls(id, magnet, file, currentTrack, pending.position, pending.quality, currentSubs);
      notify();
    });
  }

  function playHls(id, magnetSrc, file, track, start, quality, subs) {
    if (pendingPlayback) {
      pendingPlayback.position = start || 0;
      pendingPlayback.quality = currentQuality;
      streamStart = pendingPlayback.position;
      return;
    }
    if (typeof SourceIdentity !== 'undefined' && SourceIdentity.isRejected(id, magnetSrc)) {
      stage('error', 'Этот источник относится к другому фильму или сериалу. Выберите подходящий источник — позиция просмотра сохранена.');
      return;
    }
    clearMediaRecovery();
    playbackFailurePosition = null;
 stage('preparing');
    closeTrailer();
    playbackReady = false;
    if (hlsPlayer) {
      hlsPlayer.destroy();
      hlsPlayer = null;
    }
    player.pause();
    player.removeAttribute('src');
    player.load();
    playerError.hidden = true;
    // Плеер показываем при любом запуске — иначе после ошибки он останется скрытым.
    playerWrap.hidden = false;

    currentTrack = track || 0;
    currentSubs = (typeof subs === 'number' && subs >= 0) ? subs : -1;
    streamStart = start || 0;
    currentFile = (typeof file === 'number' && file >= 0) ? file : -1;
    autoNextFired = false; // новый поток — автопереход можно снова
    // Длительность запрашиваем при старте с любой позиции — иначе шкала/время её не покажут.
    fetchDuration(id, magnetSrc, currentFile);

    const p = new URLSearchParams({ magnet: magnetSrc });
    p.set('session', playbackSession);
    p.set('track', String(currentTrack));
    if (currentFile >= 0) p.set('file', String(currentFile));
    if (currentSubs >= 0) p.set('subs', String(currentSubs));
    if (streamStart > 0) p.set('start', String(streamStart));
    const src = `/api/films/${encodeURIComponent(id)}/hls.m3u8?` + p.toString();
    dbg('playHls: id=' + id + ' file=' + currentFile + ' track=' + currentTrack + ' subs=' + currentSubs + ' start=' + streamStart + ' q=' + currentQuality);

    if (window.Hls && Hls.isSupported()) {
      const hlsConfig = playbackHlsConfig();
      dbg('hls.js: создаём поток v' + (Hls.version || '?') + ' [' + hlsLogSrc(src) + ']'
        + ' | live=' + hlsConfig.liveDurationInfinity
        + ' manifestTO=' + hlsConfig.manifestLoadingTimeOut
        + ' levelTO=' + hlsConfig.levelLoadingTimeOut
        + ' fragTO=' + hlsConfig.fragLoadingTimeOut
        + ' start=' + hlsConfig.startPosition + ' buffer=' + hlsConfig.maxBufferLength + '/' + hlsConfig.maxMaxBufferLength + 's'
        + ' | плеер readyState=' + player.readyState + ' t=' + Math.round(player.currentTime || 0));
      const hls = new Hls(hlsConfig);
      let mediaRecoveryAttempts = 0;
      const maxMediaRecoveryAttempts = 2;
      const failedAppendFragments = new Map();
      hlsPlayer = hls;
      hls.loadSource(src);
      hls.attachMedia(player);
      function bufferedRanges() {
        const ranges = [];
        try {
          for (let index = 0; index < player.buffered.length; index++) {
            ranges.push(player.buffered.start(index).toFixed(3) + '-' + player.buffered.end(index).toFixed(3));
          }
        } catch (_) { ranges.push('unavailable'); }
        return '[' + ranges.join(', ') + ']';
      }
      function errorText(data, exhausted, reason) {
        if (reason === 'repeated_media_fragment') {
          return 'Один и тот же видеофрагмент повторно не удалось обработать. Восстановление плеера не помогло. Выберите другой источник — позиция просмотра сохранится.';
        }
        if (data.type === 'mediaError') {
          if (['bufferAppendError', 'bufferAppendingError', 'bufferStalledError'].includes(data.details)) {
            return exhausted
              ? 'Браузер не смог обработать видеофрагмент. Автоматическое восстановление не помогло. Повторите запуск или выберите другой источник.'
              : 'Браузер не смог обработать видеофрагмент. Повторите запуск или выберите другой источник.';
          }
          if (['bufferAddCodecError', 'bufferIncompatibleCodecsError'].includes(data.details)) {
            return 'Браузер не поддерживает формат видео или аудио выбранного источника. Выберите другой источник.';
          }
          return 'Браузер не смог разобрать видеофрагмент. Повторите запуск или выберите другой источник.';
        }
        if (data.details === 'manifestIncompatibleCodecsError') {
          return 'Браузер не поддерживает формат видео или аудио выбранного источника. Выберите другой источник.';
        }
        if (data.response && data.response.code === 503) {
          return 'Сервер занят или достигнут лимит диска. Подождите немного и повторите запуск.';
        }
        if (data.response && data.response.code === 404) {
          return 'Серверная сессия видео недоступна. Автоматическое восстановление не помогло. Повторите запуск с текущей позиции.';
        }
        if (data.type === 'networkError') {
          return data.details && /TimeOut$/.test(data.details)
            ? 'Сервер не передал видео вовремя. Автоматические повторы не помогли. Повторите запуск.'
            : 'Не удалось получить видео с сервера. Повторите запуск или выберите другой источник.';
        }
        return 'Воспроизведение прервалось. Повторите запуск или выберите другой источник.';
      }
      function fail(data, exhausted = false, reason = '') {
        if (hlsPlayer !== hls) return;
        const position = mediaRecovery && mediaRecovery.hls === hls
          ? streamStart + mediaRecovery.position : streamStart + (player.currentTime || 0);
        dbg('hls: поток перед остановкой: id=' + id + ' file=' + currentFile + ' track=' + currentTrack
          + ' subs=' + currentSubs + ' start=' + streamStart + ' position=' + position.toFixed(3)
          + ' codec=' + (currentVideoCodec || '?') + ' q=' + currentQuality + ' [' + hlsLogSrc(src) + ']');
        const message = errorText(data, exhausted, reason);
        const technical = [data.type, data.details].filter(Boolean).join(' / ')
          + (data.response ? ' (HTTP ' + data.response.code + ')' : '');
        const hadPlayback = playbackReady || position > streamStart;
        playbackFailurePosition = position;
        maybeSaveProgress(true);
        playbackReady = false;
        clearMediaRecovery();
        window.dispatchEvent(new CustomEvent('playbackfailure', { detail: {
          title: hadPlayback ? 'Просмотр прерван' : 'Не удалось начать просмотр',
          message, type: data.type, details: data.details, reason,
          status: data.response && data.response.code,
        } }));
        hlsPlayer = null;
        hls.destroy();
        playerError.textContent = message + (technical ? ' — ' + technical : '');
        playerError.hidden = false;
        showDebug();
        if (typeof playerError.scrollIntoView === 'function') playerError.scrollIntoView({ block: 'nearest' });
      }
      function repeatedAppendFragment(data) {
        // Duplicate callbacks before a reattach do not mean the media was retried.
        if (mediaRecovery && mediaRecovery.hls === hls
            && ['queued', 'recovering'].includes(mediaRecovery.phase)) return false;
        const fragment = data.frag;
        if (!['bufferAppendError', 'bufferAppendingError'].includes(data.details)
            || !fragment || !Number.isInteger(fragment.sn) || fragment.sn < 0
            || !Number.isFinite(fragment.start) || !Number.isFinite(fragment.duration)
            || fragment.duration <= 0) return false;
        const key = JSON.stringify([fragment.type, fragment.level, fragment.cc, fragment.sn,
          fragment.start, fragment.duration, data.sourceBufferName]);
        const failedAttempt = failedAppendFragments.get(key);
        if (failedAttempt != null && failedAttempt < mediaRecoveryAttempts) return true;
        failedAppendFragments.set(key, mediaRecoveryAttempts);
        return false;
      }
      function recoverMedia() {
        if (mediaRecovery && mediaRecovery.hls === hls
            && ['queued', 'recovering'].includes(mediaRecovery.phase)) return true;
        if (mediaRecoveryAttempts >= maxMediaRecoveryAttempts) return false;
        const previous = mediaRecovery && mediaRecovery.hls === hls ? mediaRecovery : null;
        const position = previous ? previous.position : Math.max(0, player.currentTime || 0);
        const paused = previous ? previous.paused : player.paused;
        clearMediaRecovery();
        mediaRecoveryAttempts++;
        playbackReady = false;
        let timer;
        const events = ['loadeddata', 'canplay', 'progress', 'seeked'];
        const recovery = { hls, position, paused, phase: 'queued', cancel() {
          clearTimeout(timer);
          for (const event of events) player.removeEventListener(event, restore);
        } };
        function restore() {
          if (hlsPlayer !== hls || mediaRecovery !== recovery || recovery.phase !== 'loading') return;
          if (player.readyState < 2 || !bufferedContains(streamStart + recovery.position)) return;
          try { player.currentTime = recovery.position; }
          catch (_) { return; }
          const pausedByHost = follower && remotePaused;
          const stayPaused = recovery.paused || pausedByHost || trailerActive;
          clearMediaRecovery();
          playbackReady = true;
          if (stayPaused) {
            player.pause();
            stage('paused', pausedByHost ? 'Просмотр на паузе у ведущего.' : 'Просмотр на паузе.');
          } else {
            stage('buffering', 'Позиция восстановлена. Возобновляем просмотр.');
            autoPlay();
          }
          notify();
        }
        recovery.restore = restore;
        mediaRecovery = recovery;
        dbg('hls: восстанавливаем медиабуфер браузера с ' + position.toFixed(3) + 's (попытка '
          + mediaRecoveryAttempts + '/' + maxMediaRecoveryAttempts + '); серверный поток сохраняется');
        stage('buffering', 'Восстанавливаем видеоплеер с текущей позиции.');
        // SourceBuffer's error callback is still on its operation queue. Let it
        // unwind before detaching MSE; recovery recreates only the browser buffer.
        timer = setTimeout(() => {
          if (hlsPlayer !== hls || mediaRecovery !== recovery) return;
          recovery.phase = 'recovering';
          for (const event of events) player.addEventListener(event, restore);
          try {
            // MEDIA_ATTACHED can run later and auto-start using this config.
            // Keep that automatic start on the same relative playback position.
            hls.config.startPosition = recovery.position;
            hls.recoverMediaError();
            hls.startLoad(recovery.position);
            recovery.phase = 'loading';
            restore();
          } catch (error) {
            dbg('hls: восстановление медиабуфера не удалось: ' + hlsLogSrc(error.message || String(error)));
            fail({ type: 'mediaError', details: 'bufferAppendError', error }, true);
          }
        }, 0);
        return true;
      }
      // The subtitle list can arrive after MANIFEST_PARSED (a separate playlist).
      hls.on(Hls.Events.SUBTITLE_TRACKS_UPDATED, () => {
        if (hlsPlayer !== hls) return;
        hls.subtitleDisplay = currentSubs >= 0;
        hls.subtitleTrack = currentSubs >= 0 && hls.subtitleTracks.length ? 0 : -1;
      });
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        // Игнорируем событие уже заменённого плеера.
        if (hlsPlayer !== hls) return;
        dbg('hls: манифест получен (уровней: ' + (hls.levels ? hls.levels.length : 0) + ' субтитров: ' + (hls.subtitleTracks ? hls.subtitleTracks.length : 0) + ')');
        // Субтитры включаем, если поток запущен с выбранной дорожкой и hls.js видит SUBTITLES.
        if (hls.subtitleTracks) {
          hls.subtitleTrack = (currentSubs >= 0 && hls.subtitleTracks.length) ? 0 : -1;
          hls.subtitleDisplay = true;
        }
        playerError.hidden = true;
        stage('buffering');
 autoPlay();
      });
      hls.on(Hls.Events.LEVEL_SWITCHED, (_e, d) => dbg('hls: уровень переключён: ' + d.level));
      hls.on(Hls.Events.FRAG_CHANGED, () => {
        if (hlsPlayer !== hls) return;
        // A small forward window needs the current fragment boundary immediately,
        // rather than waiting for the next ten-second session heartbeat.
        window.dispatchEvent(new Event('playbackfragment'));
      });
      hls.on(Hls.Events.FRAG_BUFFERED, (_e, d) => {
        if (hlsPlayer !== hls || !d || !d.frag) return;
        // A manifest can succeed while every fragment is still 404. Reset the
        // recovery budget only after real media reached the browser buffer.
        if (d.frag.sn !== 'initSegment') streamRestarts = 0;
        const stats = (d.part || d.frag).stats || {};
        const loading = stats.loading || {};
        const milliseconds = Math.max(0, (loading.end || 0) - (loading.start || 0));
        const firstByteMs = Math.max(0, (loading.first || 0) - (loading.start || 0));
        dbg('hls: фрагмент ' + d.frag.sn + ' @ ' + Math.round(d.frag.start) + 's'
          + ' | ' + (stats.total || 0) + ' bytes / ' + Math.round(milliseconds) + 'ms'
          + ' | первый байт=' + Math.round(firstByteMs) + 'ms'
          + ' | buffer=' + bufferedAhead().toFixed(1) + 's');
        if (mediaRecovery && mediaRecovery.hls === hls) mediaRecovery.restore();
      });
      hls.on(Hls.Events.ERROR, (_evt, data) => {
        // Игнорируем ошибки от уже заменённого потока.
        if (hlsPlayer !== hls) return;
        const resp = data && data.response ? ' (http ' + data.response.code + ')' : '';
        dbg('hls: ERROR type=' + (data && data.type) + ' details=' + (data && data.details)
          + ' fatal=' + !!(data && data.fatal) + resp);
        const error = data && (data.error || data.err), fragment = data && data.frag;
        if (error || (data && (data.reason || data.sourceBufferName))) {
          dbg('hls: причина: name=' + (error && error.name || '?') + ' code=' + (error && error.code || 0)
            + ' message=' + hlsLogSrc(String(error && error.message || '').replace(/[\r\n]+/g, ' '))
            + ' reason=' + hlsLogSrc(String(data && data.reason || '').replace(/[\r\n]+/g, ' ')));
        }
        dbg('hls: медиабуфер: sourceBuffer=' + (data && data.sourceBufferName || '?')
          + ' mediaError=' + (player.error ? player.error.code + ':' + hlsLogSrc(player.error.message || '') : 'none')
          + ' readyState=' + player.readyState + ' t=' + Number(player.currentTime || 0).toFixed(3)
          + ' ranges=' + bufferedRanges()
          + (fragment ? ' frag=' + fragment.sn + ' start=' + fragment.start + ' duration=' + fragment.duration : ''));
        // Фатальная ошибка: на сырой поток не фолбэчим — там AC3/DTS, звука не будет.
        // Плеер не скрываем: можно выбрать другую дорожку или исходную раздачу
        // с поддерживаемым браузером кодеком (например, H.264).
        if (data && data.fatal) {
          if (data.type === 'mediaError'
              && ['bufferAppendError', 'bufferAppendingError', 'bufferStalledError'].includes(data.details)) {
            if (repeatedAppendFragment(data)) {
              dbg('hls: тот же фрагмент повторно отвергнут после восстановления медиабуфера; нужен другой источник');
              fail(data, true, 'repeated_media_fragment');
              return;
            }
            if (recoverMedia()) return;
            fail(data, true);
            return;
          }
          // Retry the existing server stream after hls.js exhausts its request
          // retries. Do not discard downloaded media or change the source.
          if (data.type === 'networkError' && data.details === 'fragLoadTimeOut'
              && currentPlay && streamRestarts < maxStreamRestarts) {
            streamRestarts++;
            dbg('hls: таймаут доставки с сервера — повтор загрузки (попытка '
              + streamRestarts + '/' + maxStreamRestarts + ')');
            stage('buffering');
            hls.startLoad(player.currentTime || 0);
            return;
          }
          dbg('hls: ФАТАЛЬНАЯ ошибка, воспроизведение остановлено');
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
          fail(data);
        }
      });
    } else if (player.canPlayType && player.canPlayType('application/vnd.apple.mpegurl')) {
      // Нативные HLS (Safari).
      dbg('HLS: нативный (Safari)');
      player.src = src;
      const showNativeSubtitles = () => {
        if (hlsPlayer || player.getAttribute('src') !== src) return;
        let selected = false;
        for (const textTrack of player.textTracks) {
          if (textTrack.kind !== 'subtitles' && textTrack.kind !== 'captions') continue;
          textTrack.mode = currentSubs >= 0 && !selected ? 'showing' : 'disabled';
          selected = true;
        }
      };
      player.textTracks.onaddtrack = showNativeSubtitles;
      player.onloadedmetadata = showNativeSubtitles;
      autoPlay();
    } else {
      // HLS не поддерживается браузером — сырой поток (без транскодинга).
      dbg('HLS: не поддерживается — сырой поток');
      const qs = new URLSearchParams({ magnet: magnetSrc });
      if (currentFile >= 0) qs.set('file', String(currentFile));
      player.src = `/api/stream/${encodeURIComponent(id)}?` + qs.toString();
      autoPlay();
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
        if (!currentPlay || currentPlay.id !== id || currentPlay.magnet !== magnetSrc || currentFile !== file) return;
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
  function audioOrdinal(items, preference, voice, magnet) {
    if (!items.length) return -1;
    if (items.length === 1) return items[0].ordinal;
    const normalize = value => String(value || '').trim().toLowerCase();
    const language = value => ({ru:'rus', en:'eng', ja:'jpn'})[normalize(value)] || normalize(value);
    if (preference) {
      const title = normalize(preference.title), lang = language(preference.language);
      const exact = title && items.filter(tr => normalize(tr.title) === title && (!lang || language(tr.language) === lang));
      if (exact && exact.length) return (exact.find(tr => tr.ordinal === preference.ordinal) || exact[0]).ordinal;
    }
    const matched = matchVoiceOrdinal(items, voice);
    if (matched != null) return matched;
    if (preference) {
      const lang = language(preference.language);
      const sameLanguage = lang ? items.filter(tr => language(tr.language) === lang) : [];
      if (sameLanguage.length === 1) return sameLanguage[0].ordinal;
      const sameSlot = items.find(tr => tr.ordinal === preference.ordinal);
      if (preference.magnet === magnet && sameSlot && (!lang || language(sameSlot.language) === lang)) return sameSlot.ordinal;
    }
    return items[0].ordinal;
  }

  async function loadTracks(id, magnetSrc, file) {
    const generation = ++tracksGeneration;
    // Заявляем ключ длительности, чтобы fetchDuration не дублировал этот же запрос.
    durationFetch.key = id + '|' + (magnetSrc || '') + '|' + (typeof file === 'number' ? file : -1);
    durationFetch.inflight = true;
    lastTracksItems = [];
    tracksEl.hidden = false;
    tracksList.innerHTML = '';
    tracksCurrent.hidden = true;
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
      if (generation !== tracksGeneration || !currentPlay || currentPlay.id !== id || currentPlay.magnet !== magnetSrc || currentFile !== file) return;
      totalDuration = data.duration || 0;
      updatePlayerUI();
      window.dispatchEvent(new CustomEvent('playbacksegments', { detail: {
        segments: data.segments || [], media_key: data.media_key || '', duration: totalDuration,
        identity: { id, magnet: magnetSrc, file },
      } }));
      // Запрос длительности завершён — fetchDuration в playHls больше не нужен.
      durationFetch.inflight = false;
      currentVideoCodec = (data.codec || '').toLowerCase();
      // По реальному разрешению показываем только доступные кнопки качества (1080p не «повысить» до 4K).
      const items = data.items || [];
      lastTracksItems = items;
      const subtitles = data.subtitles || [];
      dbg('tracks: id=' + id + ' duration=' + (data.duration || '?') + 's дорожек=' + items.length + ' субтитров=' + subtitles.length + ' video=' + currentVideoCodec + '/' + (data.height || 0) + 'p');
      // Субтитры рендерим ДО раннего возврата по аудио-дорожкам — иначе при одной
      // (или нулевой) звуковой дорожке блок субтитров не появился бы.
      currentSubtitles = subtitles;
      renderSubtitles(subtitles, id, magnetSrc);

      const previousTrack = currentTrack;
      const ordinal = audioOrdinal(items, selectedAudio, autoVoice || selectedVoice, magnetSrc);
      autoVoice = '';
      currentTrack = ordinal;
      const activeTrack = items.find(tr => tr.ordinal === ordinal);
      if (activeTrack) {
        selectedAudio = { ...activeTrack, magnet: magnetSrc };
        // The preference may be unavailable in this file. Report the track that
        // was actually resolved, so history and source changes keep that voice.
        selectedVoice = titleVoices(activeTrack.title || '')[0] || activeTrack.title || trackButtonLabel(activeTrack);
      } else {
        selectedAudio = null;
        selectedVoice = '';
      }
      renderTracks(items, id, magnetSrc);
      if (!pendingPlayback && ordinal !== previousTrack) {
        playHls(id, magnetSrc, file, ordinal, absTime(), currentQuality, currentSubs);
      }
      window.dispatchEvent(new CustomEvent('subtitletracks', { detail: subtitles }));
      return true;
    } catch (e) {
      if (generation !== tracksGeneration || !currentPlay || currentPlay.id !== id || currentPlay.magnet !== magnetSrc || currentFile !== file) return;
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
      return false;
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

  function currentAudio() {
    const track = lastTracksItems.find(item => item.ordinal === currentTrack);
    return track ? { ordinal: track.ordinal, title: track.title || '', language: track.language || '',
      codec: track.codec || '', label: trackButtonLabel(track) } : null;
  }

  function renderTracks(items, id, magnetSrc) {
    tracksEl.hidden = false;
    tracksTitle.textContent = t('audioTracks');
    tracksList.innerHTML = '';
    const active = items.find(tr => tr.ordinal === currentTrack);
    tracksCurrent.textContent = active ? t('audioSelected') + ': ' + trackButtonLabel(active) : t('audioNone');
    tracksCurrent.hidden = false;
    // Keep one button per translation, using its active track when codecs share
    // a label. Otherwise the selected duplicate would have no selected button.
    const choices = new Map();
    items.forEach(tr => {
      const label = trackButtonLabel(tr);
      if (!choices.has(label) || tr.ordinal === currentTrack) choices.set(label, tr);
    });
    choices.forEach((tr, label) => {
      const selected = tr.ordinal === currentTrack;
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'track-btn' + (selected ? ' active' : '');
      btn.setAttribute('aria-pressed', String(selected));
      btn.setAttribute('aria-label', label);
      btn.textContent = (selected ? '✓ ' : '') + label;
      btn.addEventListener('click', () => {
        if (currentTrack === tr.ordinal) return;
        selectedVoice = titleVoices(tr.title || '')[0] || tr.title || label;
        selectedAudio = { ...tr, magnet: magnetSrc };
        autoVoice = '';
        // Смена дорожки сохраняет позицию, серию, качество и субтитры (ordinal аудио).
        playHls(id, magnetSrc, currentFile, tr.ordinal, absTime(), currentQuality, currentSubs);
        renderTracks(items, id, magnetSrc);
        tracksList.querySelector('[aria-pressed="true"]')?.focus({ preventScroll: true });
        notify();
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
  function subsSelect(id, magnetSrc, subs, remember = true) {
    if (!currentPlay || subs === currentSubs) return;
    dbg('субтитры: ' + id + ' -> ' + (subs >= 0 ? subs : 'выкл'));
    const position = absTime();
    currentSubs = subs;
    if(remember){const tr=currentSubtitles.find(t=>t.ordinal===subs);window.dispatchEvent(new CustomEvent('subtitlechoice',{detail:tr?tr.language:'off'}));}
    renderSubtitles(currentSubtitles, id, magnetSrc);
    if (!pendingPlayback) playHls(id, magnetSrc, currentFile, currentTrack, position, currentQuality, subs);
  }

  // ---- Серии текущей раздачи ----

  // Файлы раздачи: prev/next, список серий и (через хук onFiles) сетка на странице.
  async function loadFiles() {
    if (!currentPlay) return;
    const play = currentPlay;
    const files = await fetchFiles(play.id, play.magnet, releaseTitle);
    if (currentPlay !== play) return;
    lastFiles = files || [];
    const selected = lastFiles.find((f) => f.index === currentFile);
    if (selected) {
      curSeason = selected.season || curSeason;
      curEpisode = selected.episode || curEpisode;
    }
    dbg('files: файлов=' + lastFiles.length + ' (id=' + currentPlay.id + ')');
    if (hooks.onFiles) {
      try { hooks.onFiles(lastFiles, state()); } catch (e) { dbg('files hook: ' + e.message); }
    }
    renderEpisodeList();
    notify();
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
    maybeSaveProgress(true);
    completedPlayback = false;
    playbackReady = false;
    lastProgressSend = 0;
    autoVoice = selectedVoice;
    currentFile = index;
    streamStart = 0;
    // Keep the current ordinal until metadata maps the chosen audio to the new file.
    // Субтитры новой серии могут отличаться — сброс (селектор перерисует loadTracks).
    currentSubs = -1;
    // Старая длительность могла бы спровоцировать преждевременный автопереход.
    totalDuration = 0;
    // Кеш длительности привязан к файлу — новый запрос обязателен.
    durationFetch = { key: '', inflight: false };
    const sel = lastFiles.find((f) => f.index === index);
    curSeason = sel && sel.season || 0;
    curEpisode = sel && sel.episode || 0;
    const saved = episodeHistoryEntry(currentPlay.id, curSeason, curEpisode, currentPlay.magnet, index);
    streamStart = saved ? saved.position : 0;
    autoNextFired = false;
    renderEpisodeList();
    notify();
    dbg('серия: файл #' + index);
    beginPlayback(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, streamStart, currentQuality);
 if(typeof window!=='undefined')window.dispatchEvent(new Event('playbackstart'));
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
 if(follower)return;
    if (!currentPlay) return;
    const prepared=dir>0&&typeof PlaybackExtras!=='undefined'&&PlaybackExtras.next?PlaybackExtras.next():null;
    if(prepared){start({...prepared, voice:selectedVoice});return;}
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

  function bufferedContains(target, minimumAhead = 0) {
    const relative = target - streamStart;
    if (!Number.isFinite(relative) || relative < 0 || !Number.isFinite(minimumAhead) || minimumAhead < 0) return false;
    try {
      const ranges = player.buffered;
      for (let i = 0; i < ranges.length; i++) {
        if (relative >= ranges.start(i) && relative + minimumAhead <= ranges.end(i)) return true;
      }
    } catch (_) { /* No usable media ranges yet. */ }
    return false;
  }

  function bufferedAhead() {
    try {
      const ranges = player.buffered, position = player.currentTime || 0;
      for (let i = 0; i < ranges.length; i++) {
        if (position >= ranges.start(i) && position <= ranges.end(i)) return ranges.end(i) - position;
      }
    } catch (_) { /* No usable media ranges yet. */ }
    return 0;
  }

  // Абсолютная позиция: streamStart + currentTime (при перемотке ffmpeg стартует с позиции).
  function absTime() {
    if (completedPlayback && totalDuration > 0) return totalDuration;
    if (playbackFailurePosition != null) return playbackFailurePosition;
    if (mediaRecovery && mediaRecovery.hls === hlsPlayer) return streamStart + mediaRecovery.position;
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
    if (!follower && !player.paused && totalDuration > 0 && pos >= totalDuration - 1.5 && !autoNextFired) {
      autoNextFired = true;
      playNeighbor(1, true);
    }
    maybeSaveProgress(false);
  }

  // Клик по полосе — перемотка внутри сохранённого диапазона браузера;
  // для удалённого старого буфера или ещё не переданных данных создаём новый поток.
  if (available && ctrlBar) {
    ctrlBar.addEventListener('click', (e) => {
      if(follower)return;
 const rect = ctrlBar.getBoundingClientRect();
      const frac = (e.clientX - rect.left) / rect.width;
      const dur = totalDuration || streamStart + (player.duration || 0);
      const target = Math.max(0, Math.min(dur, frac * dur));
      completedPlayback = false;
      if (!mediaRecovery && bufferedContains(target)) {
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
    completedPlayback = false;
    dbg('seek: перезапуск потока с ' + target + 's');
    streamStart = target;
    playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, target, currentQuality, currentSubs);
  }

  // Перемотка на delta секунд: внутри переданных данных двигаем currentTime, иначе перезапуск ffmpeg.
  function seekBy(delta) {
 if(follower)return;
    if (!currentPlay || playerWrap.hidden) return;
    const dur = totalDuration || streamStart + (player.duration || 0);
    const target = Math.max(0, isFinite(dur) ? Math.min(dur, absTime() + delta) : absTime() + delta);
    completedPlayback = false;
    if (!mediaRecovery && bufferedContains(target)) {
      player.currentTime = target - streamStart;
    } else if (isFinite(target)) {
      seekTo(target);
    }
    updatePlayerUI();
  }

  // A bounded server HLS window can end before the underlying file does.
  // Open its next window without treating that temporary end as an episode end.
  function continueStream() {
    if (!currentPlay || trailerActive || pendingPlayback || completedPlayback || totalDuration <= 0) return false;
    const position = absTime();
    if (!Number.isFinite(position) || position >= totalDuration - 3) return false;
    shortContinuations = lastContinuationPosition < 0 || position > lastContinuationPosition + 5 ? 0 : shortContinuations + 1;
    lastContinuationPosition = position;
    if (shortContinuations >= maxStreamRestarts) {
      const message = 'Серверный поток завершается раньше времени. Повторите запуск.';
      playerError.textContent = message;
      playerError.hidden = false;
      stage('error', message);
      window.dispatchEvent(new CustomEvent('playbackfailure', { detail: { title: 'Просмотр прерван', message } }));
      return true;
    }
    dbg('hls: продолжение файла с ' + position.toFixed(1) + 's / ' + totalDuration.toFixed(1) + 's');
    playHls(currentPlay.id, currentPlay.magnet, currentFile, currentTrack, position, currentQuality, currentSubs);
    return true;
  }

  function toggleFullscreen() {
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => {});
    } else if (playerWrap.requestFullscreen) {
      playerWrap.requestFullscreen().catch(() => {});
    }
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
      if (continueStream()) return;
      if (follower || autoNextFired) return;
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
    player.addEventListener('canplay',()=>{if(follower)autoPlay();});
    player.addEventListener('playing', () => {
      if (trailerActive) { player.pause(); return; }
      playbackReady = true;
    });
    player.addEventListener('waiting', () => {
      if (!currentPlay || trailerActive) return;
      dbg('player: ожидание @ ' + absTime().toFixed(1) + 's'
        + ' | buffer=' + bufferedAhead().toFixed(1) + 's readyState=' + player.readyState
        + ' q=' + currentQuality + ' codec=' + (currentVideoCodec || '?'));
    });
    player.addEventListener('pause', () => maybeSaveProgress(true));
    player.addEventListener('progress', updatePlayerUI);
    player.addEventListener('play', updatePlayerUI);
    player.addEventListener('pause', updatePlayerUI);
    player.addEventListener('volumechange', updatePlayerUI);
    player.addEventListener('durationchange', updatePlayerUI);

    // ←/→ — перемотка на 5 секунд (когда курсор не в поле ввода).
    document.addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      if (e.defaultPrevented || document.querySelector('dialog[open]')) return;
      if (e.target && e.target.closest && e.target.closest('.skip-segments-panel, .skip-segments-toolbar')) return;
      const tag = ((e.target && e.target.tagName) || '').toLowerCase();
      if (tag === 'input' || tag === 'textarea' || tag === 'select') return;
      if (playerWrap.hidden || !currentPlay || trailerActive) return;
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
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'hidden') maybeSaveProgress(true, true);
    });
    window.addEventListener('pagehide', event => {
      // A cached document retains its useful MSE buffer. Keep its server stream
      // until the normal idle timeout; pageshow renews the viewing heartbeat.
      if (event.persisted) maybeSaveProgress(true, true);
      else leave();
    });
    window.addEventListener('beforeunload', () => {
      // Reload must retain the selected source in history.state. stop() is a
      // user action: its notification clears that selection on the film page.
      // pagehide releases this document's stream session without changing it.
      maybeSaveProgress(true, true);
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
    // A retry copies the local media state; only room guests inherit a host pause.
    remotePaused = follower && !!o.paused;
    closeTrailer();
    const keepAudio = currentPlay && currentPlay.id === o.id && (!o.voice || o.voice === selectedVoice);
    const initialTrack = keepAudio && currentPlay.magnet === o.magnet ? currentTrack : 0;
    const previousVoice = keepAudio ? selectedVoice : '';
    if (!keepAudio) selectedAudio = null;
    if (currentPlay) {maybeSaveProgress(true);if(currentPlay.id!==o.id)leave();}
    completedPlayback = false;
    playbackReady = false;
    lastProgressSend = 0;
    currentPlay = { id: o.id, magnet: o.magnet };
    releaseTitle = o.release || '';
    streamRestarts = 0;
    lastContinuationPosition = -1;
    shortContinuations = 0;
    autoNextFired = false;
    lastFiles = [];
    curSeason = o.season || 0;
    curEpisode = o.ep || 0;
    selectedVoice = o.voice || previousVoice || (typeof Personal !== 'undefined' ? Personal.preferences().voice : '') || '';
    autoVoice = selectedVoice;
    currentFile = (typeof o.file === 'number' && o.file >= 0) ? o.file : -1;
    durationFetch = { key: '', inflight: false };
    totalDuration = 0;
    playerWrap.hidden = false;
    // Серии текущей раздачи — для prev/next (фоном: плеер стартует сразу).
    loadFiles();
    const saved = episodeHistoryEntry(o.id, curSeason, curEpisode, o.magnet, currentFile);
    const position = o.pos != null ? o.pos : (saved ? saved.position : 0);
    beginPlayback(o.id, o.magnet, currentFile, initialTrack, position, currentQuality);
    window.dispatchEvent(new Event('playbackstart'));
    notify();
    return true;
  }

  // Остановка: гасим поток и прячем плеер (карточка фильма при этом остаётся на месте).
  function stop(opts) {
    if (sourceCheckController) sourceCheckController.abort();
    sourceCheckController = null;
    clearMediaRecovery();
    pendingPlayback = null;
 if(currentPlay)leave();
 window.dispatchEvent(new Event("playbackstop"));
    closeTrailer();
    const o = opts || {};
    if (currentPlay && !o.keepProgress) maybeSaveProgress(true);
    playbackReady = false;
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
    selectedAudio = null;
    tracksGeneration++;
    currentSubtitles = [];
    totalDuration = 0;
    currentPlay = null;
    playbackFailurePosition = null;
    completedPlayback = false;
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
    setTrailer,
    playing: () => !!currentPlay && !!playerWrap && !playerWrap.hidden && !trailerActive,
    state,
    session: () => playbackSession,
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
    ready: () => !pendingPlayback && !mediaRecovery && playbackFailurePosition == null && (playbackReady || player.readyState >= 2),
    setFollower: value => {
      follower = !!value;
      if (!follower) remotePaused = false;
      window.dispatchEvent(new CustomEvent('playbackrole', { detail: { follower } }));
    },
    isFollower: () => follower,
    duration: () => totalDuration,
    setRemotePaused: (value, message) => {
      if (!follower) {
        remotePaused = false;
        return;
      }
      remotePaused = !!value;
      if (remotePaused) {
        player.pause();
        stage(message ? 'buffering' : 'paused', message || 'Ведущий поставил просмотр на паузу.');
      } else if (follower && player.readyState >= 2 && player.paused) autoPlay();
    },
    selectSubtitle: ordinal => {
      if (currentPlay) subsSelect(currentPlay.id, currentPlay.magnet, ordinal, false);
    },
    canSeekBuffered: (target, minimumAhead = 0) => !pendingPlayback && !mediaRecovery && playbackFailurePosition == null && bufferedContains(target, minimumAhead),
    seekBuffered: target => {
      if (!currentPlay || pendingPlayback || mediaRecovery || playbackFailurePosition != null || !bufferedContains(target)) return false;
      completedPlayback = false;
      player.currentTime = target - streamStart;
      return true;
    },
    seek: target => {
      completedPlayback = false;
      if (!mediaRecovery && bufferedContains(target)) player.currentTime = target - streamStart;
      else seekTo(target);
    },
    skipSegment: target => {
      if (follower || !currentPlay || !Number.isFinite(target) || target < 0) return false;
      if (totalDuration > 0 && Math.abs(target - totalDuration) < 0.01) {
        // Starting ffmpeg at EOF cannot yield a playable frame. Complete the
        // current episode explicitly, keeping history at its real end.
        completedPlayback = true;
        autoNextFired = true;
        player.pause();
        maybeSaveProgress(true);
        updatePlayerUI();
        playNeighbor(1, true);
        return true;
      }
      completedPlayback = false;
      if (!mediaRecovery && bufferedContains(target)) player.currentTime = target - streamStart;
      else seekTo(target);
      return false;
    },
    version: 2,
  };
})();

// Смена языка — перерисовываем подписи плеера (поток не трогаем).
if (typeof onLang === 'function') onLang(() => PP.relabel());

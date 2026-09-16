// IPTV: каналы (список/группы/поиск), live-плеер и телепрограмма. Поток всегда идёт
// через наш сервер (/api/iptv/...) — адресов и логинов провайдера в браузере нет.
// Раздел работает и на ТВ-приставке: навигацию по пульту обеспечивает tv.js.
(function () {
  'use strict';

  var TEXT = {
    ru: {
      title: 'IPTV',
      search: 'Поиск канала...',
      all: 'Все каналы',
      empty: 'Каналов нет. Откройте ⚙ и добавьте плейлист (M3U-ссылка или Xtream Codes).',
      notFound: 'Ничего не найдено.',
      loading: 'Загружаю каналы…',
      noEpg: 'Программа передач недоступна.',
      refresh: 'Обновить список',
      admin: 'Плейлисты',
      epgButton: 'Программа',
      playlists: 'Плейлисты IPTV',
      save: 'Сохранить и обновить',
      sync: '⟳',
      del: 'Удалить',
      saved: 'Плейлист сохранён, каналы обновлены',
      syncAll: '🕒 Обновить программу',
      adminOnly: 'Управление плейлистами доступно только администратору.',
      on: 'Включить',
      off: 'Выключить',
      disabled: 'выключен',
      channels: 'каналов',
      streamError: 'Не удалось открыть канал: ',
      remuxing: 'Перепаковываю поток на сервере (звук провайдера не поддерживается браузером)…',
      noHls: 'Этот браузер не умеет играть HLS-потоки.',
      stop: 'Стоп'
    },
    en: {
      title: 'IPTV',
      search: 'Search channel...',
      all: 'All channels',
      empty: 'No channels. Open ⚙ and add a playlist (M3U URL or Xtream Codes).',
      notFound: 'Nothing found.',
      loading: 'Loading channels…',
      noEpg: 'Program guide is unavailable.',
      refresh: 'Refresh list',
      admin: 'Playlists',
      epgButton: 'Guide',
      playlists: 'IPTV playlists',
      save: 'Save and refresh',
      sync: '⟳',
      del: 'Delete',
      saved: 'Playlist saved, channels refreshed',
      syncAll: '🕒 Refresh guide',
      adminOnly: 'Playlist management is available to admins only.',
      on: 'Enable',
      off: 'Disable',
      disabled: 'off',
      channels: 'channels',
      streamError: 'Cannot open channel: ',
      remuxing: 'Repacking the stream on the server (browser cannot decode the provider audio)…',
      noHls: 'This browser cannot play HLS streams.',
      stop: 'Stop'
    }
  };

  function curLang() {
    try {
      if (typeof lang !== 'undefined' && lang) return lang;
    } catch (e) { /* noop */ }
    return localStorage.getItem('lang') || 'ru';
  }

  function t(key) {
    var d = TEXT[curLang()] || TEXT.ru;
    return d[key] || key;
  }

  // GROUP_RU — русские подписи категорий; фильтр на сервере идёт по ИСХОДНОМУ
  // (английскому) имени, незнакомые группы остаются как есть.
  var GROUP_RU = {
    general: 'Общие', entertainment: 'Развлекательные', movies: 'Кино',
    series: 'Сериалы', documentary: 'Документальные', news: 'Новости',
    sports: 'Спорт', kids: 'Детские', family: 'Семейные', music: 'Музыка',
    culture: 'Культура', science: 'Наука', education: 'Образование',
    cooking: 'Кулинария', lifestyle: 'Стиль жизни', travel: 'Путешествия',
    outdoor: 'Природа и отдых', relax: 'Релакс', religious: 'Религиозные',
    shop: 'Магазины', auto: 'Авто', business: 'Бизнес', classic: 'Классика',
    animation: 'Анимация', comedy: 'Юмор', legislative: 'Парламентские',
    weather: 'Погода', interactive: 'Интерактивные', public: 'Общественные',
    undefined: 'Без категории', adult: 'Для взрослых', xxx: 'Для взрослых',
    nature: 'Природа', animals: 'Животные', agriculture: 'Агро',
    fishing: 'Рыбалка', hunting: 'Охота', history: 'История',
    health: 'Здоровье', tech: 'Технологии', regional: 'Региональные',
    local: 'Местные', shopping: 'Телепокупки', religion: 'Религия'
  };

  function groupLabel(name) {
    var n = String(name || '').trim();
    if (!n) return n;
    if (curLang() !== 'ru') return n;
    var key = n.toLowerCase();
    if (GROUP_RU[key]) return GROUP_RU[key];
    // Составные группы («Nature;Documentary») переводим по частям.
    if (n.indexOf(';') >= 0) {
      return n.split(';').map(function (p) { return groupLabel(p); }).join('; ');
    }
    return n;
  }

  var $ = function (id) { return document.getElementById(id); };

  var state = {
    channels: [],
    groups: [],
    playlists: [],
    group: '',
    query: '',
    chan: null,
    hls: null,
    playing: false,
    isAdmin: false,
    loading: false
  };

  var view, grid, groupsEl, emptyEl, searchEl, refreshEl, adminOpenEl, closeEl;
  var playerWrap, videoEl, playBtn, logoEl, nameEl, nowEl, volEl, muteBtn, noteEl;
  var epgEl, epgTitleEl, epgListEl, adminEl, adminListEl, formEl;

  // ---- Раздел целиком ----

  function isOpen() { return view && !view.hidden; }

  function open() {
    if (!view) return;
    view.hidden = false;
    document.body.classList.add('iptv-on');
    if (!state.channels.length) loadChannels();
    focusFirst();
  }

  function close() {
    stop();
    hidePanels();
    if (view) view.hidden = true;
    document.body.classList.remove('iptv-on');
  }

  function focusFirst() {
    var first = grid && grid.querySelector('.iptv-card');
    if (first && document.body.classList.contains('tv')) first.focus();
  }

  function hidePanels() {
    if (epgEl) epgEl.hidden = true;
    if (adminEl) adminEl.hidden = true;
  }

  // ---- Список каналов ----

  function loadChannels() {
    if (state.loading) return;
    state.loading = true;
    if (emptyEl) {
      emptyEl.hidden = false;
      emptyEl.textContent = t('loading');
    }
    var q = [];
    if (state.group) q.push('group=' + encodeURIComponent(state.group));
    if (state.query) q.push('q=' + encodeURIComponent(state.query));
    // Каналы не листаем: поиск и группы фильтруются на сервере, 2000 хватает с запасом.
    q.push('limit=2000');
    fetch('/api/iptv/channels?' + q.join('&'))
      .then(function (r) { return r.json(); })
      .then(function (data) {
        state.channels = data.channels || [];
        state.groups = data.groups || [];
        state.playlists = data.playlists || [];
        renderGroups();
        renderChannels();
      })
      .catch(function (e) {
        if (emptyEl) {
          emptyEl.hidden = false;
          emptyEl.textContent = 'Ошибка: ' + e.message;
        }
      })
      .then(function () { state.loading = false; });
  }

  function renderGroups() {
    if (!groupsEl) return;
    groupsEl.innerHTML = '';
    var total = state.channels.length;
    var all = document.createElement('button');
    all.type = 'button';
    all.className = 'chip' + (state.group === '' ? ' active' : '');
    all.textContent = t('all') + (state.group === '' ? ' (' + total + ')' : '');
    all.addEventListener('click', function () {
      state.group = '';
      loadChannels();
    });
    groupsEl.appendChild(all);
    state.groups.forEach(function (g) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'chip' + (state.group === g.name ? ' active' : '');
      b.textContent = groupLabel(g.name) + ' (' + g.count + ')';
      b.title = g.name;
      b.addEventListener('click', function () {
        state.group = g.name;
        loadChannels();
      });
      groupsEl.appendChild(b);
    });
  }

  function renderChannels() {
    if (!grid) return;
    grid.innerHTML = '';
    if (!state.channels.length) {
      if (emptyEl) {
        emptyEl.hidden = false;
        emptyEl.textContent = state.query || state.group ? t('notFound') : t('empty');
      }
      return;
    }
    if (emptyEl) emptyEl.hidden = true;
    state.channels.forEach(function (ch) {
      grid.appendChild(channelCard(ch));
    });
  }

  function channelCard(ch) {
    var card = document.createElement('div');
    card.className = 'card iptv-card';
    card.tabIndex = 0;
    card.setAttribute('role', 'button');

    if (ch.logo) {
      var img = document.createElement('img');
      img.className = 'iptv-logo';
      img.loading = 'lazy';
      img.alt = '';
      img.src = ch.logo;
      img.addEventListener('error', function () { img.hidden = true; });
      card.appendChild(img);
    }

    var body = document.createElement('div');
    body.className = 'iptv-card-body';

    var name = document.createElement('div');
    name.className = 'iptv-cname';
    name.textContent = ch.name || ('#' + ch.num);
    // Полное имя — в title: в карточке название обрезано двумя строками.
    name.title = ch.name_original || ch.name || '';
    body.appendChild(name);

    var now = document.createElement('div');
    now.className = 'iptv-now';
    // Без телепрограммы строку не занимаем: категория канала и так видна
    // бейджем ниже.
    now.textContent = ch.now ? '▶ ' + ch.now.title : '';
    body.appendChild(now);

    if (ch.now) {
      var bar = document.createElement('div');
      bar.className = 'iptv-progress';
      var fill = document.createElement('i');
      fill.style.width = progressPct(ch.now) + '%';
      bar.appendChild(fill);
      body.appendChild(bar);
    }
    if (ch.next) {
      var next = document.createElement('div');
      next.className = 'iptv-next';
      next.textContent = '⏭ ' + hhmm(ch.next.start) + ' ' + ch.next.title;
      body.appendChild(next);
    }
    if (ch.group && !ch.now) {
      var badge = document.createElement('span');
      badge.className = 'iptv-badge';
      badge.textContent = groupLabel(ch.group);
      body.appendChild(badge);
    }
    card.appendChild(body);

    card.addEventListener('click', function () { play(ch); });
    card.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        play(ch);
      }
    });
    return card;
  }

  function progressPct(p) {
    var s = Date.parse(p.start), e = Date.parse(p.stop);
    if (!(s && e && e > s)) return 0;
    return Math.max(0, Math.min(100, ((Date.now() - s) / (e - s)) * 100));
  }

  function hhmm(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return '';
    return ('0' + d.getHours()).slice(-2) + ':' + ('0' + d.getMinutes()).slice(-2);
  }

  // ---- Плеер ----

  function play(ch) {
    stop();
    state.chan = ch;
    if (!playerWrap) return;
    playerWrap.hidden = false;
    if (nameEl) {
      nameEl.textContent = ch.name || '';
      nameEl.title = ch.name_original || ch.name || '';
    }
    if (nowEl) nowEl.textContent = ch.now ? ch.now.title : '';
    if (logoEl) {
      if (ch.logo) {
        logoEl.src = ch.logo;
        logoEl.hidden = false;
      } else {
        logoEl.hidden = true;
      }
    }
    if (noteEl) noteEl.hidden = true;
    state.remux = false;

    var url = ch.play_url || ('/api/iptv/play/' + ch.id + '.m3u8');
    startHls(url, ch);
    state.playing = true;
    updatePlayBtn();
    loadEpgInline(ch);
    if (document.body.classList.contains('tv')) {
      if (playBtn) playBtn.focus();
    }
  }

  // startHls запускает канал; remux — повторный запуск через серверную перепаковку ffmpeg.
  function startHls(url, ch, remux) {
    if (state.hls) {
      try { state.hls.destroy(); } catch (e) { /* noop */ }
      state.hls = null;
    }
    // Ошибка декодера «залипает» в элементе: без сброса src следующий поток не играет.
    if (videoEl) {
      try { videoEl.pause(); } catch (e) { /* noop */ }
      videoEl.removeAttribute('src');
      try { videoEl.load(); } catch (e) { /* noop */ }
    }
    if (!window.Hls || !Hls.isSupported()) {
      if (videoEl && videoEl.canPlayType('application/vnd.apple.mpegurl')) {
        // Safari/iOS: HLS играется нативно.
        videoEl.src = url;
        videoEl.play().catch(function () {});
        return;
      }
      showNote(t('noHls'));
      return;
    }
    var hls = new Hls({
      // Живой поток: держим небольшой буфер у «края», перемотка не нужна.
      lowLatencyMode: false,
      liveSyncDurationCount: 3,
      maxBufferLength: 20,
      backBufferLength: 30,
      manifestLoadingTimeOut: 20000,
      manifestLoadingMaxRetry: 4,
      fragLoadingMaxRetry: 8
    });
    hls.on(Hls.Events.ERROR, function (_e, data) {
      if (!data || !data.fatal) return;
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
        hls.startLoad(); // поток мог перезапуститься после бездействия
        return;
      }
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
        // Звук провайдера (AC-3/E-AC-3 в TS) браузер не декодирует — просим серверный remux;
        // делаем один раз на канал, иначе есть риск зациклиться.
        if (!remux) {
          state.remux = true;
          showNote(t('remuxing'));
          setTimeout(function () { startHls(url + '?remux=1', ch, true); }, 800);
          return;
        }
        hls.recoverMediaError();
        return;
      }
      showNote(t('streamError') + (data.details || ''));
    });
    hls.loadSource(url);
    hls.attachMedia(videoEl);
    hls.on(Hls.Events.MANIFEST_PARSED, function () {
      videoEl.play().catch(function () { /* автоплей может быть запрещён */ });
      if (state.remux && noteEl) noteEl.hidden = true;
    });
    state.hls = hls;
  }

  function stop() {
    if (state.hls) {
      try { state.hls.destroy(); } catch (e) { /* noop */ }
      state.hls = null;
    }
    if (videoEl) {
      try { videoEl.pause(); } catch (e) { /* noop */ }
      videoEl.removeAttribute('src');
      try { videoEl.load(); } catch (e) { /* noop */ }
    }
    state.playing = false;
    state.chan = null;
    state.remux = false;
    if (playerWrap) playerWrap.hidden = true;
    if (epgEl) epgEl.hidden = true;
  }

  function togglePlay() {
    if (!videoEl || !state.chan) return;
    if (videoEl.paused) {
      videoEl.play().catch(function () {});
      state.playing = true;
    } else {
      videoEl.pause();
      state.playing = false;
    }
    updatePlayBtn();
  }

  function updatePlayBtn() {
    if (!playBtn) return;
    playBtn.textContent = state.playing && videoEl && !videoEl.paused ? '⏸' : '▶';
  }

  function showNote(msg) {
    if (!noteEl) return;
    noteEl.hidden = false;
    noteEl.textContent = msg;
  }

  function toggleFullscreen() {
    var el = playerWrap;
    if (!el) return;
    if (document.fullscreenElement) {
      document.exitFullscreen();
    } else if (el.requestFullscreen) {
      el.requestFullscreen();
    } else if (el.webkitRequestFullscreen) {
      el.webkitRequestFullscreen();
    } else if (videoEl && videoEl.webkitEnterFullscreen) {
      videoEl.webkitEnterFullscreen(); // iOS
    }
  }

  function loadEpgInline(ch) {
    if (!ch || !ch.id) return;
    if (nowEl && !ch.now) nowEl.textContent = t('noEpg');
    if (ch.now && ch.next) {
      if (nowEl) nowEl.textContent = ch.now.title + '  ·  ⏭ ' + hhmm(ch.next.start) + ' ' + ch.next.title;
    }
  }

  // ---- Программа передач ----

  function openEpg(ch) {
    if (!epgEl || !ch) return;
    epgEl.hidden = false;
    if (epgTitleEl) epgTitleEl.textContent = ch.name || '';
    if (epgListEl) {
      epgListEl.innerHTML = '';
      epgListEl.textContent = t('loading');
    }
    fetch('/api/iptv/epg?channel=' + encodeURIComponent(ch.id) + '&hours=12')
      .then(function (r) { return r.json(); })
      .then(function (data) {
        var items = data.programs || [];
        if (!epgListEl) return;
        epgListEl.innerHTML = '';
        if (!items.length) {
          epgListEl.textContent = t('noEpg');
          return;
        }
        items.forEach(function (p) {
          var row = document.createElement('div');
          row.className = 'iptv-epg-item';
          var now = false;
          try {
            now = Date.parse(p.start) <= Date.now() && Date.parse(p.stop) > Date.now();
          } catch (e) { /* noop */ }
          if (now) row.className += ' now';
          var time = document.createElement('span');
          time.className = 'iptv-epg-time';
          time.textContent = hhmm(p.start) + '–' + hhmm(p.stop);
          row.appendChild(time);
          var box = document.createElement('span');
          var title = document.createElement('div');
          title.textContent = p.title || '';
          box.appendChild(title);
          if (p.desc) {
            var desc = document.createElement('div');
            desc.className = 'iptv-epg-desc';
            desc.textContent = p.desc;
            box.appendChild(desc);
          }
          row.appendChild(box);
          epgListEl.appendChild(row);
        });
      })
      .catch(function (e) {
        if (epgListEl) epgListEl.textContent = 'Ошибка: ' + e.message;
      });
  }

  // ---- Плейлисты (админ) ----

  function loadPlaylists() {
    if (!adminListEl) return;
    fetch('/api/iptv/playlists')
      .then(function (r) {
        if (r.status === 401 || r.status === 403) throw new Error(t('adminOnly'));
        return r.json();
      })
      .then(function (data) {
        var list = data.playlists || [];
        adminListEl.innerHTML = '';
        if (!list.length) {
          var p = document.createElement('p');
          p.className = 'iptv-note';
          p.textContent = t('empty');
          adminListEl.appendChild(p);
          return;
        }
        list.forEach(function (pl) {
          var row = document.createElement('div');
          row.className = 'iptv-admin-row' + (pl.enabled === false ? ' off' : '');
          var name = document.createElement('span');
          name.className = 'iptv-admin-name';
          name.textContent = pl.name + ' · ' + (pl.channels || 0) + ' ' + t('channels') + (pl.has_epg ? ' · EPG' : '')
            + (pl.enabled === false ? ' · ' + t('disabled') : '');
          row.appendChild(name);
          if (pl.last_error) {
            var err = document.createElement('span');
            err.className = 'iptv-admin-err';
            err.textContent = pl.last_error;
            row.appendChild(err);
          }
          // Выключенный плейлист не отдаёт каналы и не синкается, но остаётся в базе.
          var power = document.createElement('button');
          power.type = 'button';
          power.className = 'auth-btn';
          power.textContent = pl.enabled === false ? '⭘' : '⏻';
          power.title = pl.enabled === false ? t('on') : t('off');
          power.addEventListener('click', function () {
            power.disabled = true;
            fetch('/api/iptv/playlists/' + pl.id + '/enabled', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ enabled: pl.enabled === false })
            })
              .then(function (r) { if (!r.ok) return r.text().then(function (x) { throw new Error(x); }); })
              .then(function () { loadPlaylists(); loadChannels(); })
              .catch(function (e) { alert('Ошибка: ' + e.message); power.disabled = false; });
          });
          row.appendChild(power);
          var sync = document.createElement('button');
          sync.type = 'button';
          sync.className = 'auth-btn';
          sync.textContent = t('sync');
          sync.title = 'Обновить';
          sync.addEventListener('click', function () {
            sync.disabled = true;
            fetch('/api/iptv/playlists/' + pl.id + '/sync', { method: 'POST' })
              .then(function (r) { return r.ok ? r.json() : r.text().then(function (x) { throw new Error(x); }); })
              .then(function () { loadPlaylists(); loadChannels(); })
              .catch(function (e) { alert('Ошибка обновления: ' + e.message); })
              .then(function () { sync.disabled = false; });
          });
          row.appendChild(sync);
          var del = document.createElement('button');
          del.type = 'button';
          del.className = 'auth-btn';
          del.textContent = '✕';
          del.title = t('del');
          del.addEventListener('click', function () {
            if (!confirm('Удалить плейлист «' + pl.name + '»?')) return;
            fetch('/api/iptv/playlists/' + pl.id, { method: 'DELETE' })
              .then(function () { loadPlaylists(); loadChannels(); });
          });
          row.appendChild(del);
          adminListEl.appendChild(row);
        });
      })
      .catch(function (e) {
        adminListEl.innerHTML = '';
        var p = document.createElement('p');
        p.className = 'iptv-note';
        p.textContent = e.message;
        adminListEl.appendChild(p);
      });
  }

  function submitPlaylist(ev) {
    ev.preventDefault();
    var body = {
      name: $('iptv-f-name').value.trim() || 'IPTV',
      kind: $('iptv-f-kind').value,
      url: $('iptv-f-url').value.trim(),
      username: $('iptv-f-user').value.trim(),
      password: $('iptv-f-pass').value,
      epg_url: $('iptv-f-epg').value.trim(),
      enabled: true
    };
    if (!body.url) return;
    fetch('/api/iptv/playlists', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    })
      .then(function (r) {
        if (!r.ok) return r.text().then(function (x) { throw new Error(x); });
        return r.json();
      })
      .then(function (res) {
        if (res && res.warning) alert('Плейлист сохранён, но обновление не удалось: ' + res.warning);
        $('iptv-f-name').value = '';
        $('iptv-f-url').value = '';
        $('iptv-f-user').value = '';
        $('iptv-f-pass').value = '';
        $('iptv-f-epg').value = '';
        loadPlaylists();
        loadChannels();
      })
      .catch(function (e) { alert('Ошибка: ' + e.message); });
  }

  function checkAdmin() {
    fetch('/api/auth/me')
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) {
        // Ответ auth-сервиса: {user: {role}} либо {user: null}.
        var role = d && d.user && d.user.role;
        state.isAdmin = role === 'admin';
        if (adminOpenEl) adminOpenEl.hidden = !state.isAdmin;
      })
      .catch(function () { /* гость — управление скрыто */ });
  }

  // ---- Локализация подсказок (сам текст в HTML переводит app.js) ----

  function applyLang() {
    if (searchEl) searchEl.placeholder = t('search');
    if (refreshEl) refreshEl.title = t('refresh');
    if (adminOpenEl) adminOpenEl.title = t('admin');
    var epgBtn = $('iptv-epg-open');
    if (epgBtn) epgBtn.title = t('epgButton');
    if (groupsEl && state.groups.length) renderGroups();
  }

  // ---- Инициализация ----

  function init() {
    view = $('iptv-view');
    if (!view) return;
    grid = $('iptv-grid');
    groupsEl = $('iptv-groups');
    emptyEl = $('iptv-empty');
    searchEl = $('iptv-search');
    refreshEl = $('iptv-refresh');
    adminOpenEl = $('iptv-admin-open');
    closeEl = $('iptv-close');
    playerWrap = $('iptv-player-wrap');
    videoEl = $('iptv-player');
    playBtn = $('iptv-play');
    logoEl = $('iptv-plogo');
    nameEl = $('iptv-pname');
    nowEl = $('iptv-pnow');
    volEl = $('iptv-pvolume');
    muteBtn = $('iptv-pmute');
    noteEl = $('iptv-pnote');
    epgEl = $('iptv-epg');
    epgTitleEl = $('iptv-epg-title');
    epgListEl = $('iptv-epg-list');
    adminEl = $('iptv-admin');
    adminListEl = $('iptv-admin-list');
    formEl = $('iptv-form');

    var openBtn = $('iptv-open');
    if (openBtn) openBtn.addEventListener('click', function () { isOpen() ? close() : open(); });
    if (closeEl) closeEl.addEventListener('click', close);
    if (refreshEl) {
      refreshEl.addEventListener('click', function () {
        state.channels = [];
        loadChannels();
      });
    }
    if (searchEl) {
      var timer = null;
      searchEl.addEventListener('input', function () {
        clearTimeout(timer);
        timer = setTimeout(function () {
          state.query = searchEl.value.trim();
          loadChannels();
        }, 350);
      });
    }
    if (playBtn) playBtn.addEventListener('click', togglePlay);
    if (videoEl) {
      videoEl.addEventListener('play', updatePlayBtn);
      videoEl.addEventListener('pause', updatePlayBtn);
      videoEl.addEventListener('click', togglePlay);
    }
    if (muteBtn) {
      muteBtn.addEventListener('click', function () {
        if (!videoEl) return;
        videoEl.muted = !videoEl.muted;
        muteBtn.textContent = videoEl.muted ? '🔇' : '🔊';
      });
    }
    if (volEl) {
      volEl.addEventListener('input', function () {
        if (videoEl) videoEl.volume = parseFloat(volEl.value);
      });
    }
    var fullBtn = $('iptv-pfull');
    if (fullBtn) fullBtn.addEventListener('click', toggleFullscreen);
    var pclose = $('iptv-pclose');
    if (pclose) pclose.addEventListener('click', stop);
    var pepg = $('iptv-pepg');
    if (pepg) {
      pepg.addEventListener('click', function () {
        if (epgEl && !epgEl.hidden) {
          epgEl.hidden = true;
          return;
        }
        openEpg(state.chan);
      });
    }
    var epgClose = $('iptv-epg-close');
    if (epgClose) epgClose.addEventListener('click', function () { epgEl.hidden = true; });
    if (adminOpenEl) {
      adminOpenEl.addEventListener('click', function () {
        if (!adminEl) return;
        adminEl.hidden = !adminEl.hidden;
        if (!adminEl.hidden) loadPlaylists();
      });
    }
    var adminClose = $('iptv-admin-close');
    if (adminClose) adminClose.addEventListener('click', function () { adminEl.hidden = true; });
    if (formEl) formEl.addEventListener('submit', submitPlaylist);

    // ТВ-пульт жмёт #ctrl-play (кнопка плеера фильмов) — в IPTV перехватываем в capture-фазе.
    var ctrlPlay = $('ctrl-play');
    if (ctrlPlay) {
      ctrlPlay.addEventListener('click', function (e) {
        if (state.chan) {
          e.stopPropagation();
          togglePlay();
        }
      }, true);
    }

    checkAdmin();
    applyLang();
    document.addEventListener('vv:lang', applyLang);
  }

  window.VideoViewerIPTV = {
    isOpen: isOpen,
    open: open,
    close: close,
    play: play,
    stop: stop,
    version: 1
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();

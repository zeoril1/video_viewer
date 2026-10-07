'use strict';

/* TV-режим (Android TV / пульт): включается при ?tv=1 (нативная обёртка), localStorage
 * vv_tv=1 или маркере VVTV/1.0 в user-agent. D-pad двигает фокус, Enter/OK — клик,
 * Back — window.__vvBack(): закрыть оверлей, иначе шаг назад по истории.
 * Сайт многостраничный (каталог → фильм → просмотр), поэтому «назад» — переход
 * на предыдущую страницу, а выход из приложения — только из каталога. */
(function () {
  'use strict';
  if (typeof window === 'undefined') return;

  // Оверлеи-панели: открылись — фокус внутрь, Back — закрыть.
  var MODALS = ['#iptv-epg', '#iptv-admin'];

  // ---- Детект TV-режима --------------------------------------------------
  function detectedByUrl() {
    try { return new URLSearchParams(window.location.search).get('tv') === '1'; }
    catch (e) { return false; }
  }
  function detectedByStorage() {
    try { return window.localStorage.getItem('vv_tv') === '1'; }
    catch (e) { return false; }
  }
  function detectedByUA() {
    return /VVTV\/|Android TV|SMART-TV/i.test(window.navigator.userAgent || '');
  }
  function isTV() { return detectedByUrl() || detectedByStorage() || detectedByUA(); }

  var enabled = false;
  var tvFocus = null;          // последний элемент, куда ставили фокус
  var wasModalOpen = false;
  var wasPlayerOpen = false;

  // Кликабельные/фокусируемые элементы.
  var SEL = [
    'a[href]', 'button', 'select', 'textarea', 'input:not([type=hidden])',
    '.card', '.continue-card', '.chip', '.ep-btn', '.sec-drop-item',
    '.resume-btn', '.watch-btn', '.q-btn',
    '[role="button"]', '[tabindex]:not([tabindex="-1"])'
  ].join(',');

  function $(s, root) { return (root || document).querySelector(s); }
  function playerWrapEl() { return document.getElementById('player-wrap'); }
  function playerOpen() { var p = playerWrapEl(); return !!(p && !p.hidden); }

  // Самый верхний (последний в DOM среди видимых) открытый модал.
  function topModal() {
    var found = null;
    for (var i = 0; i < MODALS.length; i++) {
      var m = document.querySelector(MODALS[i]);
      if (m && !m.hidden) found = m;
    }
    return found;
  }

  function isVisible(el) {
    if (!el || !el.getBoundingClientRect) return false;
    if (el.hidden) return false;
    var n = el;
    while (n && n !== document) {
      if (n.hidden) return false;
      n = n.parentNode;
    }
    var cs = window.getComputedStyle(el);
    if (!cs || cs.display === 'none' || cs.visibility === 'hidden') return false;
    var r = el.getBoundingClientRect();
    if (!r || (r.width < 2 && r.height < 2)) return false;
    return true;
  }

  function getCandidates(container) {
    var out = [];
    var els = container.querySelectorAll(SEL);
    for (var i = 0; i < els.length; i++) {
      if (isVisible(els[i])) out.push(els[i]);
    }
    return out;
  }

  function center(r) { return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; }
  function overlap(a0, a1, b0, b1) { return Math.max(0, Math.min(a1, b1) - Math.max(a0, b0)); }

  // «Стоимость» перехода в направлении dir. Infinity — если цель не в ту сторону.
  function score(fromR, toR, dir) {
    var fc = center(fromR), tc = center(toR);
    var dx = tc.x - fc.x, dy = tc.y - fc.y;
    var dist, perp;
    if (dir === 'left' || dir === 'right') {
      if (dir === 'left' && dx >= 0) return Infinity;
      if (dir === 'right' && dx <= 0) return Infinity;
      dist = Math.abs(dx);
      perp = overlap(fromR.top, fromR.bottom, toR.top, toR.bottom) / Math.max(1, fromR.height);
    } else {
      if (dir === 'up' && dy >= 0) return Infinity;
      if (dir === 'down' && dy <= 0) return Infinity;
      dist = Math.abs(dy);
      perp = overlap(fromR.left, fromR.right, toR.left, toR.right) / Math.max(1, fromR.width);
    }
    return dist + (1 - perp) * 400; // штраф за слабое перекрытие по перпендикулярной оси
  }

  function nearest(el, dir, list) {
    var fromR = el.getBoundingClientRect();
    var best = null, bestS = Infinity;
    for (var i = 0; i < list.length; i++) {
      var c = list[i];
      if (c === el) continue;
      var r = c.getBoundingClientRect();
      if (r.width < 2 || r.height < 2) continue;
      var s = score(fromR, r, dir);
      if (s < bestS) { bestS = s; best = c; }
    }
    return best;
  }

  // «Заворот» на краю ряда/колонки (как на ТВ-пультах).
  function wrap(el, dir, list) {
    var er = el.getBoundingClientRect();
    var best = null, bestK = -Infinity;
    for (var i = 0; i < list.length; i++) {
      var c = list[i];
      if (c === el) continue;
      var r = c.getBoundingClientRect();
      if (r.width < 2 || r.height < 2) continue;
      if (dir === 'left' || dir === 'right') {
        var rowOv = Math.min(er.bottom, r.bottom) - Math.max(er.top, r.top);
        if (rowOv < 0.3 * Math.min(er.height, r.height)) continue;
        var k = dir === 'right' ? r.left : -r.right;
        if (k > bestK) { bestK = k; best = c; }
      } else {
        var colOv = Math.min(er.right, r.right) - Math.max(er.left, r.left);
        if (colOv < 0.3 * Math.min(er.width, r.width)) continue;
        var kk = dir === 'down' ? r.top : -r.bottom;
        if (kk > bestK) { bestK = kk; best = c; }
      }
    }
    return best;
  }

  function isNativeInteractive(el) {
    if (!el) return false;
    var tag = (el.tagName || '').toUpperCase();
    if (tag === 'BUTTON' || tag === 'A' || tag === 'INPUT' ||
        tag === 'SELECT' || tag === 'TEXTAREA' || tag === 'VIDEO' || tag === 'AUDIO') return true;
    var role = el.getAttribute('role');
    return role === 'button' || role === 'tab';
  }

  function makeFocusable(el) {
    if (!el || isNativeInteractive(el)) return;
    if (!el.hasAttribute('tabindex')) el.setAttribute('tabindex', '0');
  }

  function focusEl(el) {
    if (!el) return;
    makeFocusable(el);
    tvFocus = el;
    try { el.focus({ preventScroll: true }); } catch (e) { el.focus(); }
    if (el.scrollIntoView) {
      try { el.scrollIntoView({ block: 'nearest', inline: 'nearest' }); } catch (e2) { el.scrollIntoView(); }
    }
  }

  // Разумная «точка входа» в контейнере при первом нажатии стрелки.
  function defaultTarget(container, dir) {
    if (!container || container === document.body) {
      var pw = playerWrapEl();
      if (pw && !pw.hidden) {
        var play = document.getElementById('ctrl-play');
        if (play && isVisible(play)) return play;
      }
      if (dir === 'up' || dir === 'left') {
        var act = document.querySelector('.sections .section-btn.active');
        if (act && isVisible(act)) return act;
        var first = document.querySelector('.sections .section-btn');
        if (first && isVisible(first)) return first;
      }
      var card = document.querySelector('.grid .card');
      if (card && isVisible(card)) return card;
      var cont = document.querySelector('.continue .continue-card');
      if (cont && isVisible(cont)) return cont;
      var any = document.querySelector('.card, .section-btn, .chip, .ep-btn, .resume-btn, .watch-btn');
      return (any && isVisible(any)) ? any : null;
    }
    if (container.id === 'iptv-admin') {
      var ab = $('#iptv-f-name', container);
      if (ab) return ab;
    }
    var b = $('button, .close', container);
    return (b && isVisible(b)) ? b : null;
  }

  function moveFocus(dir, fromEl) {
    var modal = topModal();
    var container = modal || document.body;

    var from = null;
    if (fromEl && isVisible(fromEl)) from = fromEl;
    else if (tvFocus && isVisible(tvFocus)) from = tvFocus;
    else if (document.activeElement && document.activeElement !== document.body &&
             document.activeElement !== document.documentElement && isVisible(document.activeElement)) {
      from = document.activeElement;
    }
    if (!from) {
      var t0 = defaultTarget(container, dir);
      if (t0) focusEl(t0);
      return;
    }
    var list = getCandidates(container);
    if (!list.length) return;
    var t = nearest(from, dir, list);
    if (!t) t = wrap(from, dir, list);
    if (t && t !== from) focusEl(t);
  }

  function dirOf(k) {
    if (k === 'ArrowUp' || k === 'Up') return 'up';
    if (k === 'ArrowDown' || k === 'Down') return 'down';
    if (k === 'ArrowLeft' || k === 'Left') return 'left';
    if (k === 'ArrowRight' || k === 'Right') return 'right';
    return null;
  }

  var CODE2KEY = { 37: 'ArrowLeft', 38: 'ArrowUp', 39: 'ArrowRight', 40: 'ArrowDown', 13: 'Enter', 32: 'Space' };
  function keyOf(e) {
    var k = e.key;
    if (k === ' ' || k === 'Spacebar') return 'Space';
    if (k && k !== 'Unidentified' && k.length) return k;
    return CODE2KEY[e.keyCode] || '';
  }

  function isTyping(el) {
    if (!el) return false;
    var tag = (el.tagName || '').toUpperCase();
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable;
  }

  function inControls(el) {
    return !!(el && el.closest && el.closest('#player-controls, .skip-segments-panel, .skip-segments-toolbar'));
  }

  function onKeyDown(e) {
    if (!enabled) return;
    var t = e.target;
    var k = keyOf(e);
    var dir = dirOf(k);
    if (!dir && k !== 'Enter' && k !== 'Space') return;
    // В текстовых полях (поиск, логин) стрелки/Enter отдаём браузеру/IME.
    if (isTyping(t)) return;

    var pOpen = playerOpen();

    // Видео играет: ←/→ = перемотка (её делает watch.js), если фокус не в панели.
    if (pOpen && dir && (dir === 'left' || dir === 'right')) {
      if (!inControls(t)) return; // перемотка
      e.preventDefault(); e.stopPropagation();
      moveFocus(dir, t);
      return;
    }
    // ↑/↓ при открытом плеере — попасть в панель управления (play/pause и т.п.).
    if (pOpen && dir && (dir === 'up' || dir === 'down')) {
      e.preventDefault(); e.stopPropagation();
      if (!inControls(t)) {
        var play = document.getElementById('ctrl-play');
        if (play && isVisible(play)) { focusEl(play); return; }
      }
      moveFocus(dir, t);
      return;
    }

    if (k === 'Enter' || k === 'Space') {
      // Нативные кнопки/ссылки браузер активирует сам.
      if (isNativeInteractive(t)) return;
      e.preventDefault(); e.stopPropagation();
      if (t && typeof t.click === 'function') t.click();
      return;
    }

    e.preventDefault();
    e.stopPropagation();
    moveFocus(dir, t);
  }

  // ---- Реакция на открытие/закрытие оверлеев и плеера --------------------
  function onDomChange() {
    var mOpen = !!topModal();
    var pOpen = playerOpen();
    if (mOpen && !wasModalOpen) {
      var modal = topModal();
      var t = defaultTarget(modal, 'down');
      if (t) focusEl(t);
    } else if (!mOpen && wasModalOpen) {
      // Вернуться к контенту за оверлеем (если элемент ещё жив).
      if (tvFocus && isVisible(tvFocus)) {
        focusEl(tvFocus);
      } else {
        var c = document.querySelector('.grid .card, .continue .continue-card');
        if (c && isVisible(c)) focusEl(c);
      }
    }
    wasModalOpen = mOpen;
    wasPlayerOpen = pOpen;
  }

  function enable() {
    if (enabled) return;
    enabled = true;
    document.documentElement.classList.add('vv-tv');
    document.body.classList.add('tv');
    wasModalOpen = !!topModal();
    wasPlayerOpen = playerOpen();
    document.addEventListener('keydown', onKeyDown, true);
    try {
      var obs = new MutationObserver(onDomChange);
      obs.observe(document.body, { subtree: true, attributes: true, attributeFilter: ['hidden', 'class'] });
    } catch (e) { /* очень старый WebView — работаем без автофокуса модалок */ }
    if (window.console) console.log('[tv] TV-режим включён');
  }

  // ---- Мост для нативной обёртки: кнопка Back ----------------------------
  function handleBack() {
    try {
      var skipPanel = document.getElementById('skip-segments-panel');
      if (skipPanel && !skipPanel.hidden) {
        var skipClose = document.getElementById('skip-segments-close');
        if (skipClose) skipClose.click();
        return 'consumed';
      }
      if (document.fullscreenElement) {
        if (document.exitFullscreen) document.exitFullscreen();
        return 'consumed';
      }
      // Канал IPTV играет — сначала гасим поток (остаёмся на странице каналов).
      if (window.VideoViewerIPTV && window.VideoViewerIPTV.isOpen()) {
        window.VideoViewerIPTV.close();
        return 'consumed';
      }
      // Открытая панель (телепрограмма/плейлисты) закрывается своей кнопкой ×.
      var m = topModal();
      if (m) {
        var c = $('.close', m);
        if (c && isVisible(c)) c.click(); else m.setAttribute('hidden', '');
        return 'consumed';
      }
      // Страницы фильма/просмотра/IPTV/входа — шаг назад; выход из приложения — только из каталога.
      if ((document.body.getAttribute('data-page') || 'catalog') !== 'catalog') {
        if (window.VV && window.VV.tvBack) window.VV.tvBack();
        else window.history.back();
        return 'consumed';
      }
    } catch (e) { /* ignore */ }
    return 'exit';
  }

  // Публичный API (для нативной обёртки и отладки).
  window.VideoViewerTV = { enable: enable, isTV: isTV, handleBack: handleBack, version: 1 };
  window.__vvBack = handleBack;

  if (isTV()) enable();
})();

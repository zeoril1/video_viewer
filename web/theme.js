(function () {
  'use strict';

  const storageKey = 'kinoteka_theme';
  const validThemes = ['system', 'light', 'dark'];
  const root = document.documentElement;
  const media = typeof window.matchMedia === 'function'
    ? window.matchMedia('(prefers-color-scheme: dark)')
    : null;
  let preference = 'system';

  try {
    const stored = window.localStorage.getItem(storageKey);
    if (validThemes.includes(stored)) preference = stored;
  } catch (error) { /* Theme remains available when browser storage is disabled. */ }

  function applyTheme() {
    const resolved = preference === 'system'
      ? (media && media.matches ? 'dark' : 'light')
      : preference;
    root.dataset.theme = resolved;
    root.style.colorScheme = resolved;
    const select = document.getElementById('theme-select');
    if (select) select.value = preference;
  }

  function setTheme(value) {
    preference = validThemes.includes(value) ? value : 'system';
    try { window.localStorage.setItem(storageKey, preference); }
    catch (error) { /* Keep the choice for this page even without storage. */ }
    applyTheme();
  }

  // This script is loaded synchronously in the head, before the stylesheets.
  applyTheme();
  if (media) {
    if (typeof media.addEventListener === 'function') media.addEventListener('change', applyTheme);
    else if (typeof media.addListener === 'function') media.addListener(applyTheme);
  }
  window.addEventListener('storage', (event) => {
    if (event.key !== storageKey && event.key !== null) return;
    preference = validThemes.includes(event.newValue) ? event.newValue : 'system';
    applyTheme();
  });

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text) node.textContent = text;
    return node;
  }

  function icon(className, paths) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 32 32');
    svg.setAttribute('width', '32');
    svg.setAttribute('height', '32');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('aria-hidden', 'true');
    svg.classList.add(className);
    paths.forEach((d) => {
      const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
      path.setAttribute('d', d);
      path.setAttribute('stroke', 'currentColor');
      path.setAttribute('stroke-width', '2');
      path.setAttribute('stroke-linecap', 'round');
      path.setAttribute('stroke-linejoin', 'round');
      svg.append(path);
    });
    return svg;
  }

  function link(url, title, className) {
    const node = element('a', className, title);
    node.href = url;
    if (location.pathname === url) node.setAttribute('aria-current', 'page');
    return node;
  }

  function shellLanguage() {
    if (window.VV && window.VV.lang) return window.VV.lang === 'en' ? 'en' : 'ru';
    try {
      const stored = window.localStorage.getItem('lang');
      if (stored === 'en' || stored === 'ru') return stored;
    } catch (error) { /* The markup language is the fallback without storage. */ }
    return root.lang === 'en' ? 'en' : 'ru';
  }

  function buildHeader() {
    const header = document.querySelector('.topbar');
    if (!header || header.dataset.shell === 'ready') return;
    header.dataset.shell = 'ready';
    header.classList.add('site-header');

    // Move existing nodes rather than cloning them: auth, language and playback
    // scripts have already attached listeners and retained references to them.
    const search = header.querySelector('#search');
    const language = header.querySelector('.lang-switch');
    const account = header.querySelector('.user-area, #user-area');
    const authOpen = header.querySelector('#auth-open');
    const oldBrand = header.querySelector('.logo');
    const contextTitle = header.querySelector('#watch-title');
    const filmLink = header.querySelector('#film-link');

    const brand = link('/', '', 'logo site-brand');
    brand.removeAttribute('aria-current');
    if (oldBrand && oldBrand.id) brand.id = oldBrand.id;
    brand.setAttribute('aria-label', 'Кинотека — на главную');
    brand.append(
      icon('brand-mark', ['M9 5h14a4 4 0 0 1 4 4v14a4 4 0 0 1-4 4H9a4 4 0 0 1-4-4V9a4 4 0 0 1 4-4Z', 'm13 11 8 5-8 5V11Z']),
      element('span', 'brand-name', 'Кинотека')
    );

    const nav = element('nav', 'site-nav');
    nav.setAttribute('aria-label', 'Главная навигация');
    const page = document.body.dataset.page || document.body.dataset.feature || '';
    const path = location.pathname;
    const active = path === '/' || path.endsWith('/index.html') || ['catalog', 'film', 'watch'].includes(page)
      ? '/' : path;
    [
      ['/', 'Каталог'],
      ['/library.html', 'Медиатека'],
      ['/calendar.html', 'Календарь'],
      ['/iptv.html', 'IPTV'],
    ].forEach(([url, title]) => {
      const item = link(url, title, 'site-nav-link');
      if (url === active) {
        item.classList.add('active');
        item.setAttribute('aria-current', url === path || path.endsWith('/index.html') ? 'page' : 'true');
      }
      nav.append(item);
    });

    const profile = element('details', 'profile-menu');
    const summary = element('summary', 'profile-toggle');
    summary.tabIndex = 0;
    summary.setAttribute('aria-label', 'Профиль и настройки');
    summary.append(
      icon('profile-icon', ['M16 4a5 5 0 1 1 0 10 5 5 0 0 1 0-10Z', 'M6 27v-3a10 10 0 0 1 20 0v3']),
      element('span', 'profile-label', 'Профиль и настройки')
    );
    const panel = element('div', 'profile-panel');
    if (account) {
      account.classList.add('user-area', 'profile-group');
      const admin = account.querySelector('#admin-link');
      if (admin) admin.textContent = 'Администрирование';
      // Main navigation is available outside this menu.
      account.querySelectorAll('a[href="/"], a[href="/iptv.html"]').forEach((node) => node.remove());
      panel.append(account);
    }
    if (authOpen) panel.append(authOpen);

    const links = element('div', 'profile-group profile-links');
    links.append(
      link('/discover.html', 'Что посмотреть', 'profile-link'),
      link('/device.html?mode=approve', 'Вход на ТВ', 'profile-link')
    );
    panel.append(links);

    const theme = element('label', 'theme-control');
    theme.append(element('span', '', 'Тема'));
    const select = element('select', 'theme-select');
    select.id = 'theme-select';
    [['system', 'Системная'], ['light', 'Светлая'], ['dark', 'Тёмная']].forEach(([value, title]) => {
      const option = element('option', '', title);
      option.value = value;
      select.append(option);
    });
    select.value = preference;
    select.addEventListener('change', () => setTheme(select.value));
    theme.append(select);
    panel.append(theme);
    if (language) panel.append(language);
    profile.append(summary, panel);

    header.replaceChildren(brand, nav);
    if (search) {
      search.classList.add('header-search');
      search.setAttribute('aria-label', 'Поиск по каталогу');
      header.append(search);
    }
    header.append(profile);
    if (contextTitle || filmLink) {
      const context = element('div', 'header-context');
      if (contextTitle) context.append(contextTitle);
      if (filmLink) context.append(filmLink);
      header.append(context);
    }

    document.addEventListener('click', (event) => {
      if (profile.open && !profile.contains(event.target)) profile.open = false;
    });
    profile.addEventListener('keydown', (event) => {
      if (event.key === 'Escape') {
        profile.open = false;
        summary.focus();
      }
    });

    function translateHeader() {
      const locale = shellLanguage();
      root.lang = locale;
      const labels = locale === 'en' ? {
        home: 'Кинотека — home',
        navigation: 'Main navigation',
        nav: ['Catalog', 'Library', 'Calendar', 'IPTV'],
        profile: 'Profile and settings',
        discover: 'What to watch',
        device: 'Sign in on TV',
        theme: 'Theme',
        themes: ['System', 'Light', 'Dark'],
        search: 'Search the catalog',
        admin: 'Administration',
        signIn: 'Sign in',
        signOut: 'Log out',
      } : {
        home: 'Кинотека — на главную',
        navigation: 'Главная навигация',
        nav: ['Каталог', 'Медиатека', 'Календарь', 'IPTV'],
        profile: 'Профиль и настройки',
        discover: 'Что посмотреть',
        device: 'Вход на ТВ',
        theme: 'Тема',
        themes: ['Системная', 'Светлая', 'Тёмная'],
        search: 'Поиск по каталогу',
        admin: 'Администрирование',
        signIn: 'Войти',
        signOut: 'Выйти',
      };
      brand.setAttribute('aria-label', labels.home);
      nav.setAttribute('aria-label', labels.navigation);
      Array.from(nav.children).forEach((item, index) => { item.textContent = labels.nav[index]; });
      summary.setAttribute('aria-label', labels.profile);
      summary.querySelector('.profile-label').textContent = labels.profile;
      links.children[0].textContent = labels.discover;
      links.children[1].textContent = labels.device;
      theme.querySelector('span').textContent = labels.theme;
      Array.from(select.options).forEach((option, index) => { option.textContent = labels.themes[index]; });
      if (search) search.setAttribute('aria-label', labels.search);
      if (authOpen) authOpen.textContent = labels.signIn;
      if (account) {
        const admin = account.querySelector('#admin-link');
        const logout = account.querySelector('#auth-logout');
        if (admin) admin.textContent = labels.admin;
        if (logout) logout.textContent = labels.signOut;
      }
    }
    document.addEventListener('vv:lang', translateHeader);
    translateHeader();
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', buildHeader, { once: true });
  else buildHeader();
})();

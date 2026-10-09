'use strict';

// Navigation for the watch-history row; native scrolling also supports touch and focus.
(function () {
  function init() {
    const section = document.getElementById('continue');
    const list = document.getElementById('continue-list');
    const navigation = document.getElementById('continue-navigation');
    const previous = document.getElementById('continue-prev');
    const next = document.getElementById('continue-next');
    if (!section || !list || !navigation || !previous || !next) return;

    let updateFrame = 0;

    function update() {
      updateFrame = 0;
      const limit = Math.max(0, list.scrollWidth - list.clientWidth);
      navigation.hidden = section.hidden || list.clientWidth === 0 || limit <= 1;
      previous.disabled = navigation.hidden || list.scrollLeft <= 1;
      next.disabled = navigation.hidden || list.scrollLeft >= limit - 1;
    }

    function scheduleUpdate() {
      if (!updateFrame) updateFrame = window.requestAnimationFrame(update);
    }

    function translate() {
      const english = window.VV && window.VV.lang === 'en';
      navigation.setAttribute('aria-label', english ? 'Watch history navigation' : 'Перелистывание истории просмотра');
      previous.setAttribute('aria-label', english ? 'Previous titles' : 'Предыдущие фильмы');
      next.setAttribute('aria-label', english ? 'Next titles' : 'Следующие фильмы');
      previous.title = previous.getAttribute('aria-label');
      next.title = next.getAttribute('aria-label');
    }

    function scroll(direction) {
      const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
      list.scrollBy({ left: direction * list.clientWidth, behavior: reducedMotion ? 'auto' : 'smooth' });
    }

    previous.addEventListener('click', () => scroll(-1));
    next.addEventListener('click', () => scroll(1));
    list.addEventListener('scroll', scheduleUpdate, { passive: true });
    window.addEventListener('resize', scheduleUpdate);
    document.addEventListener('vv:lang', translate);

    const observer = new MutationObserver(scheduleUpdate);
    observer.observe(list, { childList: true });
    observer.observe(section, { attributes: true, attributeFilter: ['hidden'] });
    if (typeof ResizeObserver !== 'undefined') {
      const resizeObserver = new ResizeObserver(scheduleUpdate);
      resizeObserver.observe(list);
    }

    translate();
    scheduleUpdate();
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();

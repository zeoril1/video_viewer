"use strict";
const FilmFeatures = (() => {
  let loadedID = "";
  let exploreData = null;
  let exploredItem = null;
  let renderedItem = null;
  let sourceItems = [], sourceID = "";
  let disposeRelatedRail = null;
  const { el, button, card } = FeatureUI;
  const text = (ru, en) => window.VV?.lang === "en" ? en : ru;
  function note(text) {
    let n = document.getElementById("film-feature-note");
    if (!n) {
      n = el("p", undefined, "feature-message");
      n.id = "film-feature-note";
      n.setAttribute("role", "status");
      document.getElementById("film-personal").append(n);
    }
    n.textContent = text;
  }
  function action(title, fn) {
    const b = button(title, async () => {
      try {
        await fn();
      } catch (e) {
        note(e.message);
      }
    });
    return b;
  }
  function render(it) {
    if (!it) return;
    renderedItem = it;
    if (typeof WatchOrder !== "undefined") WatchOrder.load(it);
    const box = document.getElementById("film-personal");
    if (!box) return;
    const id = it.imdb_id || it.id,
      snap = Personal.snapshot(it);
    const focusKey = document.activeElement?.dataset?.filmAction;
    const secondary = document.getElementById("film-secondary-personal") || box;
    const seasonBox = document.getElementById("season-personal");
    box.replaceChildren();
    if (secondary !== box) secondary.replaceChildren();
    if (seasonBox) {
      seasonBox.replaceChildren();
      seasonBox.hidden = true;
    }
    const row = el("div", undefined, "feature-actions film-personal-actions");
    box.append(row);
    function toggle(kind, key, title, onTitle, data, target = row, quiet = false) {
      const active = !!Personal.get(kind, key);
      const b = action(active ? onTitle : title, async () => {
        if (active) await Personal.remove(kind, key);
        else await Personal.put(kind, key, data);
        render(renderedItem);
      });
      b.dataset.filmAction = kind + ":" + key;
      b.classList.toggle("active", active);
      b.classList.toggle("film-quiet-action", quiet);
      b.setAttribute("aria-pressed", String(active));
      target.append(b);
    }
    toggle("watchlist", id, text("＋ Смотреть позже", "+ Watch later"), text("✓ В списке", "✓ In watchlist"), snap);
    toggle("watched", id, text("Отметить просмотренным", "Mark as watched"), text("✓ Просмотрено", "✓ Watched"), snap, secondary, true);
    if (isSeriesKind(it.kind)) {
      toggle(
        "follow",
        id,
        text("Подписаться", "Follow series"),
        text("✓ Подписка оформлена", "✓ Following"),
        snap,
      );
      const season = typeof selectedSeason !== "undefined" ? selectedSeason : null;
      const knownSeasons = typeof allKnownSeasons === "function"
        ? allKnownSeasons(id, typeof lastSourceItems !== "undefined" ? lastSourceItems : [])
        : null;
      if (seasonBox && Number.isInteger(season) && season >= 0 &&
          (!knownSeasons || knownSeasons.includes(season))) {
        seasonBox.hidden = false;
        toggle(
          "watched",
          id + ":s" + season,
          text("Отметить сезон " + season + " просмотренным", "Mark season " + season + " as watched"),
          text("✓ Сезон " + season + " просмотрен", "✓ Season " + season + " watched"),
          {
            ...snap,
            title_ru: (snap.title_ru || snap.title) + " · Сезон " + season,
            season,
          },
          seasonBox,
          true,
        );
      }
    }
    const options = document.getElementById("film-options-body");
    if (options) {
      let prefs = document.getElementById("film-preferences-link");
      if (!prefs) {
        prefs = el("a", undefined, "film-preferences-link");
        prefs.id = "film-preferences-link";
        prefs.href = "/library.html";
        options.prepend(prefs);
      }
      prefs.textContent = text("Предпочтения просмотра", "Viewing preferences");
    }
    if (focusKey) {
      for (const target of new Set([box, secondary, seasonBox])) {
        const focused = target && [...target.querySelectorAll("[data-film-action]")]
          .find(b => b.dataset.filmAction === focusKey);
        if (focused) focused.focus({ preventScroll: true });
      }
    }
    // Keep subscription metadata current as the background film enrichment completes.
    const follow = Personal.get("follow", id);
    if (follow && snap.tmdb_id && !follow.data?.tmdb_id)
      Personal.put("follow", id, snap).catch(() => {});
  }
  function renderCredits() {
    if (!exploreData) return;
    for (const [key, target, title, filter] of [
      ["directors", "details-director", t("directorLabel"), "with_crew"],
      ["cast", "details-actors", t("actorsLabel"), "with_cast"],
    ]) {
      const people = exploreData[key] || [];
      const line = document.getElementById(target);
      if (!line || !people.length) continue;
      line.hidden = false;
      line.replaceChildren(document.createTextNode(title + ": "));
      people.forEach((person, index) => {
        if (index) line.append(document.createTextNode(", "));
        const link = el("a", person.name);
        link.href = "/discover.html?" + filter + "=" + encodeURIComponent(person.id);
        line.append(link);
      });
    }
  }
  function relatedRail(grid, navigation, previous, next) {
    let frame = 0;
    const update = () => {
      frame = 0;
      const limit = Math.max(0, grid.scrollWidth - grid.clientWidth);
      navigation.hidden = !grid.clientWidth || limit <= 1;
      previous.disabled = navigation.hidden || grid.scrollLeft <= 1;
      next.disabled = navigation.hidden || grid.scrollLeft >= limit - 1;
    };
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(update);
    };
    const scroll = direction => {
      const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      grid.scrollBy({ left: direction * grid.clientWidth, behavior: reducedMotion ? "auto" : "smooth" });
    };
    previous.addEventListener("click", () => scroll(-1));
    next.addEventListener("click", () => scroll(1));
    grid.addEventListener("scroll", schedule, { passive: true });
    grid.addEventListener("keydown", event => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      event.stopPropagation();
      scroll(event.key === "ArrowLeft" ? -1 : 1);
    });
    window.addEventListener("resize", schedule);
    const observer = typeof ResizeObserver !== "undefined" ? new ResizeObserver(schedule) : null;
    if (observer) observer.observe(grid);
    schedule();
    return () => {
      if (frame) cancelAnimationFrame(frame);
      if (observer) observer.disconnect();
      window.removeEventListener("resize", schedule);
    };
  }
  function renderRelated() {
    const box = document.getElementById("film-explore");
    if (!box || !exploreData) return;
    if (disposeRelatedRail) disposeRelatedRail();
    const previousScroll = box.querySelector(".film-related-carousel")?.scrollLeft || 0;
    const series = isSeriesKind(exploredItem?.kind);
    const title = series ? text("Похожие сериалы", "Similar series") : text("Похожие фильмы", "Similar films");
    const heading = el("div", undefined, "film-related-heading");
    heading.append(el("h3", title));
    const navigation = el("div", undefined, "continue-navigation");
    navigation.hidden = true;
    navigation.setAttribute("role", "group");
    navigation.setAttribute("aria-label", text("Перелистывание похожих", "Similar titles navigation"));
    const arrows = [-1, 1].map(direction => {
      const b = el("button", undefined, "continue-nav-btn");
      b.type = "button";
      b.disabled = true;
      b.setAttribute("aria-label", direction < 0 ? text("Предыдущие рекомендации", "Previous recommendations") : text("Следующие рекомендации", "Next recommendations"));
      b.title = b.getAttribute("aria-label");
      b.innerHTML = '<svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="' + (direction < 0 ? 'M14 6l-6 6 6 6' : 'M10 6l6 6-6 6') + '" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>';
      navigation.append(b);
      return b;
    });
    heading.append(navigation);
    const grid = el("div", undefined, "feature-carousel film-related-carousel");
    grid.tabIndex = 0;
    grid.setAttribute("role", "region");
    grid.setAttribute("aria-label", title);
    (exploreData.items || []).slice(0, 12).forEach(x => {
      const year = String(x.year || x.release_date || "").match(/^\d{4}/)?.[0] || "";
      const related = card(x);
      related.querySelector("strong").textContent = text(x.title_ru || x.title || x.id, x.title || x.title_ru || x.id);
      related.querySelector("small").textContent = [year, x.rating_tmdb ? "★ " + Number(x.rating_tmdb).toFixed(1) : ""].filter(Boolean).join(" · ");
      grid.append(related);
    });
    box.replaceChildren(heading, grid);
    box.hidden = !grid.children.length;
    grid.scrollLeft = previousScroll;
    disposeRelatedRail = relatedRail(grid, navigation, arrows[0], arrows[1]);
  }
  async function explore(it) {
    const id = it.imdb_id || it.id;
    if (!id) return;
    if (loadedID === id) {
      const changedKind = isSeriesKind(exploredItem?.kind) !== isSeriesKind(it.kind);
      exploredItem = it;
      if (changedKind && exploreData) renderRelated();
      return;
    }
    loadedID = id;
    const box = document.getElementById("film-explore");
    if (!box) return;
    exploreData = null;
    exploredItem = it;
    if (disposeRelatedRail) {
      disposeRelatedRail();
      disposeRelatedRail = null;
    }
    box.hidden = true;
    PP.setTrailer("");
    try {
      const data = await Personal.request(
        "/api/films/" + encodeURIComponent(id) + "/explore",
      );
      if (loadedID !== id) return;
      exploreData = data;
      renderCredits();
      PP.setTrailer(data.trailer || "");
      renderRelated();
    } catch (e) {
      if (loadedID === id) loadedID = "";
    }
  }
  function sources(items, id) {
    sourceItems = items;
    sourceID = id;
    let box = document.getElementById("manual-sources");
    if (!box) {
      box = el("details", undefined, "feature-section");
      box.id = "manual-sources";
      const options = document.getElementById("film-options-body");
      if (options) options.append(box);
      else document.getElementById("film-personal").after(box);
    }
    const open = box.open,
      previous = box.querySelector("select")?.value;
    box.replaceChildren(el("summary", text("Выбрать вариант вручную", "Choose an option manually")));
    box.open = open;
    box.hidden = !items.length;
    if (!items.length) return;
    const select = el("select");
    select.setAttribute("aria-label", text("Вариант просмотра", "Playback option"));
    select.style.maxWidth = "100%";
    for (const source of items) {
      const option = el(
        "option",
        [source.title, source.size, text("Сиды: ", "Seeds: ") + (source.seeds || 0)]
          .filter(Boolean)
          .join(" · "),
      );
      option.value = source.magnet;
      select.append(option);
    }
    if (items.some((s) => s.magnet === previous)) select.value = previous;
    const row = el("div", undefined, "feature-toolbar");
    row.append(
      select,
      action(text("Смотреть выбранный вариант", "Play selected option"), async () => {
        const source = items.find((s) => s.magnet === select.value);
        if (!source) return;
        if (!isSeriesKind(currentItem?.kind)) {
          openWatch(id, source, null, 0, 0, Personal.preferences().voice || "");
          return;
        }
        const token = ++playToken,
          season = selectedSeason || 0,
          episode = selectedEpisode || 0;
        const files = await fetchFiles(id, source.magnet, source.title);
        if (token !== playToken) return;
        const file = (files || []).find(
          (f) =>
            (!season || f.season === season) &&
            (!episode || f.episode === episode),
        );
        if (!file) {
          note(
            text("В выбранном варианте не найдена нужная серия. Выберите другой.", "The selected option does not contain this episode. Choose another option."),
          );
          return;
        }
        storeItem(currentItem);
        PP.start({
          id,
          magnet: source.magnet,
          release: source.title,
          file: file.index,
          season: file.season || season,
          ep: file.episode || episode,
          voice: Personal.preferences().voice || "",
        });
      }),
    );
    box.append(row);
  }
  window.addEventListener("personalchange", () => {
    if (typeof currentItem !== "undefined") render(currentItem);
  });
  if (typeof onLang === "function") onLang(() => {
    if (renderedItem) render(renderedItem);
    renderCredits();
    renderRelated();
    if (sourceID) sources(sourceItems, sourceID);
  });
  return { render, explore, sources, renderCredits };
})();

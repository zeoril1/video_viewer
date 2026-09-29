"use strict";
const FilmFeatures = (() => {
  let loadedID = "";
  let exploreData = null;
  const markedSeasons = new Map();
  const { el, button, card } = FeatureUI;
  function note(text) {
    let n = document.getElementById("film-feature-note");
    if (!n) {
      n = el("p", undefined, "feature-message");
      n.id = "film-feature-note";
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
    if (typeof WatchOrder !== "undefined") WatchOrder.load(it);
    const box = document.getElementById("film-personal");
    if (!box) return;
    const id = it.imdb_id || it.id,
      snap = Personal.snapshot(it);
    box.replaceChildren();
    const row = el("div", undefined, "feature-actions");
    box.append(row);
    function toggle(kind, key, title, onTitle, data) {
      const active = !!Personal.get(kind, key);
      const b = action(active ? onTitle : title, async () => {
        if (active) await Personal.remove(kind, key);
        else await Personal.put(kind, key, data);
        render(currentItem);
      });
      b.classList.toggle("active", active);
      b.setAttribute("aria-pressed", String(active));
      row.append(b);
    }
    toggle("watchlist", id, "＋ Смотреть позже", "✓ В списке", snap);
    toggle("watched", id, "Отметить просмотренным", "✓ Просмотрено", snap);
    if (isSeriesKind(it.kind)) {
      toggle(
        "follow",
        id,
        "Подписаться на новые серии",
        "✓ Подписка оформлена",
        snap,
      );
      const season =
        markedSeasons.get(id) ||
        (typeof selectedSeason !== "undefined" ? selectedSeason : 0) ||
        1;
      const seasonLabel = el("label", "Отметка сезона ");
      const seasonInput = el("input");
      seasonInput.type = "number";
      seasonInput.min = "1";
      seasonInput.max = String(it.seasons || 1000);
      seasonInput.value = String(season);
      seasonInput.style.width = "65px";
      seasonInput.setAttribute("aria-label", "Номер просмотренного сезона");
      seasonInput.onchange = () => {
        const n = Number(seasonInput.value);
        if (Number.isInteger(n) && n > 0 && n <= Number(seasonInput.max)) {
          markedSeasons.set(id, n);
          render(it);
        }
      };
      seasonLabel.append(seasonInput);
      row.append(seasonLabel);
      if (season)
        toggle(
          "watched",
          id + ":s" + season,
          "Просмотрен сезон " + season,
          "✓ Сезон " + season + " просмотрен",
          {
            ...snap,
            title_ru: (snap.title_ru || snap.title) + " · Сезон " + season,
            season,
          },
        );
      if (!snap.tmdb_id)
        box.append(
          el(
            "p",
            "Данные сериала загружаются. После появления TMDB ID календарь сможет показать даты серий.",
            "feature-help",
          ),
        );
    }
    const prefs = el("a", "Настроить подбор раздачи", "auth-btn");
    prefs.href = "/library.html";
    row.append(prefs);
    // Keep subscription metadata current as the background film enrichment completes.
    const follow = Personal.get("follow", id);
    if (follow && snap.tmdb_id && !follow.data.tmdb_id)
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
      line.replaceChildren(document.createTextNode(title + ": "));
      people.forEach((person, index) => {
        if (index) line.append(document.createTextNode(", "));
        const link = el("a", person.name);
        link.href = "/discover.html?" + filter + "=" + encodeURIComponent(person.id);
        line.append(link);
      });
    }
  }
  async function explore(it) {
    const id = it.imdb_id || it.id;
    if (!id || loadedID === id) return;
    loadedID = id;
    const box = document.getElementById("film-explore");
    if (!box) return;
    exploreData = null;
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
      box.replaceChildren(el("h3", "Похожие"));
      const grid = el("div", undefined, "feature-carousel");
      grid.tabIndex = 0;
      grid.setAttribute("role", "region");
      grid.setAttribute("aria-label", "Похожие фильмы и сериалы");
      (data.items || []).slice(0, 12).forEach((x) => grid.append(card(x)));
      box.append(grid);
      box.hidden = !grid.children.length;
    } catch (e) {
      if (loadedID === id) loadedID = "";
    }
  }
  function sources(items, id) {
    let box = document.getElementById("manual-sources");
    if (!box) {
      box = el("details", undefined, "feature-section");
      box.id = "manual-sources";
      document.getElementById("film-personal").after(box);
    }
    const open = box.open,
      previous = box.querySelector("select")?.value;
    box.replaceChildren(el("summary", "Выбрать раздачу вручную"));
    box.open = open;
    box.hidden = !items.length;
    if (!items.length) return;
    const select = el("select");
    select.setAttribute("aria-label", "Раздача");
    select.style.maxWidth = "100%";
    for (const source of items) {
      const option = el(
        "option",
        [source.title, source.size, "Сиды: " + (source.seeds || 0)]
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
      action("Смотреть выбранную", async () => {
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
            "В выбранной раздаче не найдена нужная серия. Выберите другой источник.",
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
  return { render, explore, sources, renderCredits };
})();

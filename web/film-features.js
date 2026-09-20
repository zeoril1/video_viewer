"use strict";
const FilmFeatures = (() => {
  let loadedID = "";
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
  async function explore(it) {
    const id = it.imdb_id || it.id;
    if (!id || loadedID === id) return;
    loadedID = id;
    const box = document.getElementById("film-explore");
    if (!box) return;
    const details = el("details");
    details.append(el("summary", "Трейлер, похожие фильмы и участники"));
    box.replaceChildren(details);
    details.addEventListener("toggle", async () => {
      if (!details.open || details.dataset.loaded) return;
      details.dataset.loaded = "1";
      const body = el("div", "Загрузка…");
      details.append(body);
      try {
        const data = await Personal.request(
          "/api/films/" + encodeURIComponent(id) + "/explore",
        );
        body.replaceChildren();
        if (data.trailer) {
          const a = el("a", "▶ Смотреть трейлер", "auth-btn");
          a.href = data.trailer;
          a.target = "_blank";
          a.rel = "noopener noreferrer";
          body.append(a);
        }
        for (const [key, title, filter] of [
          ["directors", "Режиссёры", "with_crew"],
          ["cast", "В ролях", "with_cast"],
        ]) {
          if (!(data[key] || []).length) continue;
          body.append(el("h3", title));
          const row = el("div", undefined, "feature-actions");
          for (const p of data[key]) {
            const a = el("a", p.name, "auth-btn");
            a.href = "/discover.html?" + filter + "=" + p.id;
            row.append(a);
          }
          body.append(row);
        }
        body.append(el("h3", "Похожие"));
        const grid = el("div", undefined, "feature-grid");
        (data.items || []).slice(0, 12).forEach((x) => grid.append(card(x)));
        body.append(grid);
        if (!grid.children.length)
          grid.textContent = "Похожих вариантов пока нет.";
      } catch (e) {
        body.textContent = e.message;
        delete details.dataset.loaded;
      }
    });
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
  return { render, explore, sources };
})();

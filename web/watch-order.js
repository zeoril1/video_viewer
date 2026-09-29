"use strict";
const WatchOrder = (() => {
  const { el, button } = FeatureUI;
  let currentID = "", data = null, generation = 0;
  function render() {
    const box = document.getElementById("film-watch-order");
    if (!box || !data) return;
    box.replaceChildren();
    box.hidden = data.items.length < 2;
    if (box.hidden) return;
    box.append(el("h3", "Порядок просмотра"));
    box.append(el("p", data.title + " · По дате выхода", "watch-order-heading"));
    box.append(el("p", data.note || "Связанные части по дате выхода.", "feature-help"));
    if (data.partial || data.stale) box.append(el("p", data.stale
      ? "Источник временно недоступен. Показан ранее сохранённый порядок."
      : "Найдены не все связи: список может быть неполным.", "feature-help"));
    const list = el("ol", undefined, "watch-order-list");
    for (const item of data.items) {
      const row = el("li");
      if (item.current) { row.classList.add("watch-order-current"); row.setAttribute("aria-current", "true"); }
      const text = el("div");
      const title = el(item.id ? "a" : "strong", item.title);
      if (item.id) title.href = "/film.html?id=" + encodeURIComponent(item.id);
      text.append(title);
      const watched = item.id && Personal.get("watched", item.id);
      text.append(el("small", [item.date || "Дата неизвестна", item.extra ? "Дополнительная часть / спецвыпуск" : "", item.current ? "Вы здесь" : "", watched ? "✓ Просмотрено" : ""].filter(Boolean).join(" · ")));
      row.append(text);
      if (!item.id) {
        row.append(button("Найти в каталоге", async () => {
          const target = el("div", undefined, "feature-carousel");
          row.querySelector(".feature-carousel")?.remove();
          row.append(target);
          target.textContent = "Ищем совпадения…";
          try {
            const result = await Personal.request("/api/catalog?section=all&per_page=10&q=" + encodeURIComponent(item.title));
            target.replaceChildren();
            for (const found of result.items || []) target.append(FeatureUI.card(found));
            if (!target.children.length) target.textContent = "В каталоге пока не найдено.";
          } catch (_) { target.textContent = "Не удалось выполнить поиск. Попробуйте ещё раз."; }
        }));
      }
      list.append(row);
    }
    const details = el("details");
    details.open = data.items.length <= 12;
    details.append(el("summary", "Все части: " + data.items.length), list);
    const index = data.items.findIndex(x => x.current);
    const next = data.items.slice(index + 1).find(x => !x.extra && (!x.date || x.date <= new Date().toISOString().slice(0, 10)) && !(x.id && Personal.get("watched", x.id)));
    if (index >= 0 && next?.id) {
      const link = el("a", "Далее: " + next.title, "auth-btn");
      link.href = "/film.html?id=" + encodeURIComponent(next.id);
      box.append(link);
    }
    box.append(details);
    const source = el("a", "Источник: " + data.source, "feature-help");
    if (/^https:\/\/(www\.themoviedb\.org|anilist\.co)\//.test(data.source_url)) {
      source.href = data.source_url; source.target = "_blank"; source.rel = "noopener noreferrer";
    }
    box.append(source);
  }
  async function load(it, retry = false) {
    const id = it?.imdb_id || it?.id;
    if (!id || (id === currentID && !retry)) return;
    currentID = id; data = null;
    const token = ++generation;
    const box = document.getElementById("film-watch-order");
    if (!box) return;
    if (!/^(tt\d+|tmdb-(movie|tv)-\d+)$/.test(id)) { box.hidden = true; return; }
    box.hidden = false;
    box.replaceChildren(el("h3", "Порядок просмотра"), el("p", "Ищем связанные части…", "feature-help"));
    box.setAttribute("aria-busy", "true");
    try {
      const result = await Personal.request("/api/films/" + encodeURIComponent(id) + "/watch-order");
      if (token !== generation) return;
      data = { ...result, items: result.items || [] };
      render();
    } catch (_) {
      if (token !== generation) return;
      box.replaceChildren(el("h3", "Порядок просмотра"), el("p", "Не удалось получить порядок просмотра.", "feature-help"), button("Повторить", () => load(it, true)));
    } finally { if (token === generation) box.removeAttribute("aria-busy"); }
  }
  window.addEventListener("personalchange", render);
  return { load };
})();

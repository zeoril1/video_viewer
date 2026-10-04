"use strict";

const FeatureUI = (() => {
  function el(tag, text, cls) {
    const e = document.createElement(tag);
    if (text !== undefined) e.textContent = text;
    if (cls) e.className = cls;
    return e;
  }
  function card(it) {
    const a = el("a", undefined, "feature-card");
    a.href = "/film.html?id=" + encodeURIComponent(it.imdb_id || it.id);
    a.addEventListener("click", () => storeItem(it));
    if (it.poster || it.poster_url) {
      const img = el("img");
      img.src = it.poster || it.poster_url;
      img.alt = "";
      img.loading = "lazy";
      a.append(img);
    }
    a.append(el("strong", it.title_ru || it.title || it.id));
    a.append(
      el(
        "small",
        [
          it.release_date || it.year,
          it.rating_tmdb ? "★ " + Number(it.rating_tmdb).toFixed(1) : "",
        ]
          .filter(Boolean)
          .join(" · "),
      ),
    );
    return a;
  }
  function message(text) {
    const e = document.getElementById("feature-status");
    if (e) {
      e.textContent = text;
      e.hidden = !text;
    }
  }
  function button(text, fn) {
    const b = el("button", text, "auth-btn");
    b.type = "button";
    b.onclick = async () => {
      b.disabled = true;
      try {
        await fn();
      } catch (e) {
        message(e.message);
      } finally {
        b.disabled = false;
      }
    };
    return b;
  }
  return { el, card, message, button };
})();

(async () => {
  if (!document.getElementById("feature-content")) return;
  const { el, card, message, button } = FeatureUI;
  const content = document.getElementById("feature-content"),
    toolbar = document.getElementById("feature-toolbar");
  const mode = document.body.dataset.feature;
  applyLang();
  await initAuth();
  try {
    await Personal.load();
  } catch (e) {
    message(e.message);
  }
  if (mode === "library") {
    if (!VV.user) {
      message("Войдите в аккаунт, чтобы увидеть списки и подписки.");
      return;
    }
    let kind = "watchlist";
    function render() {
      content.className = "feature-grid";
      content.replaceChildren();
      const list = Personal.list(kind);
      message(
        list.length
          ? ""
          : "Здесь пока пусто. Добавляйте фильмы и сериалы со страницы фильма.",
      );
      list.forEach((x) => {
        const wrap = el("div");
        wrap.append(
          card(x.data),
          button("Убрать", async () => {
            await Personal.remove(kind, x.key);
            render();
          }),
        );
        content.append(wrap);
      });
    }
    [
      ["watchlist", "Смотреть позже"],
      ["watched", "Просмотрено"],
      ["follow", "Подписки"],
    ].forEach(([k, t]) =>
      toolbar.append(
        button(t, () => {
          kind = k;
          render();
        }),
      ),
    );
    const settings = el("section", undefined, "feature-section");
    settings.innerHTML =
      '<h2>Предпочтения просмотра</h2><form class="feature-form"><label>Озвучка<input name="voice" maxlength="100" placeholder="Например, LostFilm"></label><label>Максимальное качество<select name="max_quality"><option value="0">Без ограничения</option><option>2160</option><option>1080</option><option>720</option><option>480</option></select></label><label>Размер раздачи, ГБ<input name="max_size_gb" type="number" min="0" max="10000" step="0.1" placeholder="0 — без ограничения"></label><button class="auth-btn">Сохранить</button></form><p class="feature-help">Лимит размера относится ко всей раздаче, включая пакеты сезонов. Источники с неизвестным размером или качеством могут участвовать в подборе. Ручной выбор остаётся доступным.</p>';
    content.before(settings);
    const form = settings.querySelector("form"),
      p = Personal.preferences();
    for (const key of ["voice", "max_quality", "max_size_gb"])
      form.elements[key].value = p[key] || (key === "voice" ? "" : 0);
    form.onsubmit = async (e) => {
      e.preventDefault();
      try {
        await Personal.put("preferences", "playback", {
          voice: form.elements.voice.value.trim(),
          max_quality: Number(form.elements.max_quality.value),
          max_size_gb: Number(form.elements.max_size_gb.value),
        });
        message("Предпочтения сохранены.");
      } catch (err) {
        message(err.message);
      }
    };
    window.addEventListener("personalchange", () => {
      if (!VV.user) {
        content.replaceChildren();
        settings.hidden = true;
        toolbar.replaceChildren();
        message("Войдите в аккаунт, чтобы увидеть списки и подписки.");
      }
    });
    render();
    return;
  }
  if (mode === "discover") {
    toolbar.innerHTML =
      '<label>Тип <select id="pick-media"><option value="movie">Фильмы</option><option value="tv">Сериалы</option></select></label><label>Жанр <select id="pick-genre"><option value="">Любой</option><option value="35">Комедия</option><option value="18">Драма</option><option value="53">Триллер</option><option value="878">Фантастика</option><option value="16">Анимация</option><option value="99">Документальный</option></select></label><label>Рейтинг от <input id="pick-rating" type="number" min="0" max="10" step="0.5" value="6"></label><label>До минут <input id="pick-runtime" type="number" min="1" max="600" value="150"></label><label><input id="pick-unwatched" type="checkbox" checked> Непросмотренное</label>';
    let page = 1,
      pickGeneration = 0;
    const qs = new URLSearchParams(location.search);
    const pickMedia = document.getElementById("pick-media");
    if (qs.has("with_cast") || qs.has("with_crew")) pickMedia.disabled = true;
    pickMedia.onchange = () => {
      const options = document.getElementById("pick-genre").options;
      options[3].value = pickMedia.value === "tv" ? "9648" : "53";
      options[3].textContent =
        pickMedia.value === "tv" ? "Детектив" : "Триллер";
      options[4].value = pickMedia.value === "tv" ? "10765" : "878";
    };
    async function show(append) {
      const gen = ++pickGeneration;
      message("Подбираю варианты…");
      if (!append) {
        page = 1;
        content.replaceChildren();
      }
      content.className = "feature-grid";
      const q = new URLSearchParams({
        media: document.getElementById("pick-media").value,
        page: String(page),
        "vote_average.gte": document.getElementById("pick-rating").value,
        "with_runtime.lte": document.getElementById("pick-runtime").value,
      });
      if (document.getElementById("pick-genre").value)
        q.set("with_genres", document.getElementById("pick-genre").value);
      for (const key of ["with_cast", "with_crew"])
        if (qs.has(key)) q.set(key, qs.get(key));
      const data = await Personal.request("/api/discover/picks?" + q);
      if (gen !== pickGeneration) return;
      let list = data.items || [];
      if (document.getElementById("pick-unwatched").checked)
        list = list.filter(
          (it) => !Personal.get("watched", it.imdb_id || it.id),
        );
      list.forEach((it) => content.append(card(it)));
      more.hidden = page >= data.total_pages;
      message(
        content.children.length
          ? ""
          : "Подходящих вариантов нет. Измените фильтры или загрузите следующую страницу.",
      );
    }
    toolbar.append(button("Подобрать", () => show(false)));
    toolbar.append(
      button("Случайный из найденных", () => {
        const links = [...content.querySelectorAll("a")];
        if (links.length)
          links[Math.floor(Math.random() * links.length)].click();
      }),
    );
    const more = button("Ещё варианты", () => {
      page++;
      return show(true);
    });
    const pagination = el("div", undefined, "feature-toolbar feature-pagination");
    pagination.append(more);
    content.after(pagination);
    try {
      await show(false);
    } catch (e) {
      message(e.message);
    }
    return;
  }
  if (mode === "calendar") {
    const now = new Date();
    let month = new Date(now.getFullYear(), now.getMonth(), 1),
      media = "",
      page = 1,
      events = [],
      generation = 0;
    const input = el("input");
    input.type = "month";
    const select = el("select");
    [
      ["", "Все новинки"],
      ["movie", "Фильмы"],
      ["tv", "Сериалы"],
    ].forEach(([v, t]) => {
      const o = el("option", t);
      o.value = v;
      select.append(o);
    });
    const eventMode = el("select");
    [
      ["episodes", "Премьеры и серии"],
      ["premieres", "Только премьеры"],
    ].forEach(([v, t]) => {
      const o = el("option", t);
      o.value = v;
      eventMode.append(o);
    });
    const mine = el("input");
    mine.type = "checkbox";
    const mineLabel = el("label", "Только подписки ");
    mineLabel.prepend(mine);
    toolbar.append(
      button("Назад", () => {
        month.setMonth(month.getMonth() - 1);
        return load();
      }),
      input,
      button("Вперёд", () => {
        month.setMonth(month.getMonth() + 1);
        return load();
      }),
      select,
      eventMode,
      mineLabel,
    );
    const help = el(
      "p",
      "Даты премьер и серий — по TMDB. Загружайте следующие страницы, чтобы увидеть больше событий месяца. Дата выхода не гарантирует наличия раздачи.",
      "feature-help",
    );
    toolbar.after(help);
    input.onchange = () => {
      if (input.value) {
        month = new Date(input.value + "-01T12:00:00");
        load().catch((e) => message(e.message));
      }
    };
    eventMode.onchange = () => load().catch((e) => message(e.message));
    select.onchange = () => {
      media = select.value;
      load().catch((e) => message(e.message));
    };
    mine.onchange = () => load().catch((e) => message(e.message));
    const more = button("Загрузить ещё премьеры", () => load(true));
    content.after(more);
    function draw() {
      content.className = "calendar-grid";
      content.replaceChildren();
      ["Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"].forEach((d) =>
        content.append(el("strong", d, "calendar-weekday")),
      );
      for (let i = 0; i < (month.getDay() + 6) % 7; i++)
        content.append(el("div", undefined, "calendar-day calendar-empty"));
      const days = new Date(
        month.getFullYear(),
        month.getMonth() + 1,
        0,
      ).getDate();
      for (let d = 1; d <= days; d++) {
        const date = input.value + "-" + String(d).padStart(2, "0"),
          day = el("section", undefined, "calendar-day");
        day.append(el("strong", String(d)));
        if (
          date ===
          now.getFullYear() +
            "-" +
            String(now.getMonth() + 1).padStart(2, "0") +
            "-" +
            String(now.getDate()).padStart(2, "0")
        )
          day.classList.add("today");
        events
          .filter((e) => e.date === date)
          .sort((a, b) =>
            (a.item.title_ru || a.item.title || "").localeCompare(
              b.item.title_ru || b.item.title || "",
            ),
          )
          .forEach((e) => {
            const a = el(
              "a",
              e.item.title_ru || e.item.title,
              "calendar-event " +
                (e.item.kind === "tvSeries" ? "series" : "movie"),
            );
            a.href =
              "/film.html?id=" +
              encodeURIComponent(e.item.imdb_id || e.item.id) +
              (e.episode ? "&season=" + e.season + "&ep=" + e.episode : "");
            a.onclick = () => storeItem(e.item);
            a.append(
              el(
                "small",
                e.episode
                  ? "S" + e.season + " · E" + e.episode + " " + e.name
                  : "Премьера",
              ),
            );
            day.append(a);
          });
        content.append(day);
      }
    }
    async function load(append) {
      const gen = ++generation;
      if (!append) {
        page = 1;
        events = [];
      } else page++;
      input.value =
        month.getFullYear() +
        "-" +
        String(month.getMonth() + 1).padStart(2, "0");
      message("Загружаю календарь…");
      more.hidden = true;
      let hasMore = false;
      const added = [];
      if (mine.checked && !append && media !== "movie") {
        Personal.list("follow").forEach(({ data: it }) => {
          if (it.release_date && it.release_date.startsWith(input.value))
            added.push({ date: it.release_date, item: it });
        });
      }
      if (!mine.checked) {
        for (const type of media ? [media] : ["movie", "tv"]) {
          const data = await Personal.request(
            "/api/discover/calendar?media=" +
              type +
              "&month=" +
              input.value +
              "&page=" +
              page +
              "&events=" +
              eventMode.value,
          );
          if (gen !== generation) return;
          hasMore = hasMore || page < data.total_pages;
          (data.items || [])
            .filter(
              (it) =>
                it.release_date && it.release_date.startsWith(input.value),
            )
            .forEach((it) => added.push({ date: it.release_date, item: it }));
          added.push(...(data.events || []));
          if (data.partial)
            help.textContent =
              "Некоторые серии не удалось загрузить. Доступные события показаны; повторите позже.";
        }
      }
      if (!append && media !== "movie" && eventMode.value === "episodes") {
        const followed = Personal.list("follow");
        let failed = 0;
        // Four workers keep a large subscription list from flooding the metadata service.
        let index = 0;
        await Promise.all(
          Array.from({ length: Math.min(4, followed.length) }, async () => {
            while (index < followed.length) {
              const it = followed[index++].data;
              if (!it.tmdb_id) continue;
              try {
                const data = await Personal.request(
                  "/api/discover/episodes?tmdb_id=" +
                    encodeURIComponent(it.tmdb_id) +
                    "&month=" +
                    input.value,
                );
                (data.episodes || []).forEach((ep) =>
                  added.push({
                    date: ep.air_date,
                    item: it,
                    season: ep.season_number,
                    episode: ep.episode_number,
                    name: ep.name,
                  }),
                );
              } catch (e) {
                failed++;
              }
            }
          }),
        );
        if (failed)
          help.textContent =
            "Не удалось загрузить серии для " +
            failed +
            " подписок. Остальные события показаны. Дата выхода не гарантирует наличия раздачи.";
      }
      if (gen !== generation) return;
      const keys = new Set(
        events.map((e) => [e.item.id, e.date, e.season, e.episode].join("|")),
      );
      for (const e of added) {
        const k = [e.item.id, e.date, e.season, e.episode].join("|");
        if (!keys.has(k)) {
          events.push(e);
          keys.add(k);
        }
      }
      draw();
      more.hidden = !hasMore || mine.checked;
      message(
        events.length
          ? ""
          : "Нет событий за этот месяц. Для персонального календаря подпишитесь на сериалы.",
      );
    }
    try {
      await load();
    } catch (e) {
      message(e.message);
    }
    return;
  }
})();

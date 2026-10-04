"use strict";
window.IPTVFeatures = (() => {
  const { el, button, message } = FeatureUI;
  let mode = "all",
    current = null,
    previous = null,
    offset = 0,
    guideGeneration = 0;
  const tools = document.getElementById("iptv-feature-tools"),
    guide = document.getElementById("iptv-guide");
  function render() {
    if (window.VideoViewerIPTV) VideoViewerIPTV.render();
  }
  [
    ["all", "Все каналы"],
    ["favorite", "★ Избранные"],
    ["recent", "Недавние"],
  ].forEach(([m, t]) =>
    tools.append(
      button(t, () => {
        mode = m;
        render();
      }),
    ),
  );
  tools.append(
    button("↩ Предыдущий канал", () => {
      if (previous) VideoViewerIPTV.play(previous);
      else message("Предыдущий канал ещё не выбран.");
    }),
  );
  tools.append(
    button("Телепрограмма", () => {
      guide.hidden = !guide.hidden;
      if (!guide.hidden) return loadGuide();
    }),
  );
  function filter(channels) {
    if (mode === "all") return channels;
    const entries = Personal.list(
      mode === "favorite" ? "iptv_favorite" : "iptv_recent",
    );
    const ids = new Map(entries.map((e, i) => [String(e.key), i]));
    return channels
      .filter((ch) => ids.has(String(ch.id)))
      .sort((a, b) => ids.get(String(a.id)) - ids.get(String(b.id)));
  }
  function decorate(card, ch) {
    const active = !!Personal.get("iptv_favorite", ch.id),
      b = el("button", active ? "★" : "☆", "auth-btn");
    b.type = "button";
    b.setAttribute(
      "aria-label",
      active ? "Убрать из избранного" : "В избранное",
    );
    b.setAttribute("aria-pressed", String(active));
    b.onclick = async (e) => {
      e.stopPropagation();
      b.disabled = true;
      try {
        if (active) await Personal.remove("iptv_favorite", ch.id);
        else await Personal.put("iptv_favorite", ch.id, { name: ch.name });
        render();
      } catch (err) {
        message(err.message);
      } finally {
        b.disabled = false;
      }
    };
    b.onkeydown = (e) => e.stopPropagation();
    card.append(b);
  }
  function playing(ch) {
    if (current && current.id !== ch.id) previous = current;
    current = ch;
    if (VV.user)
      Personal.put("iptv_recent", ch.id, { name: ch.name })
        .then(async () => {
          for (const old of Personal.list("iptv_recent").slice(0).slice(30))
            await Personal.remove("iptv_recent", old.key);
        })
        .catch(() => {});
  }
  const heading = el("h2", "Телепрограмма"),
    bar = el("div", undefined, "feature-toolbar"),
    date = el("input");
  date.type = "datetime-local";
  function setDate(d) {
    date.value = new Date(d.getTime() - d.getTimezoneOffset() * 60000)
      .toISOString()
      .slice(0, 16);
  }
  setDate(new Date());
  let tableWrap = el("div", undefined, "epg-table-wrap");
  bar.append(
    button("← 6 часов", () => {
      setDate(new Date(new Date(date.value).getTime() - 21600000));
      offset = 0;
      return loadGuide();
    }),
    date,
    button("6 часов →", () => {
      setDate(new Date(new Date(date.value).getTime() + 21600000));
      offset = 0;
      return loadGuide();
    }),
    button("Сейчас", () => {
      setDate(new Date());
      offset = 0;
      return loadGuide();
    }),
  );
  date.onchange = () => {
    offset = 0;
    loadGuide().catch((e) => message(e.message));
  };
  const back = button("Предыдущие каналы", () => {
      offset = Math.max(0, offset - 20);
      return loadGuide();
    }),
    next = button("Следующие каналы", () => {
      offset += 20;
      return loadGuide();
    });
  guide.append(
    heading,
    bar,
    el(
      "p",
      "Архив доступен у каналов с поддержкой провайдера. История программы хранится до 7 дней; будущие передачи — по загруженному EPG.",
      "feature-help",
    ),
    tableWrap,
    back,
    next,
  );
  async function loadGuide() {
    const generation = ++guideGeneration,
      from = new Date(date.value);
    if (!Number.isFinite(from.getTime())) return;
    message("Загружаю телепрограмму…");
    const data = await Personal.request(
      "/api/iptv/guide?from=" +
        encodeURIComponent(from.toISOString()) +
        "&offset=" +
        offset +
        "&q=" +
        encodeURIComponent(document.getElementById("iptv-search").value),
    );
    if (generation !== guideGeneration) return;
    tableWrap.replaceChildren();
    const table = el("table", undefined, "epg-table");
    tableWrap.append(table);
    const head = el("tr");
    head.append(el("th", "Канал"));
    for (let i = 0; i < 6; i++) {
      head.append(
        el(
          "th",
          new Date(from.getTime() + i * 3600000).toLocaleTimeString([], {
            hour: "2-digit",
            minute: "2-digit",
          }),
        ),
      );
    }
    table.append(head);
    const now = Date.parse(data.now);
    for (const ch of data.channels || []) {
      const row = el("tr"),
        name = el("th");
      name.append(
        button(ch.name, () =>
          VideoViewerIPTV.play({
            ...ch,
            play_url: "/api/iptv/play/" + ch.id + ".m3u8",
          }),
        ),
      );
      row.append(name);
      for (let i = 0; i < 6; i++) {
        const cell = el("td");
        const start = from.getTime() + i * 3600000,
          end = start + 3600000;
        for (const p of ch.programs || []) {
          const ps = Date.parse(p.start),
            pe = Date.parse(p.stop);
          if (ps >= end || pe <= start) continue;
          const text =
            new Date(ps).toLocaleTimeString([], {
              hour: "2-digit",
              minute: "2-digit",
            }) +
            " " +
            p.title;
          if (
            pe <= now &&
            ch.catchup_days > 0 &&
            ps >= now - ch.catchup_days * 86400000
          ) {
            const b = button("▶ " + text, () =>
              VideoViewerIPTV.play({
                ...ch,
                archive: true,
                name: ch.name + " · " + p.title,
                play_url:
                  "/api/iptv/archive/" +
                  ch.id +
                  "?start=" +
                  encodeURIComponent(p.start) +
                  "&stop=" +
                  encodeURIComponent(p.stop),
              }),
            );
            b.classList.add("epg-program");
            cell.append(b);
          } else {
            cell.append(
              el("p", text + (ps <= now && pe > now ? " · Сейчас" : "")),
            );
          }
        }
        row.append(cell);
      }
      table.append(row);
    }
    back.disabled = offset === 0;
    next.hidden = !data.has_more;
    message((data.channels || []).length ? "" : "Каналов не найдено.");
  }
  window.addEventListener("personalchange", render);
  initAuth()
    .then(() => Personal.load())
    .then(render)
    .catch((e) => message(e.message));
  return { filter, decorate, playing };
})();

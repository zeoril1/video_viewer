"use strict";
(() => {
  if (document.body.dataset.feature !== "library") return;
  const { el, button, card, message } = FeatureUI;
  const toolbar = document.getElementById("feature-toolbar"),
    content = document.getElementById("feature-content");
  let generation = 0;
  toolbar.addEventListener("click", () => {
    generation++;
  });
  const open = button("Новые серии", async () => {
    const token = ++generation;
    await Personal.load();
    content.replaceChildren();
    content.className = "feature-grid";
    message("Проверяю подписки…");
    const today = new Date(),
      cutoff = new Date(today.getTime() - 30 * 86400000),
      dates = new Set();
    for (let d = new Date(cutoff); d <= today; d.setDate(d.getDate() + 1))
      dates.add(
        d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0"),
      );
    const jobs = [];
    for (const sub of Personal.list("follow"))
      for (const month of dates) jobs.push({ it: sub.data, month });
    let index = 0,
      failures = 0;
    const releases = [];
    await Promise.all(
      Array.from({ length: Math.min(4, jobs.length) }, async () => {
        while (index < jobs.length) {
          const { it, month } = jobs[index++];
          if (!it.tmdb_id) {
            failures++;
            continue;
          }
          try {
            const data = await Personal.request(
              "/api/discover/episodes?tmdb_id=" +
                it.tmdb_id +
                "&month=" +
                month,
            );
            for (const ep of data.episodes || []) {
              const date = new Date(ep.air_date + "T00:00:00");
              if (date < cutoff || date > today) continue;
              const id = it.imdb_id || it.id,
                h = VV.history.find((x) => x.film_id === id);
              const marks = [
                Personal.get("watched", id),
                Personal.get("watched", id + ":s" + ep.season_number),
              ];
              // A completed ongoing series can still release new episodes later.
              if (
                marks.some(
                  (mark) => mark && ep.air_date <= mark.updated_at.slice(0, 10),
                )
              )
                continue;
              if (
                h &&
                (h.season > ep.season_number ||
                  (h.season === ep.season_number &&
                    (h.episode > ep.episode_number ||
                      (h.episode === ep.episode_number &&
                        h.duration > 0 &&
                        h.position / h.duration >= 0.9))))
              )
                continue;
              releases.push({ it, ep });
            }
          } catch (e) {
            failures++;
          }
        }
      }),
    );
    if (token !== generation) return;
    releases.sort((a, b) => b.ep.air_date.localeCompare(a.ep.air_date));
    for (const { it, ep } of releases) {
      const wrap = el("article"),
        a = card(it);
      a.href += "&season=" + ep.season_number + "&ep=" + ep.episode_number;
      a.append(
        el(
          "small",
          ep.air_date +
            " · S" +
            ep.season_number +
            "E" +
            ep.episode_number +
            " · " +
            ep.name,
        ),
      );
      wrap.append(a);
      const state = el(
        "p",
        "Серия вышла. Наличие источника ещё не проверено.",
        "feature-help",
      );
      wrap.append(
        state,
        button("Проверить нужную озвучку", async () => {
          state.textContent = "Ищу доступные раздачи…";
          const id = it.imdb_id || it.id,
            prefs = Personal.preferences(),
            deadline = Date.now() + 90000;
          let sources = [];
          while (Date.now() < deadline) {
            const data = await Personal.request(
              "/api/films/" + encodeURIComponent(id) + "/sources",
            );
            sources = Personal.rank(data.items || [], prefs.voice);
            if (prefs.voice)
              sources = sources.filter((s) =>
                String(s.title || "")
                  .toLowerCase()
                  .includes(prefs.voice.toLowerCase()),
              );
            if (sources.length || data.status !== "searching") break;
            await new Promise((r) => setTimeout(r, 2000));
          }
          for (const s of sources.slice(0, 6)) {
            const q = new URLSearchParams({
              magnet: s.magnet,
              title: s.title || "",
              tmdb: String(it.tmdb_id),
            });
            const data = await Personal.request(
              "/api/films/" + encodeURIComponent(id) + "/files?" + q,
            );
            const f = (data.files || []).find(
              (f) =>
                f.season === ep.season_number &&
                f.episode === ep.episode_number,
            );
            if (!f) continue;
            state.textContent =
              "Найден источник" +
              (prefs.voice ? " · " + prefs.voice : "") +
              ".";
            const watch = el("a", "▶ Смотреть", "auth-btn");
            watch.href =
              "/film.html?id=" +
              encodeURIComponent(id) +
              "&magnet=" +
              encodeURIComponent(s.magnet) +
              "&file=" +
              f.index +
              "&season=" +
              ep.season_number +
              "&ep=" +
              ep.episode_number +
              "&voice=" +
              encodeURIComponent(prefs.voice || "") +
              "&play=1";
            watch.onclick = () => storeItem(it);
            wrap.append(watch);
            return;
          }
          state.textContent = "Подходящий источник этой серии пока не найден.";
        }),
      );
      content.append(wrap);
    }
    message(
      (releases.length
        ? "Непросмотренные серии за последние 30 дней."
        : "Новых непросмотренных серий за последние 30 дней нет.") +
        (failures ? " Не все подписки удалось проверить." : ""),
    );
  });
  // Avoid the toolbar's cancellation event invalidating this very request.
  open.addEventListener("click", (e) => e.stopPropagation());
  open.disabled = true;
  onAuth(() => {
    open.disabled = !VV.user;
  });
  toolbar.append(open);
})();

"use strict";
(async () => {
  const status = document.getElementById("feature-status"),
    start = document.getElementById("device-start");
  const approving =
    new URLSearchParams(location.search).get("mode") === "approve";
  let timer,
    generation = 0;
  await initAuth();
  if (approving) {
    document.getElementById("device-display").hidden = true;
    document.getElementById("device-title").textContent =
      "Подтверждение входа на ТВ";
    if (!VV.user) {
      location.replace(
        "/login.html?next=" + encodeURIComponent("/device.html?mode=approve"),
      );
      return;
    }
    status.textContent =
      "Введите код с вашего телевизора. Подтверждение даст этому устройству доступ к вашему аккаунту.";
    const form = document.getElementById("device-approve");
    form.hidden = false;
    form.onsubmit = async (e) => {
      e.preventDefault();
      const submit = form.querySelector("button");
      submit.disabled = true;
      try {
        await Personal.request("/api/auth/device/approve", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ code: form.elements.code.value }),
        });
        status.textContent =
          "Вход подтверждён. Телевизор подключится через несколько секунд.";
        form.hidden = true;
      } catch (err) {
        status.textContent =
          "Не удалось подтвердить код. Проверьте код и срок его действия; при частых попытках подождите.";
      } finally {
        submit.disabled = false;
      }
    };
    return;
  }
  start.onclick = async () => {
    const gen = ++generation;
    clearTimeout(timer);
    start.disabled = true;
    try {
      const d = await Personal.request("/api/auth/device/start", {
        method: "POST",
      });
      document.getElementById("device-code").textContent =
        d.user_code.slice(0, 4) + "-" + d.user_code.slice(4);
      document.getElementById("device-instructions").textContent =
        "На телефоне откройте " +
        d.verification_uri +
        ", войдите в аккаунт и введите этот код.";
      const end = Date.now() + d.expires_in * 1000;
      async function poll() {
        if (gen !== generation) return;
        if (Date.now() >= end) {
          status.textContent = "Код истёк. Получите новый.";
          start.disabled = false;
          return;
        }
        status.textContent =
          "Ожидаю подтверждения. Код действует ещё " +
          Math.ceil((end - Date.now()) / 60000) +
          " мин.";
        try {
          const result = await Personal.request("/api/auth/device/poll", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ device_code: d.device_code }),
          });
          if (gen !== generation) return;
          if (result.status === "approved") {
            location.replace("/?tv=1");
            return;
          }
          if (result.status === "expired") {
            status.textContent = "Код истёк или уже использован.";
            start.disabled = false;
            return;
          }
        } catch (e) {
          status.textContent = "Связь прервана. Повторяю проверку…";
        }
        timer = setTimeout(poll, d.interval * 1000);
      }
      timer = setTimeout(poll, d.interval * 1000);
      status.textContent = "Код действует 10 минут. Ожидаю подтверждения.";
    } catch (e) {
      status.textContent = e.message;
      start.disabled = false;
    }
  };
  window.addEventListener("pagehide", () => {
    generation++;
    clearTimeout(timer);
  });
})();

"use strict";
(() => {
  if (!PP.available) return;
  const video = document.getElementById("player"),
    box = document.createElement("div");
  box.className = "recovery-offer";
  box.hidden = true;
  box.setAttribute("role", "status");
  document.getElementById("player-wrap").after(box);
  const text = document.createElement("p"),
    button = document.createElement("button");
  button.className = "auth-btn";
  button.textContent = "Выбрать другой источник";
  box.append(text, button);
  let stalled = 0,
    failureMessage = "";
  function offer() {
    if (PP.isFollower()) {
      box.hidden = true;
      return;
    }
    if (PP.playing()) {
      box.hidden = false;
      text.textContent = failureMessage ||
        "Воспроизведение задерживается. Можно открыть список источников и выбрать другой с сохранением позиции.";
    }
  }
  window.addEventListener("playbackstart", () => {
    failureMessage = "";
    stalled = Date.now();
    box.hidden = true;
  });
  window.addEventListener("playbackfailure", event => {
    const message = event && event.detail && event.detail.message;
    failureMessage = typeof message === "string" && message.trim() ? message
      : "Воспроизведение прервалось. Можно выбрать другой источник с сохранением позиции.";
    offer();
  });
  window.addEventListener("playbackrole", () => {
    button.disabled = PP.isFollower();
    if (!PP.isFollower()) return;
    box.hidden = true;
  });
  video.addEventListener("error", offer);
  video.addEventListener("waiting", () => {
    if (!stalled) stalled = Date.now();
  });
  video.addEventListener("playing", () => {
    stalled = 0;
    failureMessage = "";
    box.hidden = true;
  });
  let lastTime = 0;
  video.addEventListener("timeupdate", () => {
    if (video.currentTime !== lastTime) {
      lastTime = video.currentTime;
      stalled = 0;
    }
  });
  const interval = setInterval(() => {
    if (
      !document.hidden &&
      stalled &&
      Date.now() - stalled > 25000 &&
      !video.ended
    )
      offer();
  }, 2000);
  button.onclick = () => {
    if (PP.isFollower()) return;
    window.dispatchEvent(new Event("requestsourcechange"));
  };
  window.addEventListener("pagehide", () => {
    clearInterval(interval);
  });
})();

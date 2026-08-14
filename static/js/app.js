(() => {
  "use strict";

  const body = document.body;
  const dossier = document.getElementById("auth-dossier");
  let mode = body.dataset.authMode === "register" ? "register" : "login";

  function selectMode(nextMode) {
    mode = nextMode === "register" ? "register" : "login";
    dossier?.querySelectorAll("[data-auth-tab]").forEach((tab) => {
      const selected = tab.dataset.authTab === mode;
      tab.setAttribute("aria-selected", String(selected));
      tab.tabIndex = selected ? 0 : -1;
    });
    dossier?.querySelectorAll("[data-auth-panel]").forEach((panel) => {
      panel.hidden = panel.dataset.authPanel !== mode;
    });
  }

  function openAuth(nextMode = "login", next = "/") {
    if (!dossier) return;
    selectMode(nextMode);
    dossier.querySelectorAll("[data-auth-next]").forEach((input) => { input.value = next; });
    if (!dossier.open) dossier.showModal();
    dossier.querySelector(`[data-auth-panel="${mode}"] input:not([type="hidden"])`)?.focus();
  }

  document.querySelectorAll("[data-open-auth]").forEach((button) => {
    button.addEventListener("click", () => openAuth(button.dataset.openAuth));
  });
  document.querySelectorAll("[data-auth-required]").forEach((link) => {
    link.addEventListener("click", (event) => {
      if (body.dataset.authenticated === "true") return;
      event.preventDefault();
      openAuth("login", link.dataset.next || link.getAttribute("href") || "/");
    });
  });
  dossier?.querySelectorAll("[data-auth-tab]").forEach((tab) => tab.addEventListener("click", () => selectMode(tab.dataset.authTab)));
  dossier?.querySelector("[data-close-auth]")?.addEventListener("click", () => {
    dossier.close();
    sessionStorage.setItem("jamcontests-auth-dismissed", "1");
  });
  dossier?.addEventListener("cancel", () => sessionStorage.setItem("jamcontests-auth-dismissed", "1"));

  if (dossier && (body.dataset.authOpen === "true" || !sessionStorage.getItem("jamcontests-auth-dismissed"))) {
    openAuth(mode);
  } else {
    selectMode(mode);
  }

  const timer = document.querySelector("[data-timer]");
  const deadline = timer?.dataset.deadline ? Date.parse(timer.dataset.deadline) : Number.NaN;
  if (timer && Number.isFinite(deadline)) {
	let interval;
    const fields = {
      days: timer.querySelector("[data-days]"),
      hours: timer.querySelector("[data-hours]"),
      minutes: timer.querySelector("[data-minutes]"),
      seconds: timer.querySelector("[data-seconds]")
    };
    const update = () => {
      const remaining = Math.max(0, deadline - Date.now());
      const totalSeconds = Math.floor(remaining / 1000);
      const values = {
        days: Math.floor(totalSeconds / 86400),
        hours: Math.floor((totalSeconds % 86400) / 3600),
        minutes: Math.floor((totalSeconds % 3600) / 60),
        seconds: totalSeconds % 60
      };
      Object.entries(values).forEach(([key, value]) => { fields[key].textContent = String(value).padStart(2, "0"); });
      if (remaining === 0 && interval) window.clearInterval(interval);
    };
    update();
    interval = window.setInterval(update, 1000);
  }
})();

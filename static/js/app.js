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
    button.addEventListener("click", () => openAuth(button.dataset.openAuth, button.dataset.next || "/"));
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

  if (dossier && (body.dataset.authOpen === "true" || (body.dataset.authAutoOpen === "true" && !sessionStorage.getItem("jamcontests-auth-dismissed")))) {
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
      if (remaining === 0 && interval) {
        // The timer is presentation only; the server computes the canonical
        // stage. Reload so the new effective stage renders (jamDetail
        // rechecks the stage and redirects if it already changed).
        window.clearInterval(interval);
        window.location.reload();
      }
    };
    update();
    interval = window.setInterval(update, 1000);
  }

  const helpDialog = document.getElementById("help-dossier");
  const helpCards = helpDialog ? Array.from(helpDialog.querySelectorAll("[data-help-card]")) : [];
  let helpIndex = 0;
  function showHelpCard(index) {
    if (helpCards.length === 0) return;
    helpIndex = (index + helpCards.length) % helpCards.length;
    helpCards.forEach((card, cardIndex) => {
      card.hidden = cardIndex !== helpIndex;
      card.setAttribute("aria-hidden", String(cardIndex !== helpIndex));
    });
    const dots = helpDialog?.querySelector("[data-help-dots]");
    if (dots) dots.textContent = (helpIndex + 1) + " / " + helpCards.length;
  }
  function openHelp() {
    if (!helpDialog) return;
    showHelpCard(0);
    if (helpDialog.open) return;
    if (typeof helpDialog.showModal === "function") {
      try {
        helpDialog.showModal();
        return;
      } catch (error) {
        // Fall through to the attribute fallback below.
      }
    }
    helpDialog.setAttribute("open", "");
    helpDialog.classList.add("open-fallback");
  }
  function closeHelp() {
    if (helpDialog.open && typeof helpDialog.close === "function") {
      try {
        helpDialog.close();
      } catch (error) {
        // Fall through to attribute removal.
      }
    }
    helpDialog.removeAttribute("open");
    helpDialog.classList.remove("open-fallback");
  }
  document.querySelectorAll("[data-help-open]").forEach((button) => {
    button.addEventListener("click", openHelp);
  });
  helpDialog?.querySelector("[data-help-close]")?.addEventListener("click", closeHelp);
  helpDialog?.querySelector("[data-help-prev]")?.addEventListener("click", () => showHelpCard(helpIndex - 1));
  helpDialog?.querySelector("[data-help-next]")?.addEventListener("click", () => showHelpCard(helpIndex + 1));
  helpDialog?.addEventListener("keydown", (event) => {
    if (event.key === "ArrowRight" || event.key === "ArrowDown") { event.preventDefault(); showHelpCard(helpIndex + 1); }
    if (event.key === "ArrowLeft" || event.key === "ArrowUp") { event.preventDefault(); showHelpCard(helpIndex - 1); }
  });
  let helpTouchX = null;
  helpDialog?.addEventListener("touchstart", (event) => { helpTouchX = event.touches[0].clientX; }, { passive: true });
  helpDialog?.addEventListener("touchend", (event) => {
    if (helpTouchX === null) return;
    const delta = event.changedTouches[0].clientX - helpTouchX;
    helpTouchX = null;
    if (Math.abs(delta) > 40) showHelpCard(helpIndex + (delta < 0 ? 1 : -1));
  }, { passive: true });
})();

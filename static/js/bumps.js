(() => {
  "use strict";

  const csrf = document.querySelector('meta[name="csrf-token"]')?.content || "";
  const panels = Array.from(document.querySelectorAll("[data-bump-product]"));
  const cooldownTimers = new WeakMap();
	const versions = new WeakMap();
	const posting = new WeakSet();

  function nextVersion(panel) {
	const version = (versions.get(panel) || 0) + 1;
	versions.set(panel, version);
	return version;
  }

  function setCooldown(panel, seconds, mutable = true) {
    const button = panel.querySelector("[data-bump-action]");
    if (!button) return;
    window.clearInterval(cooldownTimers.get(panel));
	if (!mutable) {
	  button.disabled = true;
	  button.textContent = "Бампы закрыты";
	  return;
	}
    let remaining = Math.max(0, Number(seconds) || 0);
    const render = () => {
      button.disabled = remaining > 0;
      button.textContent = remaining > 0 ? `Бамп через ${remaining} с` : "Бамп";
      if (remaining > 0) remaining -= 1;
    };
    render();
    if (remaining >= 0 && button.disabled) {
      cooldownTimers.set(panel, window.setInterval(() => {
        render();
        if (!button.disabled) window.clearInterval(cooldownTimers.get(panel));
      }, 1000));
    }
  }

  function applyState(panel, data) {
    if (Number.isFinite(Number(data.count))) panel.querySelector("[data-bump-count]").textContent = String(data.count);
	if (data.cooldown_seconds !== undefined || data.mutable === false) setCooldown(panel, data.cooldown_seconds, data.mutable !== false);
    panel.querySelector("[data-bump-status]").textContent = data.error || "";
  }

  async function refresh(panel) {
	if (posting.has(panel)) return;
	const version = nextVersion(panel);
    const response = await fetch(`/api/products/${panel.dataset.bumpProduct}/bumps`, { headers: { Accept: "application/json" } });
	const data = await response.json().catch(() => ({}));
	if (response.ok && versions.get(panel) === version) applyState(panel, data);
  }

  panels.forEach((panel) => {
    panel.querySelector("[data-bump-action]")?.addEventListener("click", async () => {
      const status = panel.querySelector("[data-bump-status]");
	  const button = panel.querySelector("[data-bump-action]");
	  if (posting.has(panel)) return;
	  posting.add(panel);
	  const version = nextVersion(panel);
	  button.disabled = true;
      status.textContent = "";
      try {
        const response = await fetch(`/api/products/${panel.dataset.bumpProduct}/bumps`, {
          method: "POST",
          headers: { Accept: "application/json", "X-CSRF-Token": csrf }
        });
		const data = await response.json().catch(() => ({}));
		if (versions.get(panel) === version) {
		  applyState(panel, data);
		  if (response.ok) status.textContent = "Бамп учтён.";
		  else {
			if (!data.error) status.textContent = "Не удалось обновить бампы.";
			if (data.mutable === undefined && data.cooldown_seconds === undefined) button.disabled = false;
		  }
		}
      } catch (_) {
        status.textContent = "Не удалось обновить бампы.";
		button.disabled = false;
	  } finally {
		posting.delete(panel);
      }
    });
  });

  async function refreshAll() {
    if (document.hidden) return;
    await Promise.allSettled(panels.map(refresh));
  }

  if (panels.length > 0) {
    refreshAll();
    window.setInterval(refreshAll, 15000);
    document.addEventListener("visibilitychange", () => { if (!document.hidden) refreshAll(); });
  }
})();

(() => {
  "use strict";

  const csrf = document.querySelector('meta[name="csrf-token"]')?.content || "";
  const ballots = Array.from(document.querySelectorAll("[data-vote-nomination]"));
  const pending = new WeakSet();

  function applyCounts(data) {
    if (!Array.isArray(data.counts)) return;
    const counts = new Map(data.counts.map((item) => [`${item.nomination_id}:${item.product_id}`, item.count]));
    ballots.forEach((ballot) => {
      ballot.querySelectorAll("[data-vote-product]").forEach((option) => {
        const key = `${ballot.dataset.voteNomination}:${option.dataset.voteProduct}`;
        const count = option.querySelector("[data-vote-count]");
        if (count && counts.has(key)) count.textContent = String(counts.get(key));
      });
    });
  }

  async function refreshCounts() {
    if (document.hidden || ballots.length === 0) return;
    const jamID = ballots[0].dataset.voteJam;
    const response = await fetch(`/api/jams/${jamID}/vote-counts`, { headers: { Accept: "application/json" } });
    const data = await response.json().catch(() => ({}));
    if (response.ok) applyCounts(data);
    else if (response.status === 404) {
      ballots.forEach((ballot) => {
        const button = ballot.querySelector("[data-vote-action]");
        if (button) button.disabled = true;
        ballot.querySelector("[data-vote-status]").textContent = data.error || "Голосование закрыто.";
      });
    }
  }

  ballots.forEach((ballot) => {
    ballot.querySelector("[data-vote-action]")?.addEventListener("click", async () => {
      if (pending.has(ballot)) return;
      const selected = ballot.querySelector('input[type="radio"]:checked');
      const status = ballot.querySelector("[data-vote-status]");
      const button = ballot.querySelector("[data-vote-action]");
      if (!selected) {
        status.textContent = "Выберите продукт.";
        return;
      }
      pending.add(ballot);
      button.disabled = true;
      status.textContent = "";
      try {
        const response = await fetch(`/api/jams/${ballot.dataset.voteJam}/nominations/${ballot.dataset.voteNomination}/vote`, {
          method: "POST",
          headers: { Accept: "application/json", "Content-Type": "application/json", "X-CSRF-Token": csrf },
          body: JSON.stringify({ product_id: Number(selected.value) })
        });
        const data = await response.json().catch(() => ({}));
        status.textContent = response.ok ? "Голос учтён." : (data.error || "Не удалось сохранить голос.");
        if (response.ok) await refreshCounts();
      } catch (_) {
        status.textContent = "Не удалось сохранить голос.";
      } finally {
        pending.delete(ballot);
        button.disabled = false;
      }
    });
  });

  if (ballots.length > 0) {
    window.setInterval(refreshCounts, 15000);
    document.addEventListener("visibilitychange", () => { if (!document.hidden) refreshCounts(); });
  }
})();

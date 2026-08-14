(() => {
  "use strict";

  const root = document.getElementById("questionnaire");
  if (!root || root.dataset.writable !== "true") return;

  const csrf = document.querySelector('meta[name="csrf-token"]')?.content || "";
  const completeForm = document.getElementById("questionnaire-complete");
  const responseStatus = document.getElementById("response-status");
  const timers = new Map();
  const pending = new Map();
  const dirty = new Set();
  const versions = new Map();

  function payload(question) {
    const result = { question_id: Number(question.dataset.questionId) };
    if (question.dataset.questionType === "short_text") {
      result.value = question.querySelector(".questionnaire-text").value;
    } else {
      result.option_ids = Array.from(question.querySelectorAll(".questionnaire-choice:checked"), (input) => Number(input.value));
    }
    return result;
  }

  function show(question, message, kind = "") {
    const status = question.querySelector("[data-question-status]");
    status.textContent = message;
    status.dataset.kind = kind;
  }

  async function save(question) {
    const previous = pending.get(question);
    if (previous) await previous.catch(() => {});

    const version = versions.get(question) || 0;
    show(question, "Сохранение...");
    const request = fetch(root.dataset.autosaveUrl, {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Accept": "application/json",
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf
      },
      body: JSON.stringify(payload(question))
    }).then(async (response) => {
      const data = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(data.error || "Не удалось сохранить ответ.");
      if ((versions.get(question) || 0) === version) dirty.delete(question);
      show(question, data.returned_to_draft ? "Сохранено. Анкета снова стала черновиком." : "Сохранено.");
      if (responseStatus) responseStatus.textContent = "черновик";
    }).catch((error) => {
      show(question, error.message || "Не удалось сохранить ответ.", "error");
      throw error;
    }).finally(() => {
      if (pending.get(question) === request) pending.delete(question);
    });
    pending.set(question, request);
    return request;
  }

  root.querySelectorAll("[data-question]").forEach((question) => {
    const text = question.querySelector(".questionnaire-text");
    if (text) {
      text.addEventListener("input", () => {
        versions.set(question, (versions.get(question) || 0) + 1);
        dirty.add(question);
        clearTimeout(timers.get(question));
        timers.set(question, setTimeout(() => save(question).catch(() => {}), 500));
      });
      text.addEventListener("blur", () => {
        if (!dirty.has(question)) return;
        clearTimeout(timers.get(question));
        save(question).catch(() => {});
      });
    }
    question.querySelectorAll(".questionnaire-choice").forEach((choice) => {
      choice.addEventListener("change", () => {
        versions.set(question, (versions.get(question) || 0) + 1);
        dirty.add(question);
        save(question).catch(() => {});
      });
    });
  });

  completeForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = completeForm.querySelector('button[type="submit"]');
    if (button) button.disabled = true;
    timers.forEach((timer) => clearTimeout(timer));
    try {
      await Promise.all(Array.from(dirty, (question) => save(question)));
      await Promise.all(Array.from(pending.values()));
      completeForm.submit();
    } catch (_) {
      if (button) button.disabled = false;
    }
  });
})();

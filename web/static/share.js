(() => {
  "use strict";
  let content = document.querySelector("#shared-content");
  // Retain the exact retry key/body until a successful write or authoritative
  // refresh settles the operation. Rejected retries alone cannot settle it.
  let pendingWrite = null;
  let writing = false;
  let refreshing = false;
  let requiresRefresh = false;
  let unavailable = false;
  const endpoint = location.pathname.replace(/\/$/, "");

  function showStatus(text, retry = false, refresh = false) {
    const status = content.querySelector("#share-status");
    if (!status) return;
    status.hidden = !text;
    status.querySelector("span").textContent = text;
    content.querySelector("#share-retry").hidden = !retry;
    content.querySelector("#share-refresh").hidden = !refresh;
  }
  function updateWriteControls() {
    content.querySelectorAll("[data-shared-check]").forEach(input => {
      input.disabled = Boolean(pendingWrite) || writing || refreshing || requiresRefresh || unavailable;
    });
  }
  function showUnavailable() {
    unavailable = true;
    pendingWrite = null;
    document.querySelector('meta[name="share-csrf"]')?.remove();
    const box = document.createElement("div");
    box.className = "share-unavailable";
    const title = document.createElement("h1");
    title.className = "share-title";
    title.textContent = "This link is unavailable";
    const description = document.createElement("p");
    description.className = "shared-subtitle";
    description.textContent = "Shared lists are available for 6 hours. Ask your friend for a new link.";
    box.append(title, description);
    content.replaceChildren(box);
    delete content.dataset.expiresAt;
  }
  function updateAvailability() {
    if (unavailable) return false;
    const remaining = Number(content.dataset.expiresAt) - Date.now();
    if (!(remaining > 0)) {
      showUnavailable();
      return false;
    }
    const minutes = Math.ceil(remaining / 60000);
    const hours = Math.floor(minutes / 60);
    const rest = minutes % 60;
    content.querySelector("#share-time").textContent = hours ? `${hours}h${rest ? ` ${rest}m` : ""} left` : `${minutes}m left`;
    const inputs = [...content.querySelectorAll("[data-shared-check]")];
    const picked = inputs.filter(input => input.checked).length;
    content.querySelector("#shared-progress").textContent = picked === inputs.length ? "All picked up. Thank you!" : `${picked} of ${inputs.length} picked up`;
    return true;
  }
  async function refreshChecklist() {
    if (refreshing || writing || !updateAvailability()) return false;
    refreshing = true;
    updateWriteControls();
    const focusedPosition = document.activeElement?.closest("[data-shared-position]")?.dataset.sharedPosition;
    try {
      const response = await fetch(endpoint, { credentials: "omit", cache: "no-store", headers: { Accept: "text/html" } });
      if (response.status === 404) {
        showUnavailable();
        return false;
      }
      if (!response.ok) throw new Error("Refresh failed. Try again shortly.");
      const parsed = new DOMParser().parseFromString(await response.text(), "text/html");
      const next = parsed.querySelector("#shared-content");
      const token = parsed.querySelector('meta[name="share-csrf"]');
      if (!next || !token) throw new Error("Refresh failed.");
      // An in-flight response must never restore items after the local deadline.
      if (!updateAvailability()) return false;
      content.replaceWith(next);
      content = next;
      document.querySelector('meta[name="share-csrf"]').content = token.content;
      requiresRefresh = false;
      if (!updateAvailability()) return false;
      // An authoritative refresh also settles an unknown write outcome.
      pendingWrite = null;
      if (focusedPosition) content.querySelector(`[data-shared-position="${CSS.escape(focusedPosition)}"] input`)?.focus({ preventScroll: true });
      return true;
    } catch (error) {
      if (!unavailable) showStatus(error.message, Boolean(pendingWrite), true);
      return false;
    } finally {
      refreshing = false;
      updateWriteControls();
    }
  }
  async function savePendingWrite() {
    if (!pendingWrite || writing || refreshing || !updateAvailability()) return;
    const attempt = pendingWrite;
    writing = true;
    updateWriteControls();
    showStatus("Saving…");
    try {
      const response = await fetch(`${endpoint}/items/${attempt.position}`, {
        method: "PATCH", credentials: "omit", cache: "no-store",
        headers: {
          "Content-Type": "application/json",
          "X-Share-CSRF": document.querySelector('meta[name="share-csrf"]').content,
          "Idempotency-Key": attempt.key,
        },
        body: JSON.stringify(attempt.body),
      });
      if (!updateAvailability()) return;
      if (response.status === 404) {
        showUnavailable();
        return;
      }
      if (!response.ok) {
        const error = new Error(response.status === 409 ? "The shared checklist changed." : "The change was rejected.");
        error.status = response.status;
        throw error;
      }
      pendingWrite = null;
      writing = false;
      // A settled write still needs authoritative revisions before another edit.
      requiresRefresh = true;
      if (await refreshChecklist()) showStatus("Saved.");
      else if (!unavailable) showStatus("Saved. Refresh failed; load the current checklist before making another change.", false, true);
    } catch (error) {
      writing = false;
      if (!updateAvailability()) return;
      if (error.status && error.status < 500) {
        requiresRefresh = true;
        const refreshed = await refreshChecklist();
        if (!unavailable) showStatus(refreshed ? `${error.message} Refreshed; review it and try again.` : `${error.message} Refresh failed.`, Boolean(pendingWrite), !refreshed);
      } else {
        showStatus("Not synced. The change may have reached the server. Retry safely or refresh.", true, true);
      }
    } finally {
      writing = false;
      updateWriteControls();
    }
  }
  document.addEventListener("change", event => {
    if (!event.target.matches("[data-shared-check]")) return;
    const input = event.target;
    const checked = input.checked;
    input.checked = !checked;
    if (pendingWrite || writing || refreshing || requiresRefresh || !updateAvailability()) return;
    const row = input.closest("[data-shared-position]");
    pendingWrite = {
      position: Number(row.dataset.sharedPosition),
      key: crypto.randomUUID(),
      body: {
        picked_up: checked,
        expected_revision: Number(row.dataset.revision),
      },
    };
    void savePendingWrite();
  });
  document.addEventListener("click", async event => {
    if (event.target.closest("#share-retry")) void savePendingWrite();
    if (event.target.closest("#share-refresh") && !writing) {
      if (await refreshChecklist()) {
        showStatus("Refreshed. Review the current checklist.");
      }
    }
  });
  function revalidate() {
    if (!updateAvailability() || document.hidden || pendingWrite || writing || refreshing) return;
    void refreshChecklist();
  }
  window.addEventListener("focus", revalidate);
  document.addEventListener("visibilitychange", revalidate);
  updateAvailability();
  setInterval(updateAvailability, 1000);
  setInterval(revalidate, 30000);
})();

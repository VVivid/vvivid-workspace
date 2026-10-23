(() => {
  "use strict";
  const status = document.querySelector("#sync-status");
  const retryButton = document.querySelector("#retry-write");
  const refreshButton = document.querySelector("#refresh-view");
  const productDialog = document.querySelector("#product-dialog");
  const quantityDialog = document.querySelector("#quantity-dialog");
  const shareDialog = document.querySelector("#share-dialog");
  let selectingShare = false;
  // Keep unknown writes intact. A share needs its original result to recover the URL.
  let pendingWrite = null;
  let writing = false;
  let refreshing = false;
  let quantityItem = null;
  let dialogTrigger = null;
  let detailRequest = 0;
  let refreshController = null;

  function message(text, retry = false, refresh = retry) {
    status.hidden = !text;
    status.querySelector("span").textContent = text;
    retryButton.hidden = !retry;
    refreshButton.hidden = !refresh;
  }
  function disableWrites(disabled) {
    document.querySelectorAll("[data-picked-up], [data-edit-quantity], [data-add-product]").forEach(control => {
      control.disabled = disabled || selectingShare;
    });
    document.querySelectorAll("[data-share-select], #share-cancel, #share-all, #share-start").forEach(control => {
      control.disabled = disabled;
    });
    updateShareCount(disabled);
  }
  async function jsonRequest(url, options = {}) {
    const response = await fetch(url, { credentials: "same-origin", cache: "no-store", ...options });
    let data;
    try {
      data = await response.json();
    } catch {
      data = null;
    }
    if (!response.ok) {
      const error = new Error(data?.message || (response.status === 401 ? "Sign in again to continue." : "The request failed."));
      error.status = response.status;
      throw error;
    }
    return data;
  }
  async function refreshView() {
    if (refreshing || writing) return false;
    refreshing = true;
    refreshController = new AbortController();
    const currentRefresh = refreshController;
    const focused = document.activeElement;
    const focusItem = focused?.closest("[data-item-id]")?.dataset.itemId;
    const focusRole = focused?.hasAttribute("data-picked-up")
      ? "[data-picked-up]"
      : focused?.hasAttribute("data-edit-quantity") ? "[data-edit-quantity]" : "[data-product-id]";
    try {
      const response = await fetch(location.href, { credentials: "same-origin", cache: "no-store", headers: { "Accept": "text/html" }, signal: currentRefresh.signal });
      if (!response.ok) throw new Error("Refresh failed.");
      const parsed = new DOMParser().parseFromString(await response.text(), "text/html");
      if (currentRefresh.signal.aborted) return false;
      const content = parsed.querySelector("#content");
      const csrf = parsed.querySelector('meta[name="csrf-token"]');
      if (!content || !csrf) throw new Error("Sign in again to continue.");
      const pageError = content.querySelector(".page-error");
      if (pageError) throw new Error(pageError.textContent);
      setSharing(false);
      document.querySelector("#content").replaceWith(content);
      document.querySelector('meta[name="csrf-token"]').content = csrf.content;
      disableWrites(Boolean(pendingWrite));
      if (focusItem && !productDialog.open && !quantityDialog.open) {
        document.querySelector(`[data-item-id="${CSS.escape(focusItem)}"] ${focusRole}`)?.focus({ preventScroll: true });
      }
      return true;
    } catch (error) {
      if (error.name !== "AbortError") {
        message(pendingWrite ? "Not synced. Retry the pending change or refresh." : error.message, Boolean(pendingWrite), true);
      }
      return false;
    } finally {
      if (refreshController === currentRefresh) {
        refreshing = false;
        refreshController = null;
      }
    }
  }
  function restoreFocus(focus) {
    if (!focus) return;
    const selector = focus.item ? `[data-item-id="${CSS.escape(focus.item)}"] ${focus.control}` : `[data-add-product="${CSS.escape(focus.product)}"]`;
    (document.querySelector(selector) || document.querySelector("#content")).focus({ preventScroll: true });
  }
  async function sendPending() {
    if (!pendingWrite || writing) return;
    refreshController?.abort();
    refreshController = null;
    refreshing = false;
    const focus = pendingWrite.focus;
    const creatingShare = pendingWrite.kind === "share";
    writing = true;
    disableWrites(true);
    message("Saving…");
    try {
      const result = await jsonRequest(pendingWrite.url, {
        method: pendingWrite.method,
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": document.querySelector('meta[name="csrf-token"]').content,
          "Idempotency-Key": pendingWrite.key,
        },
        body: JSON.stringify(pendingWrite.body),
      });
      if (creatingShare) {
        const code = result?.record?.code;
        if (typeof code !== "string" || code.length !== 5 || /[^A-Za-z]/.test(code)) {
          throw new Error("The share link response was incomplete.");
        }
        document.querySelector("#share-url").value = new URL(`/s/${code}`, location.origin).href;
      }
      pendingWrite = null;
      writing = false;
      if (creatingShare) {
        setSharing(false);
        document.querySelector("#copy-share-status").textContent = "";
        dialogTrigger = document.querySelector("#share-start");
        shareDialog.showModal();
        document.querySelector("#share-url").focus();
        message("");
        return;
      }
      if (await refreshView()) {
        message("Saved.");
        restoreFocus(focus);
      } else {
        message("Saved. Refresh failed; reload to view the current list.");
      }
    } catch (error) {
      writing = false;
      if (creatingShare && pendingWrite && (!error.status || error.status >= 500)) {
        pendingWrite.outcomeUnknown = true;
      }
      if (error.status && error.status < 500) {
        // A rejection settles this attempt, but cannot disprove an earlier
        // unknown share creation. A private list refresh cannot recover its URL.
        const unknownShare = creatingShare && pendingWrite?.outcomeUnknown;
        if (!unknownShare) pendingWrite = null;
        const refreshed = await refreshView();
        if (unknownShare) {
          message("Not synced. The link may already exist. Retry to recover its URL.", true);
        } else if (refreshed) {
          message(error.status === 409 ? "The item or list changed. Refreshed; review it and try again." : error.message);
        } else {
          const reason = error.status === 409 ? "The list changed." : error.message;
          message(`${reason} Refresh failed; use Refresh to load the current list before trying again.`, false, true);
        }
      } else {
        message(creatingShare
          ? "Not synced. The link may already exist. Retry to recover its URL."
          : "Not synced. The change may have reached the server. Retry safely or refresh.", true);
      }
    } finally {
      writing = false;
      disableWrites(Boolean(pendingWrite));
    }
  }
  function mutate(method, url, body, kind = "list") {
    if (pendingWrite || writing) return;
    const active = document.activeElement;
    const row = active?.closest("[data-item-id]") || dialogTrigger?.closest("[data-item-id]");
    const focus = row
      ? { item: row.dataset.itemId, control: method === "PATCH" && body.picked_up !== undefined ? "[data-picked-up]" : "[data-edit-quantity]" }
      : active?.dataset.addProduct ? { product: active.dataset.addProduct } : null;
    pendingWrite = { method, url, body, focus, kind, key: crypto.randomUUID(), outcomeUnknown: false };
    void sendPending();
  }
  function listContext() {
    const main = document.querySelector("#content");
    return { id: main.dataset.listId, revision: Number(main.dataset.listRevision) };
  }
  function updateShareCount(disabled = Boolean(pendingWrite) || writing) {
    const create = document.querySelector("#share-create");
    if (!create) return;
    const choices = [...document.querySelectorAll("[data-share-select]")];
    const selected = choices.filter(input => input.checked).length;
    create.disabled = disabled || selected === 0;
    create.textContent = `Create link${selected ? ` · ${selected} item${selected === 1 ? "" : "s"}` : ""}`;
    const all = document.querySelector("#share-all");
    if (all) all.textContent = selected === choices.length ? "Deselect all" : "Select all";
    for (const input of choices) {
      input.closest(".product-row").classList.toggle("share-selected", input.checked);
    }
  }
  function setSharing(active) {
    selectingShare = active;
    const main = document.querySelector("#content");
    main.classList.toggle("selecting-share", active);
    for (const id of ["share-selection-tools", "share-create-tools"]) {
      const element = document.getElementById(id);
      if (element) element.hidden = !active;
    }
    const start = document.querySelector("#share-start");
    if (start) start.hidden = active;
    main.querySelectorAll(".add-products").forEach(link => { link.hidden = active; });
    main.querySelectorAll(".product-details").forEach(button => { button.tabIndex = active ? -1 : 0; });
    main.querySelectorAll("[data-share-select]").forEach(input => { input.checked = false; });
    disableWrites(Boolean(pendingWrite) || writing);
  }
  function itemURL(item) {
    return `/api/v1/lists/${encodeURIComponent(listContext().id)}/items/${encodeURIComponent(item.id)}`;
  }
  function closeDialog(dialog) { dialog.close(); }
  for (const dialog of [productDialog, quantityDialog, shareDialog]) {
    dialog.addEventListener("close", () => {
      dialogTrigger?.focus({ preventScroll: true });
    });
    dialog.addEventListener("click", event => {
      if (event.target === dialog && (event.offsetX < 0 || event.offsetY < 0 || event.offsetX > dialog.clientWidth || event.offsetY > dialog.clientHeight)) {
        closeDialog(dialog);
      }
    });
  }
  function node(tag, text, className) {
    const element = document.createElement(tag);
    if (text !== undefined) element.textContent = text;
    if (className) element.className = className;
    return element;
  }
  function detailSection(title) {
    const section = node("section", undefined, "detail-section");
    section.append(node("h3", title));
    return section;
  }
  function sourceLink(label, url) {
    const link = node("a", label);
    link.href = url;
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    return link;
  }
  function packageText(product) {
    const size = product.package_size && product.package_unit ? `${product.package_size} ${product.package_unit}` : "";
    return product.package_count ? `${product.package_count}${size ? ` × ${size}` : " per pack"}` : size || "Pack details unknown";
  }
  function renderProduct(product) {
    document.querySelector("#product-title").textContent = product.name;
    const content = document.querySelector("#product-detail-content");
    content.replaceChildren();
    if (product.media?.[0]) {
      const image = node("img", undefined, "product-photo");
      image.src = product.media[0].image_url;
      image.alt = product.name;
      image.referrerPolicy = "no-referrer";
      image.addEventListener("error", () => {
        image.replaceWith(node("p", "Product photo unavailable.", "detail-note"));
      }, { once: true });
      content.append(image, sourceLink("Photo source", product.media[0].source_url));
    } else {
      content.append(node("p", "No product photo saved.", "detail-note"));
    }
    const variant = [product.brand, product.flavor, packageText(product)].filter(Boolean).join(" · ");
    content.append(node("p", variant, "detail-note"));
    if (product.description) content.append(node("p", product.description, "detail-description"));
    if (product.nutrition) {
      const nutrition = product.nutrition;
      const section = detailSection(`Nutrition · per ${nutrition.serving_size} ${nutrition.serving_unit}`);
      const facts = node("dl");
      for (const fact of nutrition.facts) {
        facts.append(node("dt", fact.name), node("dd", fact.value === null ? "Unknown" : `${fact.value} ${fact.unit}`));
      }
      section.append(facts, node("p", `${nutrition.verification} · observed ${new Date(nutrition.observed_at).toLocaleDateString()}`, "detail-note"), sourceLink("Nutrition source", nutrition.source_url));
      content.append(section);
    } else {
      content.append(node("p", "Nutrition unknown.", "detail-note"));
    }
    const listings = detailSection("Retailer details");
    for (const listing of product.listings || []) {
      listings.append(sourceLink(`${listing.retailer}${listing.item_id ? ` · ${listing.item_id}` : ""}`, listing.product_url));
      let priceLabel = "Price unknown";
      if (listing.price_minor !== null) {
        const formatter = new Intl.NumberFormat(undefined, { style: "currency", currency: listing.currency });
        const fractionDigits = formatter.resolvedOptions().maximumFractionDigits;
        priceLabel = `Observed price: ${formatter.format(listing.price_minor / (10 ** fractionDigits))}`;
      }
      const availabilityLabel = listing.availability === "unknown" ? "local availability unknown" : listing.availability.replaceAll("_", " ");
      const observation = [priceLabel, availabilityLabel];
      if (listing.store_location) observation.push(listing.store_location);
      else if (listing.price_minor !== null) observation.push("store unspecified");
      if (listing.observed_at) observation.push(`observed ${new Date(listing.observed_at).toLocaleDateString()}`);
      listings.append(node("p", observation.join(" · "), "detail-note"));
    }
    if (!product.listings?.length) listings.append(node("p", "No retailer listing saved.", "detail-note"));
    content.append(listings);
    const sources = detailSection("Sources");
    for (const source of product.sources || []) {
      sources.append(sourceLink(`${source.fields.join(", ")} · ${source.verification} · ${new Date(source.observed_at).toLocaleDateString()}`, source.url));
    }
    content.append(sources);
  }
  document.addEventListener("click", async event => {
    const target = event.target.closest("button");
    if (!target) return;
    if (target.id === "share-start") {
      if (pendingWrite || writing || refreshing) return;
      setSharing(true);
      document.querySelector("[data-share-select]")?.focus();
      return;
    }
    if (target.id === "share-cancel") {
      setSharing(false);
      document.querySelector("#share-start")?.focus();
      return;
    }
    if (target.id === "share-all") {
      const choices = [...document.querySelectorAll("[data-share-select]")];
      const checked = !choices.every(input => input.checked);
      for (const input of choices) input.checked = checked;
      updateShareCount();
      return;
    }
    if (target.id === "share-create") {
      const list = listContext();
      const items = [...document.querySelectorAll("[data-share-select]:checked")].map(input => {
        const row = input.closest("[data-item-id]");
        return {
          item_id: row.dataset.itemId,
          expected_revision: Number(row.dataset.revision),
          expected_product_revision: Number(row.dataset.productRevision),
        };
      });
      if (items.length) {
        mutate("POST", `/api/v1/lists/${encodeURIComponent(list.id)}/shares`, { expected_revision: list.revision, items }, "share");
      }
      return;
    }
    if (target.id === "copy-share") {
      const field = document.querySelector("#share-url");
      try {
        await navigator.clipboard.writeText(field.value);
        document.querySelector("#copy-share-status").textContent = "Link copied.";
      } catch {
        field.focus();
        field.select();
        document.querySelector("#copy-share-status").textContent = "Link selected. Press and hold, or use your keyboard, to copy.";
      }
      return;
    }
    if (target.hasAttribute("data-close-dialog")) {
      closeDialog(target.closest("dialog"));
      return;
    }
    if (target.dataset.productId) {
      dialogTrigger = target;
      document.querySelector("#product-title").textContent = "Product details";
      document.querySelector("#product-detail-content").replaceChildren(node("p", "Loading…", "detail-note"));
      productDialog.showModal();
      const request = ++detailRequest;
      try {
        const product = await jsonRequest(`/api/v1/products/${encodeURIComponent(target.dataset.productId)}`);
        if (request === detailRequest) renderProduct(product);
      } catch (error) {
        if (request === detailRequest) {
          document.querySelector("#product-detail-content").replaceChildren(node("p", error.message, "page-error"));
        }
      }
    }
    if (target.hasAttribute("data-edit-quantity")) {
      const row = target.closest("[data-item-id]");
      quantityItem = { id: row.dataset.itemId, revision: Number(row.dataset.revision) };
      dialogTrigger = target;
      document.querySelector("#quantity-value").value = target.dataset.quantity;
      quantityDialog.showModal();
      document.querySelector("#quantity-value").select();
    }
    if (target.dataset.addProduct) {
      const list = listContext();
      mutate("POST", `/api/v1/lists/${encodeURIComponent(list.id)}/items`, {
        product_id: target.dataset.addProduct,
        quantity: target.dataset.addQuantity || "1",
        merge: "increment",
        retailer: target.dataset.addRetailer || null,
        expected_list_revision: list.revision,
        expected_product_revision: target.dataset.addProductRevision ? Number(target.dataset.addProductRevision) : null,
      });
    }
  });
  document.addEventListener("change", event => {
    if (event.target.hasAttribute("data-share-select")) {
      updateShareCount();
      return;
    }
    if (!event.target.hasAttribute("data-picked-up")) return;
    const checkbox = event.target;
    const checked = checkbox.checked;
    checkbox.checked = !checked;
    const row = checkbox.closest("[data-item-id]");
    mutate("PATCH", itemURL({ id: row.dataset.itemId }), { picked_up: checked, expected_revision: Number(row.dataset.revision), expected_list_revision: listContext().revision });
  });
  document.querySelector("#quantity-form").addEventListener("submit", event => {
    event.preventDefault();
    const value = document.querySelector("#quantity-value").value;
    if (!/^(0|[1-9][0-9]{0,8})(\.[0-9]{1,6})?$/.test(value) || !/[1-9]/.test(value)) {
      document.querySelector("#quantity-value").setCustomValidity("Enter a positive decimal quantity.");
      document.querySelector("#quantity-value").reportValidity();
      return;
    }
    closeDialog(quantityDialog);
    mutate("PATCH", itemURL(quantityItem), { quantity: value, expected_revision: quantityItem.revision, expected_list_revision: listContext().revision });
  });
  document.querySelector("#quantity-value").addEventListener("input", event => {
    event.target.setCustomValidity("");
  });
  document.querySelector("#remove-item").addEventListener("click", () => {
    closeDialog(quantityDialog);
    mutate("DELETE", itemURL(quantityItem), { expected_revision: quantityItem.revision, expected_list_revision: listContext().revision });
  });
  retryButton.addEventListener("click", () => {
    void sendPending();
  });
  refreshButton.addEventListener("click", async () => {
    if (await refreshView()) {
      if (pendingWrite?.kind === "share") {
        message("Not synced. The link may already exist. Retry to recover its URL.", true);
        return;
      }
      // Private list state settles list edits, but never a lost share URL.
      pendingWrite = null;
      disableWrites(false);
      message("Refreshed. Review the current list before making another change.");
    }
  });
  function revalidate() {
    if (!document.hidden && !pendingWrite && !writing && !productDialog.open && !quantityDialog.open && !shareDialog.open && !selectingShare && document.activeElement?.tagName !== "INPUT") void refreshView();
  }
  window.addEventListener("focus", revalidate);
  document.addEventListener("visibilitychange", revalidate);
  setInterval(revalidate, 30000);
})();

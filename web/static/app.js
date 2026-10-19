(() => {
  "use strict";
  const status = document.querySelector("#sync-status");
  const retryButton = document.querySelector("#retry-write");
  const refreshButton = document.querySelector("#refresh-view");
  const productDialog = document.querySelector("#product-dialog");
  const quantityDialog = document.querySelector("#quantity-dialog");
  // Keep an unknown write outcome intact so Retry uses the same key and body.
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
      control.disabled = disabled;
    });
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
    const focus = pendingWrite.focus;
    writing = true;
    disableWrites(true);
    message("Saving…");
    try {
      await jsonRequest(pendingWrite.url, {
        method: pendingWrite.method,
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": document.querySelector('meta[name="csrf-token"]').content,
          "Idempotency-Key": pendingWrite.key,
        },
        body: JSON.stringify(pendingWrite.body),
      });
      pendingWrite = null;
      writing = false;
      if (await refreshView()) {
        message("Saved.");
        restoreFocus(focus);
      } else {
        message("Saved. Refresh failed; reload to view the current list.");
      }
    } catch (error) {
      writing = false;
      if (error.status && error.status < 500) {
        // A rejected write is settled; reloading may still fail independently.
        pendingWrite = null;
        const refreshed = await refreshView();
        if (refreshed) {
          message(error.status === 409 ? "The item or list changed. Refreshed; review it and try again." : error.message);
        } else {
          const reason = error.status === 409 ? "The list changed." : error.message;
          message(`${reason} Refresh failed; use Refresh to load the current list before trying again.`, false, true);
        }
      } else {
        message("Not synced. The change may have reached the server. Retry safely or refresh.", true);
      }
    } finally {
      writing = false;
      disableWrites(Boolean(pendingWrite));
    }
  }
  function mutate(method, url, body) {
    if (pendingWrite || writing) return;
    refreshController?.abort();
    refreshController = null;
    refreshing = false;
    const active = document.activeElement;
    const row = active?.closest("[data-item-id]") || dialogTrigger?.closest("[data-item-id]");
    const focus = row
      ? { item: row.dataset.itemId, control: method === "PATCH" && body.picked_up !== undefined ? "[data-picked-up]" : "[data-edit-quantity]" }
      : active?.dataset.addProduct ? { product: active.dataset.addProduct } : null;
    pendingWrite = { method, url, body, focus, key: crypto.randomUUID() };
    void sendPending();
  }
  function listContext() {
    const main = document.querySelector("#content");
    return { id: main.dataset.listId, revision: Number(main.dataset.listRevision) };
  }
  function itemURL(item) {
    return `/api/v1/lists/${encodeURIComponent(listContext().id)}/items/${encodeURIComponent(item.id)}`;
  }
  function closeDialog(dialog) { dialog.close(); }
  for (const dialog of [productDialog, quantityDialog]) {
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
      // Discard a possibly delivered write only after loading authoritative state.
      pendingWrite = null;
      disableWrites(false);
      message("Refreshed. Review the current list before making another change.");
    }
  });
  function revalidate() {
    if (!document.hidden && !pendingWrite && !writing && !productDialog.open && !quantityDialog.open && document.activeElement?.tagName !== "INPUT") void refreshView();
  }
  window.addEventListener("focus", revalidate);
  document.addEventListener("visibilitychange", revalidate);
  setInterval(revalidate, 30000);
})();

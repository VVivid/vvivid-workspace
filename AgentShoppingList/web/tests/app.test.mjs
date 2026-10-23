import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { setImmediate } from "node:timers/promises";
import test from "node:test";
import vm from "node:vm";

const script = await readFile(new URL("../static/app.js", import.meta.url), "utf8");

// Model only the DOM operations used by these owner flows. Registered production
// event listeners drive every mutation; app.js state and functions stay private.
function ownerPage(responses) {
  let nextKey = 0;
  let nextPage = 0;
  const requests = [];
  const refreshPages = new Map();
  const windowListeners = new Map();
  const intervals = new Map();
  let document;

  function element(tagName, { id = "", dataset = {}, classes = [], ...properties } = {}) {
    const listeners = new Map();
    const classNames = new Set(classes);
    return {
      tagName: tagName.toUpperCase(), id, dataset, children: [], parentElement: null,
      disabled: false, hidden: false, checked: false, value: "", textContent: "", open: false,
      ...properties,
      classList: { toggle(name, enabled) { if (enabled) classNames.add(name); else classNames.delete(name); } },
      matches(selector) {
        if (selector.startsWith("#")) return this.id === selector.slice(1);
        if (selector.startsWith(".")) return classNames.has(selector.slice(1));
        const attribute = selector.match(/^\[([^\]=]+)(?:="([^"]*)")?\](:checked)?$/);
        if (attribute) {
          const key = attribute[1].replace(/^data-/, "").replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
          return Object.hasOwn(dataset, key) && (attribute[2] === undefined || dataset[key] === attribute[2]) && (!attribute[3] || this.checked);
        }
        return selector === this.tagName.toLowerCase();
      },
      hasAttribute(name) { return this.matches(`[${name}]`); },
      closest(selector) {
        for (let current = this; current; current = current.parentElement) if (current.matches(selector)) return current;
        return null;
      },
      querySelectorAll(selector) {
        const descendants = this.children.flatMap(child => [child, ...child.querySelectorAll("*")]);
        if (selector === "*") return descendants;
        return descendants.filter(child => selector.split(",").some(part => child.matches(part.trim())));
      },
      querySelector(selector) {
        const separator = selector.indexOf(" ");
        if (separator >= 0) return this.querySelector(selector.slice(0, separator))?.querySelector(selector.slice(separator + 1)) ?? null;
        return this.querySelectorAll(selector)[0] ?? null;
      },
      append(...children) { for (const child of children) { child.parentElement = this; this.children.push(child); } },
      replaceWith(next) {
        const index = this.parentElement.children.indexOf(this);
        next.parentElement = this.parentElement;
        this.parentElement.children[index] = next;
      },
      addEventListener(type, listener) {
        if (!listeners.has(type)) listeners.set(type, []);
        listeners.get(type).push(listener);
      },
      async dispatch(type, event) { for (const listener of listeners.get(type) ?? []) await listener(event); },
      focus() { document.activeElement = this; },
      showModal() { this.open = true; },
    };
  }

  function list({ listID = "owner-list", listRevision = 4, itemRevision = 7, productRevision = 11, pickedUp = false } = {}) {
    const main = element("main", { id: "content", dataset: { listId: listID, listRevision: String(listRevision) } });
    const row = element("li", { dataset: { itemId: "owner-item", revision: String(itemRevision), productRevision: String(productRevision) }, classes: ["product-row"] });
    row.append(
      element("button", { dataset: { productId: "owner-product" }, classes: ["product-details"] }),
      element("button", { dataset: { editQuantity: "", quantity: "1" } }),
      element("input", { dataset: { shareSelect: "" } }),
      element("input", { dataset: { pickedUp: "" }, checked: pickedUp }),
    );
    const selection = element("div", { id: "share-selection-tools", hidden: true });
    selection.append(element("button", { id: "share-cancel" }), element("button", { id: "share-all" }));
    const creation = element("div", { id: "share-create-tools", hidden: true });
    creation.append(element("button", { id: "share-create", disabled: true }));
    main.append(element("button", { id: "share-start" }), selection, row, creation, element("a", { classes: ["add-products"] }));
    return main;
  }

  document = element("document");
  document.hidden = false;
  document.activeElement = null;
  const csrf = element("meta", { id: "csrf", content: "initial-csrf" });
  const status = element("div", { id: "sync-status", hidden: true });
  status.append(element("span"));
  document.append(
    csrf, status, element("button", { id: "retry-write", hidden: true }), element("button", { id: "refresh-view", hidden: true }),
    list(), element("dialog", { id: "product-dialog" }), element("dialog", { id: "quantity-dialog" }), element("dialog", { id: "share-dialog" }),
    element("input", { id: "share-url" }), element("p", { id: "copy-share-status" }),
    element("form", { id: "quantity-form" }), element("input", { id: "quantity-value" }), element("button", { id: "remove-item" }),
  );
  const querySelector = document.querySelector.bind(document);
  document.querySelector = selector => selector === 'meta[name="csrf-token"]' ? csrf : querySelector(selector);
  document.getElementById = id => querySelector(`#${id}`);

  function refreshedPage(state = {}) {
    const html = `<main id="content" data-fixture="${++nextPage}"></main>`;
    const content = list(state);
    refreshPages.set(html, {
      querySelector: selector => selector === "#content" ? content : selector === 'meta[name="csrf-token"]' ? { content: "refreshed-csrf" } : null,
    });
    return { status: 200, body: html };
  }
  const context = vm.createContext({
    document,
    location: { href: "https://shopping.example/", origin: "https://shopping.example" },
    CSS: { escape: value => value }, URL, AbortController,
    crypto: { randomUUID: () => `00000000-0000-4000-8000-${String(++nextKey).padStart(12, "0")}` },
    window: { addEventListener(type, listener) { windowListeners.set(type, listener); } },
    setInterval(callback, delay) { intervals.set(delay, callback); },
    DOMParser: class { parseFromString(html) { return refreshPages.get(html) ?? { querySelector: () => null }; } },
    async fetch(url, options) {
      requests.push({ url, ...options });
      assert.ok(responses.length, `unexpected request: ${options.method ?? "GET"} ${url}`);
      const response = responses.shift();
      if (response instanceof Error) throw response;
      const result = await (typeof response === "function" ? response() : response);
      return {
        status: result.status, ok: result.status >= 200 && result.status < 300,
        async json() {
          if (result.jsonError) throw result.jsonError;
          return Object.hasOwn(result, "json") ? result.json : JSON.parse(result.body ?? "null");
        },
        text: async () => result.body ?? "",
      };
    },
  });
  vm.runInContext(script, context, { filename: "web/static/app.js" });

  async function dispatch(type, target) {
    const event = { target };
    await target.dispatch(type, event);
    await document.dispatch(type, event);
    // Mutation listeners start an async save without returning it. Drain the
    // deterministic fetch/body/refresh promise chain after the actual event.
    await setImmediate();
  }
  return {
    document, requests, refreshedPage,
    get status() { return querySelector("#sync-status span").textContent; },
    get retryVisible() { return !querySelector("#retry-write").hidden; },
    get refreshVisible() { return !querySelector("#refresh-view").hidden; },
    get shareURL() { return querySelector("#share-url").value; },
    get dialogOpen() { return querySelector("#share-dialog").open; },
    get pickedUp() { return querySelector("[data-picked-up]"); },
    async click(selector) {
      const target = querySelector(selector);
      assert.ok(target && !target.hidden && !target.disabled, `${selector} must be available`);
      target.focus();
      await dispatch("click", target);
    },
    async createShare() {
      await this.click("#share-start");
      const input = querySelector("[data-share-select]");
      input.checked = true;
      await dispatch("change", input);
      await this.click("#share-create");
    },
    async check(pickedUp) {
      const input = this.pickedUp;
      assert.equal(input.disabled, false, "stale checkbox must not allow a new edit");
      input.focus();
      input.checked = pickedUp;
      await dispatch("change", input);
    },
    revalidate() {
      windowListeners.get("focus")();
      intervals.get(30000)();
    },
    assertUnknownShare() {
      assert.equal(this.retryVisible, true, "unresolved share must retain Retry");
      assert.equal(this.refreshVisible, true);
      assert.equal(this.pickedUp.disabled, true);
      assert.equal(querySelector("#share-start").disabled, true);
      assert.equal(querySelector("[data-share-select]").disabled, true);
      assert.equal(querySelector("#share-create").disabled, true);
      assert.equal(this.dialogOpen, false);
      assert.equal(this.shareURL, "");
      assert.match(this.status, /Not synced/);
      assert.doesNotMatch(this.status, /Saved/);
    },
    assertExactShareRetries(count) {
      const writes = requests.filter(request => request.method === "POST");
      assert.equal(writes.length, count);
      assert.equal(writes[0].url, "/api/v1/lists/owner-list/shares");
      assert.equal(writes[0].body, '{"expected_revision":4,"items":[{"item_id":"owner-item","expected_revision":7,"expected_product_revision":11}]}');
      for (const write of writes) {
        assert.equal(write.url, writes[0].url);
        assert.equal(write.body, writes[0].body);
        assert.equal(write.headers["Idempotency-Key"], writes[0].headers["Idempotency-Key"]);
        assert.equal(write.credentials, "same-origin");
        assert.equal(write.cache, "no-store");
      }
    },
    assertRecovered(code = "AbCdE") {
      assert.equal(this.dialogOpen, true);
      assert.equal(this.shareURL, `https://shopping.example/s/${code}`);
      assert.equal(this.retryVisible, false);
      assert.equal(this.status, "");
      assert.equal(this.pickedUp.disabled, false);
    },
  };
}

const validShare = (code = "AbCdE") => ({ status: 200, json: { record: { code } } });
const networkFailure = () => new TypeError("The connection closed without a response.");

test("valid owner creation opens the URL without refreshing the private list", async () => {
  const responses = [validShare()];
  const page = ownerPage(responses);
  await page.createShare();
  page.assertRecovered();
  page.assertExactShareRetries(1);
  assert.equal(page.requests.length, 1);
  assert.equal(page.pickedUp.checked, false);
});

test("interrupted creation body leaves Retry able to replay the same operation", async () => {
  const responses = [{ status: 200, jsonError: networkFailure() }, validShare()];
  const page = ownerPage(responses);
  await page.createShare();
  assert.equal(page.retryVisible, true);
  await page.click("#retry-write");
  page.assertExactShareRetries(2);
  page.assertRecovered();
});

for (const [name, response] of [
  ["interrupted body", { status: 200, jsonError: networkFailure() }],
  ["unparseable JSON", { status: 200, body: '{"record":' }],
  ["null result", { status: 200, json: null }],
  ["missing record", { status: 200, json: {} }],
  ["null record", { status: 200, json: { record: null } }],
  ["missing code", { status: 200, json: { record: {} } }],
  ...[null, 12345, "", "AbCd", "AbCdEf", "AbC1E", "AbCdÉ", "AbCdE\n", ["AbCdE"]].map(code => [JSON.stringify(code), { status: 200, json: { record: { code } } }]),
]) {
  test(`invalid owner success (${name}) keeps the exact creation retry until its URL is recovered`, async () => {
    const responses = [response, validShare()];
    const page = ownerPage(responses);
    await page.createShare();
    page.assertUnknownShare();
    page.revalidate();
    assert.equal(page.requests.length, 1, "background refresh cannot settle an unresolved URL");
    await page.click("#retry-write");
    page.assertRecovered();
    page.assertExactShareRetries(2);
    assert.equal(responses.length, 0);
  });
}

for (const status of [400, 401, 403, 409, 429]) {
  test(`unknown creation survives retry ${status} and failed private refresh`, async () => {
    const responses = [networkFailure(), { status }, { status: 503 }, validShare()];
    const page = ownerPage(responses);
    await page.createShare();
    page.assertUnknownShare();
    await page.click("#retry-write");
    page.assertUnknownShare();
    assert.equal(page.requests[2].url, "https://shopping.example/");
    await page.click("#retry-write");
    page.assertRecovered();
    page.assertExactShareRetries(3);
  });
}

for (const [name, failure] of [
  ["throttled", { status: 429 }], ["network failure", networkFailure()], ["malformed page", { status: 200, body: "<html></html>" }],
]) {
  test(`unknown creation survives rejected retry and ${name} private refresh`, async () => {
    const responses = [{ status: 200, body: '{"record":' }, { status: 429 }, failure, { status: 503 }, validShare()];
    const page = ownerPage(responses);
    await page.createShare();
    await page.click("#retry-write");
    page.assertUnknownShare();
    await page.click("#retry-write");
    page.assertUnknownShare();
    await page.click("#retry-write");
    page.assertRecovered();
    page.assertExactShareRetries(4);
  });
}

for (const [name, failure] of [
  ["429", { status: 429 }], ["503", { status: 503 }],
  ["network failure", networkFailure()], ["malformed page", { status: 200, body: "<html></html>" }],
]) {
  test(`failed explicit private refresh (${name}) keeps the original share creation`, async () => {
    const responses = [networkFailure(), failure, validShare()];
    const page = ownerPage(responses);
    await page.createShare();
    await page.click("#refresh-view");
    page.assertUnknownShare();
    await page.click("#retry-write");
    page.assertRecovered();
    page.assertExactShareRetries(2);
  });
}

for (const refreshAction of ["explicit", "rejected retry"]) {
  test(`successful ${refreshAction} private refresh cannot settle an unresolved share URL`, async () => {
    const responses = [networkFailure()];
    const page = ownerPage(responses);
    if (refreshAction === "rejected retry") responses.push({ status: 403 });
    responses.push(() => page.refreshedPage({ listID: "new-active-list", listRevision: 20, itemRevision: 12, productRevision: 15 }), validShare());
    await page.createShare();
    await page.click(refreshAction === "explicit" ? "#refresh-view" : "#retry-write");
    page.assertUnknownShare();
    await page.click("#retry-write");
    page.assertRecovered();
    page.assertExactShareRetries(refreshAction === "explicit" ? 2 : 3);
    assert.equal(page.requests.at(-1).headers["X-CSRF-Token"], "refreshed-csrf");
  });
}

test("Retry aborts an in-flight private refresh before recovering the original URL", async () => {
  let finishRefresh;
  const refreshing = new Promise(resolve => { finishRefresh = resolve; });
  const responses = [networkFailure(), () => refreshing, validShare()];
  const page = ownerPage(responses);
  await page.createShare();
  const refreshClick = page.click("#refresh-view");
  await setImmediate();
  await page.click("#retry-write");
  page.assertRecovered();
  assert.equal(page.requests[1].signal.aborted, true);
  finishRefresh(page.refreshedPage({ listRevision: 30 }));
  await refreshClick;
  page.assertRecovered();
  assert.equal(page.document.querySelector("#content").dataset.listRevision, "4", "aborted refresh must not install stale content");
  page.assertExactShareRetries(2);
});

test("a first definitive owner conflict settles without Saved and allows a new current selection", async () => {
  const responses = [{ status: 409 }];
  const page = ownerPage(responses);
  responses.push(() => page.refreshedPage({ listRevision: 8, itemRevision: 9, productRevision: 12 }), validShare("FgHiJ"));
  await page.createShare();
  assert.equal(page.retryVisible, false);
  assert.equal(page.pickedUp.disabled, false);
  assert.match(page.status, /changed\. Refreshed/);
  assert.doesNotMatch(page.status, /Saved/);
  await page.createShare();
  page.assertRecovered("FgHiJ");
  const writes = page.requests.filter(request => request.method === "POST");
  assert.notEqual(writes[0].headers["Idempotency-Key"], writes[1].headers["Idempotency-Key"]);
  assert.equal(writes[1].body, '{"expected_revision":8,"items":[{"item_id":"owner-item","expected_revision":9,"expected_product_revision":12}]}');
});

test("ordinary owner list save refreshes current state and its next edit uses a fresh key/revisions", async () => {
  const responses = [{ status: 200, json: {} }];
  const page = ownerPage(responses);
  responses.push(() => page.refreshedPage({ listRevision: 5, itemRevision: 8, pickedUp: true }), { status: 200, json: {} }, () => page.refreshedPage({ listRevision: 6, itemRevision: 9 }));
  await page.check(true);
  assert.equal(page.status, "Saved.");
  assert.equal(page.pickedUp.checked, true);
  assert.equal(page.pickedUp.disabled, false);
  await page.check(false);
  const writes = page.requests.filter(request => request.method === "PATCH");
  assert.equal(writes.length, 2);
  assert.equal(writes[0].body, '{"picked_up":true,"expected_revision":7,"expected_list_revision":4}');
  assert.equal(writes[1].body, '{"picked_up":false,"expected_revision":8,"expected_list_revision":5}');
  assert.notEqual(writes[0].headers["Idempotency-Key"], writes[1].headers["Idempotency-Key"]);
  assert.equal(page.status, "Saved.");
});

test("ordinary unknown list write settles through authoritative private refresh", async () => {
  const responses = [networkFailure()];
  const page = ownerPage(responses);
  responses.push(() => page.refreshedPage({ listRevision: 5, itemRevision: 8, pickedUp: true }));
  await page.check(true);
  assert.equal(page.retryVisible, true);
  assert.equal(page.pickedUp.disabled, true);
  await page.click("#refresh-view");
  assert.equal(page.retryVisible, false);
  assert.equal(page.pickedUp.disabled, false);
  assert.equal(page.pickedUp.checked, true);
  assert.match(page.status, /^Refreshed\./);
  assert.equal(page.requests.filter(request => request.method === "PATCH").length, 1);
});

for (const success of [true, false]) {
  test(`ordinary definitive list conflict with ${success ? "successful" : "failed"} refresh never claims Saved`, async () => {
    const responses = [{ status: 409 }];
    const page = ownerPage(responses);
    responses.push(success ? () => page.refreshedPage({ listRevision: 8, itemRevision: 9 }) : { status: 503 });
    await page.check(true);
    assert.equal(page.retryVisible, false);
    assert.equal(page.pickedUp.disabled, false);
    assert.doesNotMatch(page.status, /Saved/);
    assert.match(page.status, success ? /changed\. Refreshed/ : /Refresh failed/);
    assert.equal(page.refreshVisible, !success);
  });
}

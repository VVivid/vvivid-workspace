import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { setImmediate } from "node:timers/promises";
import test from "node:test";
import vm from "node:vm";

const script = await readFile(new URL("../static/share.js", import.meta.url), "utf8");
const initialTime = 1_000_000;

// Only the DOM operations used by share.js are modeled. Tests dispatch real
// event listeners and inspect requests/UI; the production state stays private.
function guestPage(responses) {
  let now = initialTime;
  let nextKey = 0;
  let nextPage = 0;
  const requests = [];
  const listeners = new Map();
  const intervals = new Map();
  const refreshPages = new Map();
  const document = {
    hidden: false,
    activeElement: null,
    content: null,
    csrf: { content: "initial-capability", remove() { document.csrf = null; } },
    querySelector(selector) {
      if (selector === "#shared-content") return this.content;
      if (selector === 'meta[name="share-csrf"]') return this.csrf;
      return null;
    },
    addEventListener(type, listener) { listeners.set(type, listener); },
    createElement() {
      return { textContent: "", append(...children) { this.children = children; } };
    },
  };

  function checklist({ pickedUp = false, revision = 7, expiresAt = initialTime + 21_600_000 } = {}) {
    const statusText = { textContent: "" };
    const controls = new Map([
      ["#share-status", { hidden: true, querySelector: () => statusText }],
      ["#share-retry", button("#share-retry")],
      ["#share-refresh", button("#share-refresh")],
      ["#share-time", { textContent: "" }],
      ["#shared-progress", { textContent: "" }],
    ]);
    const row = { dataset: { sharedPosition: "3", revision: String(revision) } };
    let inputs = [{
      checked: pickedUp,
      disabled: false,
      matches: selector => selector === "[data-shared-check]",
      closest: selector => selector === "[data-shared-position]" ? row : null,
      focus() { document.activeElement = this; },
    }];
    return {
      dataset: { expiresAt: String(expiresAt) },
      querySelector(selector) {
        if (selector === '[data-shared-position="3"] input') return inputs[0];
        return controls.get(selector) ?? null;
      },
      querySelectorAll: selector => selector === "[data-shared-check]" ? inputs : [],
      replaceWith(next) { document.content = next; },
      replaceChildren(...children) {
        this.children = children;
        inputs = [];
        controls.clear();
      },
    };
  }
  function button(selector) {
    return { hidden: true, closest: target => target === selector ? document.content.querySelector(selector) : null };
  }
  document.content = checklist();

  // Refreshed HTML maps to a deterministic DOM fixture. This does not test
  // HTML parsing or layout, and it does not recreate the retry algorithm.
  function refreshedPage(state = {}) {
    const html = `<main id="shared-content" data-fixture="${++nextPage}"></main>`;
    refreshPages.set(html, {
      querySelector(selector) {
        if (selector === "#shared-content") return checklist(state);
        if (selector === 'meta[name="share-csrf"]') return { content: "refreshed-capability" };
        return null;
      },
    });
    return { status: 200, body: html };
  }
  const context = vm.createContext({
    document,
    location: { pathname: "/s/AbCdE/" },
    Date: { now: () => now },
    CSS: { escape: value => value },
    crypto: { randomUUID: () => `00000000-0000-4000-8000-${String(++nextKey).padStart(12, "0")}` },
    window: { addEventListener() {} },
    setInterval(callback, delay) { intervals.set(delay, callback); },
    DOMParser: class {
      parseFromString(html) {
        return refreshPages.get(html) ?? { querySelector: () => null };
      }
    },
    async fetch(url, options) {
      requests.push({ url, ...options });
      assert.ok(responses.length, `unexpected request: ${options.method ?? "GET"} ${url}`);
      const response = responses.shift();
      if (response instanceof Error) throw response;
      const result = typeof response === "function" ? response() : response;
      return {
        status: result.status,
        ok: result.status >= 200 && result.status < 300,
        text: async () => result.body ?? "",
      };
    },
  });
  vm.runInContext(script, context, { filename: "web/static/share.js" });

  async function dispatch(type, target) {
    await listeners.get(type)({ target });
    // Change/Retry listeners intentionally start, but do not return, the save.
    // One event-loop turn drains the deterministic fetch/text promise chain.
    await setImmediate();
  }
  return {
    document,
    requests,
    refreshedPage,
    get inputs() { return document.content.querySelectorAll("[data-shared-check]"); },
    get status() { return document.content.querySelector("#share-status")?.querySelector("span").textContent ?? ""; },
    get retryVisible() { return document.content.querySelector("#share-retry")?.hidden === false; },
    get refreshVisible() { return document.content.querySelector("#share-refresh")?.hidden === false; },
    async check(pickedUp) {
      const input = this.inputs[0];
      assert.equal(input.disabled, false, "stale checkbox must not allow a new edit");
      input.checked = pickedUp;
      await dispatch("change", input);
    },
    async click(selector) {
      const target = document.content.querySelector(selector);
      assert.ok(target && !target.hidden, `${selector} must be visible`);
      await dispatch("click", target);
    },
    expire() {
      now = initialTime + 21_600_000;
      intervals.get(1000)();
    },
    assertUnsynced() {
      assert.equal(this.retryVisible, true, "unresolved write must retain Retry");
      assert.equal(this.refreshVisible, true);
      assert.equal(this.inputs[0].disabled, true);
      assert.equal(this.inputs[0].checked, false);
      assert.doesNotMatch(this.status, /Saved|Refreshed/);
    },
    assertExactRetries(count) {
      const writes = requests.filter(request => request.method === "PATCH");
      assert.equal(writes.length, count);
      assert.equal(writes[0].url, "/s/AbCdE/items/3");
      assert.equal(writes[0].body, '{"picked_up":true,"expected_revision":7}');
      for (const write of writes) {
        assert.equal(write.url, writes[0].url);
        assert.equal(write.body, writes[0].body);
        assert.equal(write.headers["Idempotency-Key"], writes[0].headers["Idempotency-Key"]);
        assert.equal(write.credentials, "omit");
        assert.equal(write.cache, "no-store");
        assert.equal(write.headers["X-Share-CSRF"], "initial-capability");
      }
    },
    assertUnavailable() {
      assert.equal(this.inputs.length, 0);
      assert.equal(document.csrf, null);
      assert.equal(document.content.dataset.expiresAt, undefined);
      assert.equal(document.content.children[0].children[0].textContent, "This link is unavailable");
    },
  };
}

const networkFailure = () => new TypeError("The connection closed without a response.");

test("unknown write survives throttled retry/refresh and repeated failures with its exact request", async () => {
  const responses = [networkFailure(), { status: 429 }, { status: 429 }, networkFailure(), { status: 503 }, { status: 204 }];
  const page = guestPage(responses);
  responses.push(() => page.refreshedPage({ pickedUp: true, revision: 8 }));
  await page.check(true);
  page.assertUnsynced();
  await page.click("#share-retry");
  page.assertUnsynced();
  assert.equal(page.requests[2].url, "/s/AbCdE");
  await page.click("#share-retry");
  page.assertUnsynced();
  await page.click("#share-retry");
  page.assertUnsynced();
  await page.click("#share-retry");
  page.assertExactRetries(5);
  assert.equal(page.status, "Saved.");
  assert.equal(page.inputs[0].checked, true);
  assert.equal(page.inputs[0].disabled, false);
  assert.equal(page.retryVisible, false);
  assert.equal(responses.length, 0);
});

for (const [name, failure] of [
  ["429", { status: 429 }],
  ["503", { status: 503 }],
  ["network failure", networkFailure()],
  ["malformed page", { status: 200, body: "<html></html>" }],
]) {
  test(`failed explicit refresh (${name}) preserves the unknown write`, async () => {
    const responses = [networkFailure(), failure, { status: 204 }];
    const page = guestPage(responses);
    responses.push(() => page.refreshedPage({ pickedUp: true, revision: 8 }));
    await page.check(true);
    await page.click("#share-refresh");
    page.assertUnsynced();
    await page.click("#share-retry");
    page.assertExactRetries(2);
    assert.equal(page.status, "Saved.");
    assert.equal(page.inputs[0].disabled, false);
  });
}

test("successful explicit refresh settles the unknown write and the next edit uses the new revision/key", async () => {
  const responses = [networkFailure()];
  const page = guestPage(responses);
  responses.push(() => page.refreshedPage({ pickedUp: true, revision: 8 }), { status: 204 }, () => page.refreshedPage({ revision: 9 }));
  await page.check(true);
  await page.click("#share-refresh");
  assert.equal(page.status, "Refreshed. Review the current checklist.");
  assert.equal(page.retryVisible, false);
  assert.equal(page.inputs[0].disabled, false);
  await page.check(false);
  const writes = page.requests.filter(request => request.method === "PATCH");
  assert.equal(writes.length, 2);
  assert.notEqual(writes[0].headers["Idempotency-Key"], writes[1].headers["Idempotency-Key"]);
  assert.equal(writes[1].headers["X-Share-CSRF"], "refreshed-capability");
  assert.equal(writes[1].body, '{"picked_up":false,"expected_revision":8}');
  assert.equal(page.status, "Saved.");
});

test("successful save displays authoritative state", async () => {
  const responses = [{ status: 204 }];
  const page = guestPage(responses);
  responses.push(() => page.refreshedPage({ pickedUp: true, revision: 8 }));
  await page.check(true);
  assert.equal(page.status, "Saved.");
  assert.equal(page.inputs[0].checked, true);
  assert.equal(page.inputs[0].disabled, false);
  assert.equal(page.retryVisible, false);
  page.assertExactRetries(1);
});

test("settled save with failed refresh keeps stale controls locked until a successful refresh", async () => {
  const responses = [{ status: 204 }, { status: 503 }];
  const page = guestPage(responses);
  responses.push(() => page.refreshedPage({ pickedUp: true, revision: 8 }));
  await page.check(true);
  assert.match(page.status, /^Saved\. Refresh failed/);
  assert.equal(page.inputs[0].disabled, true);
  assert.equal(page.retryVisible, false);
  await page.click("#share-refresh");
  assert.equal(page.inputs[0].disabled, false);
  assert.equal(page.inputs[0].checked, true);
  page.assertExactRetries(1);
});

for (const status of [403, 409, 429]) {
  test(`${status} rejection followed by failed refresh keeps Retry until authoritative refresh`, async () => {
    const responses = [{ status }, { status: 503 }];
    const page = guestPage(responses);
    responses.push(() => page.refreshedPage({ revision: 8 }));
    await page.check(true);
    page.assertUnsynced();
    await page.click("#share-refresh");
    assert.equal(page.retryVisible, false);
    assert.equal(page.inputs[0].disabled, false);
    assert.match(page.status, /^Refreshed\./);
    page.assertExactRetries(1);
  });
}

test("conflict refresh succeeds without claiming a save", async () => {
  const responses = [{ status: 409 }];
  const page = guestPage(responses);
  responses.push(() => page.refreshedPage({ revision: 8 }));
  await page.check(true);
  assert.equal(page.status, "The shared checklist changed. Refreshed; review it and try again.");
  assert.equal(page.retryVisible, false);
  assert.equal(page.inputs[0].disabled, false);
  assert.equal(page.inputs[0].checked, false);
});

for (const action of ["write", "refresh", "countdown"]) {
  test(`expiry through ${action} removes item data and the capability`, async () => {
    const page = guestPage(action === "write" ? [{ status: 404 }] : [networkFailure(), { status: 404 }]);
    await page.check(true);
    if (action === "refresh") await page.click("#share-refresh");
    if (action === "countdown") page.expire();
    page.assertUnavailable();
  });
}

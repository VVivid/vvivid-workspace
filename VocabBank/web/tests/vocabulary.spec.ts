import { APIRequestContext, Locator, Page, expect, test } from '@playwright/test';
import { Capture, Sense, Session, Word, WordPage } from '../lib/api';

test.use({ launchOptions: { ignoreDefaultArgs: ['--hide-scrollbars'] } });
const origin = 'https://127.0.0.1:8099';
const learning: Sense[] = [
  {
    part_of_speech: 'adjective',
    definition: 'Able to express ideas clearly.',
    when_to_use: 'Describe a clear speaker or explanation.',
    examples: [
      { context: 'At a meeting', sentence: 'She gave an articulate explanation of the plan.' },
      {
        context: 'Describing a speaker',
        sentence: 'The articulate presenter answered every question.',
      },
    ],
    common_phrases: ['an articulate speaker', 'an articulate explanation'],
    usage_tip: 'Use this for clear expression.',
  },
  {
    part_of_speech: 'verb',
    definition: 'To express an idea clearly in words.',
    when_to_use: 'Describe putting thoughts or feelings into words.',
    examples: [
      {
        context: 'Explaining a problem',
        sentence: 'Can you articulate the problem in one sentence?',
      },
      { context: 'Sharing a concern', sentence: 'He needed time to articulate his concern.' },
    ],
    common_phrases: ['articulate an idea'],
  },
];
async function signIn(page: Page) {
  await page.goto('/');
  await page.getByRole('link', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Sign out', exact: true })).toBeVisible();
}
async function agent(request: APIRequestContext) {
  const grant = await request.get('/__test__/agent-token');
  expect(grant.status()).toBe(200);
  const { token } = await grant.json();
  const headers = {
    Authorization: `Bearer ${token}`,
    Accept: 'application/json, text/event-stream',
    'MCP-Protocol-Version': '2025-11-25',
  };
  const initialized = await request.post('/mcp', {
    headers,
    data: {
      jsonrpc: '2.0',
      id: 1,
      method: 'initialize',
      params: {
        protocolVersion: '2025-11-25',
        capabilities: {},
        clientInfo: { name: 'vocabbank-browser-test', version: 'test' },
      },
    },
  });
  expect(initialized.status()).toBe(200);
  return async function call<T>(name: string, args: unknown): Promise<T> {
    const response = await request.post('/mcp', {
      headers,
      data: { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name, arguments: args } },
    });
    expect(response.status()).toBe(200);
    const result = await response.json();
    expect(result.error).toBeUndefined();
    expect(result.result.isError).toBeFalsy();
    return result.result.structuredContent as T;
  };
}
async function focusRefresh(page: Page) {
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
}
async function fillCapture(form: Locator, capture: Capture) {
  await form.getByLabel('Word or expression').fill(capture.term);
  await form.getByLabel('Sentence (optional)').fill(capture.original_sentence);
  await form.getByLabel('Source URL (optional)').fill(capture.source_url);
  await form.getByLabel('Personal note (optional)').fill(capture.personal_note);
}
async function expectCapture(form: Locator, capture: Capture) {
  await expect(form.getByLabel('Word or expression')).toHaveValue(capture.term);
  await expect(form.getByLabel('Sentence (optional)')).toHaveValue(capture.original_sentence);
  await expect(form.getByLabel('Source URL (optional)')).toHaveValue(capture.source_url);
  await expect(form.getByLabel('Personal note (optional)')).toHaveValue(capture.personal_note);
}
async function session(page: Page): Promise<Session> {
  const response = await page.request.get('/api/v1/session');
  expect(response.status()).toBe(200);
  return response.json();
}
async function reauthenticate(page: Page, otherOwner = false) {
  if (otherOwner)
    await page.context().route(
      '**/auth/login?reauthenticate=1',
      async (route) => {
        const login = await route.fetch({ maxRedirects: 0 });
        expect(login.status()).toBe(302);
        const authorize = new URL(login.headers().location);
        authorize.searchParams.set('login_hint', 'other-test-subject');
        await route.fulfill({
          response: login,
          headers: { ...login.headers(), location: authorize.toString() },
        });
      },
      { times: 1 },
    );
  const popupPromise = page.waitForEvent('popup');
  await page.getByRole('link', { name: 'Sign in in a new tab', exact: true }).click();
  const popup = await popupPromise;
  await expect(popup.getByRole('button', { name: 'Sign out', exact: true })).toBeVisible();
  await popup.close();
  await page.bringToFront();
  await focusRefresh(page);
}

test('authenticated production export saves captures, learns through MCP, resolves conflicts and pages the approved grid', async ({
  page,
}) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('console', (msg) => {
    if (msg.type() === 'error' && msg.text().includes('Content Security Policy'))
      errors.push(msg.text());
  });
  await signIn(page);
  await expect(page.getByText('No words yet. Add your first word or expression.')).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await page.getByRole('button', { name: 'Dark', exact: true }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.getByRole('button', { name: 'Light', exact: true }).click();

  await page.route(
    '**/api/v1/words?*',
    (route) =>
      route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Synthetic listing outage.' }),
      }),
    { times: 1 },
  );
  await focusRefresh(page);
  await expect(page.getByText('Synthetic listing outage.')).toBeVisible();
  await page.getByRole('button', { name: 'Retry words', exact: true }).click();
  await expect(page.getByText('Synthetic listing outage.')).toHaveCount(0);

  await page.getByRole('button', { name: '+ Add word', exact: true }).click();
  const form = page.getByRole('dialog', { name: 'Add a word', exact: true });
  await form.getByLabel('Word or expression').fill('  articulate  ');
  await form
    .getByLabel('Sentence (optional)')
    .fill('My original sentence about articulate speech.');
  await form.getByLabel('Source URL (optional)').fill('https://example.test/source');
  await form.getByLabel('Personal note (optional)').fill('My note stays intact.');
  // Commit once but lose its response. The UI must reuse the exact intent key.
  await page.route(
    '**/api/v1/words',
    async (route) => {
      await route.fetch();
      await route.abort('failed');
    },
    { times: 1 },
  );
  await form.getByRole('button', { name: 'Save word', exact: true }).click();
  await expect(form.getByRole('alert')).toContainText('Could not connect');
  await expect(form.getByLabel('Personal note (optional)')).toHaveValue('My note stays intact.');
  await form.getByRole('button', { name: 'Retry save', exact: true }).click();
  const detail = page.getByRole('dialog', { name: 'articulate', exact: true });
  await expect(detail.getByText('Saved and ready to learn')).toBeVisible();
  await expect(detail.getByText('My note stays intact.')).toBeVisible();
  const mcp = await agent(page.request);
  const draftPage = await mcp<WordPage>('list_words', { status: 'needs_details' });
  expect(draftPage.total).toBe(1);
  const draft = await mcp<Word>('get_word', { id: draftPage.items[0].id });
  await mcp<Word>('complete_word', {
    id: draft.id,
    expected_revision: draft.revision,
    idempotency_key: 'browser-learning-001',
    senses: learning,
  });
  await focusRefresh(page);
  await expect(detail.getByText('Able to express ideas clearly.', { exact: true })).toBeVisible();
  await expect(detail.getByText('At a meeting', { exact: true })).toBeVisible();
  await expect(detail.locator('.vg-context-example')).toHaveCount(2);
  await expect(detail.getByText('My original sentence about articulate speech.')).toBeVisible();
  await detail.getByRole('tab', { name: 'Verb', exact: true }).click();
  await expect(detail.getByText('To express an idea clearly in words.')).toBeVisible();
  await detail.getByRole('tab', { name: 'Verb', exact: true }).press('ArrowLeft');
  await expect(detail.getByRole('tab', { name: 'Adjective', exact: true })).toBeFocused();
  await detail.getByText('Usage tip', { exact: true }).click();
  await expect(detail.getByText('Use this for clear expression.')).toBeVisible();
  await page.screenshot({ path: '../.cache/screenshots/learner-desktop.png', fullPage: true });

  await detail.getByRole('button', { name: 'Edit word', exact: true }).click();
  const editor = page.getByRole('dialog', { name: 'Edit word', exact: true });
  await editor.getByLabel('Personal note (optional)').fill('Unsaved user input.');
  const current = await mcp<Word>('get_word', { id: draft.id });
  await mcp<Word>('update_word', {
    id: current.id,
    expected_revision: current.revision,
    idempotency_key: 'browser-concurrent-001',
    term: current.term,
    original_sentence: current.original_sentence,
    source_url: current.source_url,
    personal_note: 'Another saved note.',
  });
  await focusRefresh(page);
  await expect(editor.getByLabel('Personal note (optional)')).toHaveValue('Unsaved user input.');
  await editor.getByRole('button', { name: 'Save changes', exact: true }).click();
  await expect(editor.getByRole('alert')).toContainText('changed elsewhere');
  await expect(editor.getByLabel('Personal note (optional)')).toHaveValue('Unsaved user input.');
  await editor.getByRole('button', { name: 'Review latest version', exact: true }).click();
  await expect(editor.getByRole('region', { name: 'Latest saved fields' })).toContainText(
    'Another saved note.',
  );
  await editor.getByRole('button', { name: 'Use latest fields', exact: true }).click();
  await expect(editor.getByLabel('Personal note (optional)')).toHaveValue('Another saved note.');
  await editor.getByLabel('Personal note (optional)').fill('User reconciled this note.');
  await editor.getByRole('button', { name: 'Save changes', exact: true }).click();
  await expect(detail.getByText('User reconciled this note.')).toBeVisible();
  await detail.getByRole('button', { name: 'Close', exact: true }).click();

  // Bounded separate MCP calls exercise real server totals and offsets.
  for (let i = 0; i < 105; i++)
    await mcp<Word>('add_word', {
      term: `Synthetic grid ${String(i).padStart(3, '0')}`,
      idempotency_key: `browser-grid-${String(i).padStart(3, '0')}`,
    });
  await focusRefresh(page);
  await expect(page.locator('.vg-total')).toHaveText('106 words');
  await expect(page.locator('.vg-grid .vg-word')).toHaveCount(30);
  await expect(page.getByText('1–30 of 106', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Next words', exact: true }).click();
  await expect(page.getByText('31–60 of 106', { exact: true })).toBeVisible();
  await page.getByRole('combobox', { name: 'Columns', exact: true }).selectOption('4');
  await expect
    .poll(() =>
      page
        .locator('.vg-grid')
        .evaluate((node) => getComputedStyle(node).gridTemplateColumns.split(' ').length),
    )
    .toBe(4);
  await page.getByRole('combobox', { name: 'Words per page', exact: true }).selectOption('100');
  await expect(page.locator('.vg-grid .vg-word')).toHaveCount(100);
  await expect(page.getByText('1–100 of 106', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Next words', exact: true }).click();
  await expect(page.locator('.vg-grid .vg-word')).toHaveCount(6);
  await expect(page.getByText('101–106 of 106', { exact: true })).toBeVisible();
  await page.getByRole('searchbox', { name: 'Search words' }).fill('articulate');
  await expect(page.locator('.vg-grid .vg-word')).toHaveCount(1);
  const wordButton = page.getByRole('button', { name: 'articulate', exact: true });
  const before = await wordButton.evaluate((node) => ({
    color: getComputedStyle(node).color,
    background: getComputedStyle(node).backgroundColor,
  }));
  await wordButton.hover();
  expect(
    await wordButton.evaluate((node) => ({
      color: getComputedStyle(node).color,
      background: getComputedStyle(node).backgroundColor,
    })),
  ).toEqual(before);
  const storage = await page.evaluate(() => ({
    local: Object.keys(localStorage),
    session: Object.keys(sessionStorage),
  }));
  expect(storage).toEqual({ local: ['vocabbank-display'], session: [] });
  await page.route('**/auth/logout', (route) => route.abort('failed'), { times: 1 });
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Retry sign out', exact: true })).toBeVisible();
  await expect(wordButton).toBeVisible();
  await page.getByRole('button', { name: 'Retry sign out', exact: true }).click();
  await expect(page.getByRole('link', { name: 'Sign in', exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test('mobile grid and learner popup fit the viewport and preserve display settings', async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  const mcp = await agent(page.request);
  const matches = await mcp<WordPage>('list_words', { query: 'articulate' });
  let word = matches.items.find((item) => item.term === 'articulate');
  if (!word)
    word = await mcp<Word>('add_word', {
      term: 'articulate',
      idempotency_key: 'mobile-capture-001',
    });
  if (word.status === 'needs_details')
    await mcp<Word>('complete_word', {
      id: word.id,
      expected_revision: word.revision,
      idempotency_key: 'mobile-learning-001',
      senses: learning,
    });
  await page.getByRole('combobox', { name: 'Columns', exact: true }).selectOption('4');
  await page.getByRole('combobox', { name: 'Words per page', exact: true }).selectOption('100');
  await page.getByRole('button', { name: 'Dark', exact: true }).click();
  await page.reload();
  await expect(page.getByRole('combobox', { name: 'Columns', exact: true })).toHaveValue('4');
  await expect(page.getByRole('combobox', { name: 'Words per page', exact: true })).toHaveValue(
    '100',
  );
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.getByRole('searchbox', { name: 'Search words' }).fill('articulate');
  await page.getByRole('button', { name: 'articulate', exact: true }).click();
  const detail = page.getByRole('dialog', { name: 'articulate', exact: true });
  await expect(detail.getByText('Able to express ideas clearly.', { exact: true })).toBeVisible();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
  ).toBeTruthy();
  expect(await detail.evaluate((node) => node.scrollWidth <= node.clientWidth)).toBeTruthy();
  await expect(detail.locator('.vg-context-examples')).toHaveCSS('grid-template-columns', /\d/);
  const bounds = await detail.boundingBox();
  expect(bounds!.x).toBeGreaterThanOrEqual(0);
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390);
  await page.screenshot({ path: '../.cache/screenshots/learner-mobile.png', fullPage: true });
  await detail.getByRole('button', { name: 'Close word details', exact: true }).click();
  await expect(detail).toHaveCount(0);
});

test('background list 401 retains an add dialog and its committed-but-unacknowledged retry through same-owner sign-in', async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  const before = await session(page);
  await page.getByRole('button', { name: '+ Add word', exact: true }).click();
  const form = page.getByRole('dialog', { name: 'Add a word', exact: true });
  const capture: Capture = {
    term: 'Synthetic add recovery',
    original_sentence: 'A synthetic original sentence survives expired authentication.',
    source_url: 'https://example.test/add-recovery',
    personal_note: 'Keep this unsaved synthetic note and the retry intent.',
  };
  await fillCapture(form, capture);
  await form.evaluate((node) => node.setAttribute('data-retained-dialog', 'add'));
  const writes: { body: unknown; csrf: string | undefined }[] = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/words')
      writes.push({ body: request.postDataJSON(), csrf: request.headers()['x-csrf-token'] });
  });
  await page.route(
    '**/api/v1/words',
    async (route) => {
      const committed = await route.fetch();
      expect(committed.status()).toBe(200);
      await route.abort('failed');
    },
    { times: 1 },
  );
  await form.getByRole('button', { name: 'Save word', exact: true }).click();
  await expect(form.getByRole('alert')).toContainText('Could not connect');
  expect(writes).toHaveLength(1);
  await page.context().clearCookies({ name: '__Host-vocabbank-session' });
  const background = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === '/api/v1/words' &&
      response.request().method() === 'GET' &&
      response.status() === 401,
  );
  await focusRefresh(page);
  await background;
  const recovery = form.getByRole('alert').filter({ hasText: 'Sign in again to save' });
  await expect(recovery).toBeVisible();
  await expect(form).toHaveAttribute('data-retained-dialog', 'add');
  await expectCapture(form, capture);
  expect(await form.evaluate((node) => node.scrollWidth <= node.clientWidth)).toBeTruthy();
  await recovery.scrollIntoViewIfNeeded();
  await page.screenshot({ path: '../.cache/screenshots/auth-recovery-mobile.png', fullPage: true });
  await expect(form.getByRole('button', { name: 'Retry save', exact: true })).toBeDisabled();
  await form.locator('form').evaluate((node) => (node as HTMLFormElement).requestSubmit());
  expect(writes).toHaveLength(1);
  await page.route(
    '**/api/v1/session',
    (route) =>
      route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'Synthetic sign-in outage.' }),
      }),
    { times: 1 },
  );
  await recovery.getByRole('button', { name: 'Check sign-in', exact: true }).click();
  await expect(recovery).toContainText('Synthetic sign-in outage.');
  await expectCapture(form, capture);
  await reauthenticate(page);
  await expect(form.getByRole('button', { name: 'Retry save', exact: true })).toBeEnabled();
  await expect(form).toHaveAttribute('data-retained-dialog', 'add');
  await expectCapture(form, capture);
  const after = await session(page);
  expect(after.owner_id).toBe(before.owner_id);
  expect(after.csrf_token).not.toBe(before.csrf_token);
  await form.getByRole('button', { name: 'Retry save', exact: true }).click();
  await expect(page.getByRole('dialog', { name: capture.term, exact: true })).toBeVisible();
  expect(writes).toHaveLength(2);
  expect(writes[1].body).toEqual(writes[0].body);
  expect(writes[0].csrf).toBe(before.csrf_token);
  expect(writes[1].csrf).toBe(after.csrf_token);
  const saved = await page.request.get(`/api/v1/words?query=${encodeURIComponent(capture.term)}`);
  const result: WordPage = await saved.json();
  expect(result.total).toBe(1);
  expect(result.items[0]).toMatchObject({ ...capture, revision: 1 });
  expect(
    await page.evaluate(() => ({
      local: Object.keys(localStorage),
      session: Object.keys(sessionStorage),
    })),
  ).toEqual({ local: ['vocabbank-display'], session: [] });
});

test('edit capture survives background/detail and save 401s, blocks another owner, and resumes only for its original owner', async ({
  page,
}) => {
  await signIn(page);
  const originalSession = await session(page);
  const mcp = await agent(page.request);
  const word = await mcp<Word>('add_word', {
    term: 'Synthetic edit recovery',
    idempotency_key: 'browser-edit-recovery-001',
  });
  await page.getByRole('searchbox', { name: 'Search words' }).fill(word.term);
  await page.getByRole('button', { name: word.term, exact: true }).click();
  const detail = page.getByRole('dialog', { name: word.term, exact: true });
  await expect(detail.getByText('Saved and ready to learn')).toBeVisible();
  // A detail refresh may already be in flight when Edit closes its learner popup.
  // Its late 401 must not replace or close the editor that now owns this dialog.
  let releaseDetail!: () => void;
  let detailRequested!: () => void;
  const pendingDetail = new Promise<void>((resolve) => {
    detailRequested = resolve;
  });
  const release = new Promise<void>((resolve) => {
    releaseDetail = resolve;
  });
  await page.route(
    `**/api/v1/words/${word.id}`,
    async (route) => {
      detailRequested();
      await release;
      await route.fulfill({ status: 401, body: 'Sign in to continue.' });
    },
    { times: 1 },
  );
  await focusRefresh(page);
  await pendingDetail;
  await detail.getByRole('button', { name: 'Edit word', exact: true }).click();
  const form = page.getByRole('dialog', { name: 'Edit word', exact: true });
  const capture: Capture = {
    term: 'Synthetic edited recovery',
    original_sentence: 'The original synthetic edit sentence stays in the form.',
    source_url: 'https://example.test/edit-recovery',
    personal_note: 'The synthetic edit note survives switching accounts.',
  };
  await fillCapture(form, capture);
  await form.evaluate((node) => node.setAttribute('data-retained-dialog', 'edit'));
  const staleDetail = page.waitForResponse(
    (response) => response.url().endsWith(`/api/v1/words/${word.id}`) && response.status() === 401,
  );
  releaseDetail();
  await staleDetail;
  await expectCapture(form, capture);
  await page.context().clearCookies({ name: '__Host-vocabbank-session' });
  await focusRefresh(page);
  await expect(form.getByRole('alert').filter({ hasText: 'Sign in again to save' })).toBeVisible();
  await expect(form.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled();
  await expectCapture(form, capture);
  await reauthenticate(page, true);
  await expect(
    form.getByRole('alert').filter({ hasText: 'Different account signed in' }),
  ).toBeVisible();
  const otherSession = await session(page);
  expect(otherSession.owner_id).not.toBe(originalSession.owner_id);
  await expectCapture(form, capture);
  await expect(form).toHaveAttribute('data-retained-dialog', 'edit');
  await expect(form.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled();
  let browserWrites = 0;
  page.on('request', (request) => {
    if (
      request.method() === 'PUT' &&
      new URL(request.url()).pathname === `/api/v1/words/${word.id}`
    )
      browserWrites++;
  });
  await form.locator('form').evaluate((node) => (node as HTMLFormElement).requestSubmit());
  expect(browserWrites).toBe(0);
  const wrongCSRF = await page.request.post('/api/v1/words', {
    headers: { Origin: origin, 'X-CSRF-Token': originalSession.csrf_token },
    data: { ...capture, idempotency_key: 'browser-cross-account-denied' },
  });
  expect(wrongCSRF.status()).toBe(403);
  const otherWords: WordPage = await (await page.request.get('/api/v1/words')).json();
  expect(otherWords.total).toBe(0);
  await reauthenticate(page);
  await expect(form.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled();
  await expectCapture(form, capture);
  // A direct Save can be the first request to discover auth loss too.
  await page.context().clearCookies({ name: '__Host-vocabbank-session' });
  await form.getByRole('button', { name: 'Save changes', exact: true }).click();
  await expect(form.getByRole('alert').filter({ hasText: 'Sign in again to save' })).toBeVisible();
  await expect(form.getByRole('button', { name: 'Retry save', exact: true })).toBeDisabled();
  await expectCapture(form, capture);
  await expect(form).toHaveAttribute('data-retained-dialog', 'edit');
  await reauthenticate(page);
  await expect(form.getByRole('button', { name: 'Retry save', exact: true })).toBeEnabled();
  await form.getByRole('button', { name: 'Retry save', exact: true }).click();
  await expect(page.getByRole('dialog', { name: capture.term, exact: true })).toBeVisible();
  const saved: Word = await (await page.request.get(`/api/v1/words/${word.id}`)).json();
  expect(saved).toMatchObject({ ...capture, revision: 2 });
});

import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { BrowserApp } from '@/browser-app'
import type { Bootstrap, SettingsPage } from '@/types'

const mediaType = 'application/vnd.agentnewsfeed.browser+json'
const settings: SettingsPage = {
  preferences: { revision: 1, updated_at: 0, preferences: {
    interests: [], reading_brief: '', avoid: '', sources: [], allow_new_sources: true, allow_suggestions: true,
    discovery: { enabled: false, max_per_run: 3, max_per_day: 12, exploration: 'balanced', publication_days: 7, include_evergreen: true },
  } },
  suggestions: [], discovery: { preferences: {} as SettingsPage['preferences'], recent_urls: [], runs: [], day: '2026-11-11', day_timezone: 'America/New_York', saved_today: 0, remaining_today: 12 }, runs: { runs: [] },
}
settings.discovery.preferences = settings.preferences
const initial: Bootstrap = { configured: true, authenticated: true, can_write: true, csrf: 'synthetic-settings-csrf', section: 'settings', view: 'feed', from_archive: false, sign_in_url: '/auth/login?return_to=%2Fsettings', settings }
const request = vi.fn<typeof fetch>()
const page = (data: Bootstrap) => new Response(JSON.stringify(data), { headers: { 'Content-Type': mediaType } })
const result = () => new Response('{}', { headers: { 'Content-Type': 'application/json' } })
beforeEach(() => {
  window.history.replaceState(null, '', '/settings'); request.mockReset(); vi.stubGlobal('fetch', request)
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() })
})
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); Reflect.deleteProperty(Element.prototype, 'scrollIntoView') })

it('starts empty, preserves an unsaved draft across SPA navigation, and saves the same intent on retry', async () => {
  const user = userEvent.setup()
  render(<BrowserApp initialData={initial} />)
  expect(screen.getByText('No interests yet.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled()
  await user.type(screen.getByLabelText('New interest'), 'Storage engines')
  await user.click(screen.getByRole('button', { name: 'Add interest' }))
  expect(screen.getByText('Unsaved changes')).toBeInTheDocument()
  request.mockResolvedValueOnce(page({ ...initial, section: 'todo', settings: undefined, todo: { tasks: [] } })).mockResolvedValueOnce(page(initial))
  await user.click(screen.getByRole('link', { name: 'To-do' }))
  await screen.findByRole('heading', { name: 'To-do' })
  await user.click(screen.getByRole('link', { name: 'Settings' }))
  await screen.findByText('Storage engines')
  request.mockResolvedValueOnce(new Response('', { status: 503 }))
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await screen.findByRole('button', { name: 'Retry same request' })
  const first = JSON.parse(String(request.mock.calls[2][1]?.body))
  expect(first.expected_revision).toBe(1)
  expect(first.preferences.interests).toEqual([{ name: 'Storage engines', preference: 'more' }])
  expect(request.mock.calls[2][1]?.headers).toMatchObject({ 'Content-Type': 'application/json', 'X-CSRF-Token': initial.csrf })
  const saved = { ...initial, settings: { ...settings, preferences: { revision: 2, updated_at: 123, preferences: first.preferences } } }
  request.mockResolvedValueOnce(result()).mockResolvedValueOnce(page(saved))
  await user.click(screen.getByRole('button', { name: 'Retry same request' }))
  await screen.findByText('Saved.')
  expect(JSON.parse(String(request.mock.calls[3][1]?.body))).toEqual(first)
  await waitFor(() => expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled())
})

it('keeps a conflicting draft for comparison and lets the user discard it for the current revision', async () => {
  const user = userEvent.setup(); render(<BrowserApp initialData={initial} />)
  await user.type(screen.getByLabelText('Reading brief'), 'My draft')
  request.mockResolvedValueOnce(new Response('', { status: 409 }))
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await screen.findByRole('alert')
  expect(screen.getByLabelText('Reading brief')).toHaveValue('My draft')
  const current = { ...initial, settings: { ...settings, preferences: { ...settings.preferences, revision: 2, preferences: { ...settings.preferences.preferences, reading_brief: 'Changed elsewhere' } } } }
  request.mockResolvedValueOnce(page(current))
  await user.click(screen.getByRole('button', { name: 'Reload current settings' }))
  await screen.findByText('Saved preferences changed. Your draft is kept for reference; discard it to edit the current revision.')
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled()
  expect(screen.getByLabelText('Reading brief')).toHaveValue('My draft')
  await user.click(screen.getByRole('button', { name: 'Discard draft' }))
  expect(screen.getByLabelText('Reading brief')).toHaveValue('Changed elsewhere')
})

it('shows proposal evidence and guards unsaved preferences before applying atomically', async () => {
  const user = userEvent.setup()
  const withProposal: Bootstrap = { ...initial, settings: { ...settings, suggestions: [{ id: 'proposal', revision: 1, status: 'pending', kind: 'source', name: 'example.com', preference: 'prefer', reason: 'Original research on your interest.', evidence_urls: ['https://example.com/research'], preferences_revision: 1, created_at: 1, resolved_at: 0 }] } }
  render(<BrowserApp initialData={withProposal} />)
  await user.type(screen.getByLabelText('Reading brief'), 'Unsaved brief')
  await user.click(screen.getByRole('tab', { name: 'Suggestions (1)' }))
  expect(screen.getByRole('link', { name: 'https://example.com/research' })).toHaveAttribute('rel', 'noopener noreferrer')
  expect(screen.getByText('Original research on your interest.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Apply example.com' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Discard draft' }))
  request.mockResolvedValueOnce(result()).mockResolvedValueOnce(page(initial))
  await user.click(screen.getByRole('button', { name: 'Apply example.com' }))
  await screen.findByText('All caught up. No pending suggestions.')
  const input = JSON.parse(String(request.mock.calls[0][1]?.body))
  expect(input).toMatchObject({ suggestion_id: 'proposal', expected_revision: 1, expected_preferences_revision: 1 })
  expect(input.idempotency_key).toBeTruthy()
})

it('waits for an outstanding settings write before a history navigation reads data', async () => {
  const user = userEvent.setup(); render(<BrowserApp initialData={initial} />)
  await user.type(screen.getByLabelText('Reading brief'), 'Save before leaving')
  let resolve!: (response: Response) => void
  request.mockReturnValueOnce(new Promise(settle => { resolve = settle })).mockResolvedValueOnce(page({ ...initial, section: 'todo', settings: undefined, todo: { tasks: [] } }))
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await act(async () => { window.history.replaceState(null, '', '/todo'); window.dispatchEvent(new PopStateEvent('popstate')) })
  expect(request).toHaveBeenCalledTimes(1)
  await act(async () => resolve(result()))
  await screen.findByRole('heading', { name: 'To-do' })
  expect(request).toHaveBeenCalledTimes(2)
})

it('disables old settings actions during a delayed section read and retains the unsaved draft', async () => {
  const user = userEvent.setup(); render(<BrowserApp initialData={initial} />)
  await user.type(screen.getByLabelText('Reading brief'), 'Keep this draft')
  let resolve!: (response: Response) => void
  request.mockReturnValueOnce(new Promise(settle => { resolve = settle }))
  await user.click(screen.getByRole('link', { name: 'To-do' }))
  expect(request).toHaveBeenCalledTimes(1)
  const save = screen.getByRole('button', { name: 'Save changes' })
  expect(save).toBeDisabled()
  expect(screen.getByLabelText('Reading brief')).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Discard draft' })).toBeDisabled()
  await user.click(save)
  expect(request).toHaveBeenCalledTimes(1)
  await act(async () => resolve(page({ ...initial, section: 'todo', settings: undefined, todo: { tasks: [] } })))
  await screen.findByRole('heading', { name: 'To-do' })
  request.mockResolvedValueOnce(page(initial))
  await user.click(screen.getByRole('link', { name: 'Settings' }))
  await screen.findByRole('heading', { name: 'Reading preferences' })
  expect(screen.getByLabelText('Reading brief')).toHaveValue('Keep this draft')
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeEnabled()
  expect(request.mock.calls.every(([, options]) => options?.method !== 'POST')).toBe(true)
})

it('disables old proposal actions during a delayed section read without applying or dismissing them', async () => {
  const user = userEvent.setup()
  const withProposal: Bootstrap = { ...initial, settings: { ...settings, suggestions: [{ id: 'proposal', revision: 1, status: 'pending', kind: 'source', name: 'example.com', preference: 'prefer', reason: 'Original research on your interest.', evidence_urls: ['https://example.com/research'], preferences_revision: 1, created_at: 1, resolved_at: 0 }] } }
  render(<BrowserApp initialData={withProposal} />)
  await user.click(screen.getByRole('tab', { name: 'Suggestions (1)' }))
  let resolve!: (response: Response) => void
  request.mockReturnValueOnce(new Promise(settle => { resolve = settle }))
  await user.click(screen.getByRole('link', { name: 'To-do' }))
  expect(screen.getByRole('button', { name: 'Apply example.com' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Dismiss example.com' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Apply example.com' }))
  await user.click(screen.getByRole('button', { name: 'Dismiss example.com' }))
  expect(request).toHaveBeenCalledTimes(1)
  await act(async () => resolve(page({ ...initial, section: 'todo', settings: undefined, todo: { tasks: [] } })))
  await screen.findByRole('heading', { name: 'To-do' })
  request.mockResolvedValueOnce(page(withProposal))
  await user.click(screen.getByRole('link', { name: 'Settings' }))
  await screen.findByRole('heading', { name: 'Reading preferences' })
  await user.click(screen.getByRole('tab', { name: 'Suggestions (1)' }))
  expect(screen.getByRole('button', { name: 'Apply example.com' })).toBeEnabled()
  expect(request.mock.calls.every(([, options]) => options?.method !== 'POST')).toBe(true)
})

it('rejects an old save after the address changes before the disabled controls rerender', async () => {
  const user = userEvent.setup(); render(<BrowserApp initialData={initial} />)
  await user.type(screen.getByLabelText('Reading brief'), 'Retain after stale action')
  window.history.replaceState(null, '', '/todo')
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await screen.findByText('This page is changing. Wait for the current settings page before saving.')
  expect(request).not.toHaveBeenCalled()
  expect(screen.getByLabelText('Reading brief')).toHaveValue('Retain after stale action')
})

function unloadIsPrevented() {
  const event = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(event)
  return event.defaultPrevented
}

it('warns for a retained settings draft from To-do and removes the warning after discard or save', async () => {
  const user = userEvent.setup(); render(<BrowserApp initialData={initial} />)
  expect(unloadIsPrevented()).toBe(false)
  await user.type(screen.getByLabelText('Reading brief'), 'Retained outside Settings')
  expect(unloadIsPrevented()).toBe(true)
  request.mockResolvedValueOnce(page({ ...initial, section: 'todo', settings: undefined, todo: { tasks: [] } })).mockResolvedValueOnce(page(initial))
  await user.click(screen.getByRole('link', { name: 'To-do' }))
  await screen.findByRole('heading', { name: 'To-do' })
  expect(unloadIsPrevented()).toBe(true)
  await user.click(screen.getByRole('link', { name: 'Settings' }))
  await screen.findByRole('heading', { name: 'Reading preferences' })
  await user.click(screen.getByRole('button', { name: 'Discard draft' }))
  expect(unloadIsPrevented()).toBe(false)
  await user.type(screen.getByLabelText('Reading brief'), 'Save the new draft')
  expect(unloadIsPrevented()).toBe(true)
  const saved = { ...initial, settings: { ...settings, preferences: { revision: 2, updated_at: 123, preferences: { ...settings.preferences.preferences, reading_brief: 'Save the new draft' } } } }
  request.mockResolvedValueOnce(result()).mockResolvedValueOnce(page(saved))
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await screen.findByText('Saved.')
  expect(unloadIsPrevented()).toBe(false)
})

it.each([
  ['an unauthenticated page', () => page({ ...initial, authenticated: false, can_write: false, csrf: '', section: 'todo', settings: undefined })],
  ['a different session CSRF token', () => page({ ...initial, csrf: 'new-session-csrf', section: 'todo', settings: undefined, todo: { tasks: [] } })],
  ['an expired session', () => new Response('', { status: 401 })],
  ['denied page authorization', () => new Response('', { status: 403 })],
] as const)('clears the retained settings warning after reading %s', async (_, response) => {
  const user = userEvent.setup(); render(<BrowserApp initialData={initial} />)
  await user.type(screen.getByLabelText('Reading brief'), 'Private draft for this session')
  expect(unloadIsPrevented()).toBe(true)
  request.mockResolvedValueOnce(response())
  await user.click(screen.getByRole('link', { name: 'To-do' }))
  await waitFor(() => expect(unloadIsPrevented()).toBe(false))
})

it('clears the retained settings warning when a To-do mutation reports session expiry', async () => {
  const user = userEvent.setup(); render(<BrowserApp initialData={initial} />)
  await user.type(screen.getByLabelText('Reading brief'), 'Retained draft')
  request.mockResolvedValueOnce(page({ ...initial, section: 'todo', settings: undefined, todo: { tasks: [] } }))
  await user.click(screen.getByRole('link', { name: 'To-do' }))
  await screen.findByRole('heading', { name: 'To-do' })
  expect(unloadIsPrevented()).toBe(true)
  await user.type(screen.getByPlaceholderText('Add a task…'), 'A new task')
  request.mockResolvedValueOnce(new Response('', { status: 401 })).mockResolvedValueOnce(page({ ...initial, authenticated: false, can_write: false, csrf: '', section: 'todo', settings: undefined }))
  await user.click(screen.getByRole('button', { name: 'Add task' }))
  await waitFor(() => expect(unloadIsPrevented()).toBe(false))
})

it.each(['To-do', 'News'] as const)('keeps a failed %s write visible in Settings with source-bound recovery and independent local errors', async section => {
  const user = userEvent.setup()
  render(<BrowserApp initialData={initial} />)
  await user.type(screen.getByLabelText('Reading brief'), 'Keep my settings draft')
  const source: Bootstrap = section === 'To-do'
    ? { ...initial, section: 'todo', settings: undefined, todo: { tasks: [] } }
    : { ...initial, section: 'news', settings: undefined, page: { articles: [{
      id: 'source-story', url: 'https://example.com/story', title: 'Source story', summary: 'Saved introduction',
      revision: 1, created_at: 1793145600000, updated_at: 1793145600000,
    }] } }
  request.mockResolvedValueOnce(page(source))
  await user.click(screen.getByRole('link', { name: section }))
  await screen.findByRole('heading', { name: section === 'To-do' ? 'To-do' : 'Your reading shelf' })
  if (section === 'To-do') await user.type(screen.getByLabelText('New task'), 'A pending task')
  let resolveWrite!: (response: Response) => void
  request.mockReturnValueOnce(new Promise(settle => { resolveWrite = settle })).mockResolvedValueOnce(page(initial))
  await user.click(screen.getByRole('button', { name: section === 'To-do' ? 'Add task' : 'Archive Source story' }))
  await user.click(screen.getByRole('link', { name: 'Settings' }))
  expect(request).toHaveBeenCalledTimes(2)
  await act(async () => resolveWrite(new Response('<script>private server diagnostics</script>', { status: 503 })))
  await screen.findByRole('heading', { name: 'Reading preferences' })
  const settingsMain = screen.getByRole('main')
  const sharedError = within(settingsMain).getByRole('alert')
  expect(sharedError).toHaveTextContent('The change could not be confirmed.')
  expect(within(sharedError).queryByRole('button', { name: 'Refresh page' })).not.toBeInTheDocument()
  expect(screen.queryByText('private server diagnostics')).not.toBeInTheDocument()
  expect(screen.getByLabelText('Reading brief')).toHaveValue('Keep my settings draft')
  expect(settingsMain).toHaveFocus()
  await user.tab()
  expect(within(sharedError).getByRole('button', { name: 'Dismiss save error' })).toHaveFocus()

  request.mockResolvedValueOnce(page(source))
  await user.click(screen.getByRole('link', { name: section }))
  await screen.findByRole('heading', { name: section === 'To-do' ? 'To-do' : 'Your reading shelf' })
  expect(within(screen.getByRole('alert')).getByRole('button', { name: 'Refresh page' })).toBeEnabled()
  request.mockResolvedValueOnce(page(initial))
  await user.click(screen.getByRole('link', { name: 'Settings' }))
  await screen.findByRole('heading', { name: 'Reading preferences' })
  expect(screen.queryByRole('button', { name: 'Refresh page' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Sources' }))
  await user.type(screen.getByLabelText('Source domain'), 'not-a-domain')
  await user.click(screen.getByRole('button', { name: 'Add source' }))
  const localMessage = 'Enter a new exact domain such as example.com, without a URL or path.'
  expect(screen.getAllByRole('alert')).toHaveLength(2)
  expect(screen.getByText(localMessage)).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Dismiss save error' }))
  expect(screen.getByRole('alert')).toHaveTextContent(localMessage)
  expect(screen.queryByText('The change could not be confirmed.')).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Interests' }))
  expect(screen.getByLabelText('Reading brief')).toHaveValue('Keep my settings draft')
  expect(request.mock.calls.map(([, options]) => options?.method)).toEqual(['GET', 'POST', 'GET', 'GET', 'GET'])
})

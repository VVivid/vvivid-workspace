import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { BrowserApp } from '@/browser-app'
import type { Bootstrap, TodoList, TodoTask } from '@/types'

const mediaType = 'application/vnd.agentnewsfeed.browser+json'
const news: Bootstrap = {
  configured: true, authenticated: true, can_write: true, csrf: 'synthetic-csrf',
  view: 'feed', from_archive: false, sign_in_url: '/auth/login?return_to=%2F', section: 'news',
  page: { articles: [] },
}
const taskID = '00000000-0000-4000-8000-000000000001'
const task: TodoTask = {
  id: taskID, title: 'Review the SQLite paper', tags: ['Reading', 'Work'], estimate_minutes: 120, revision: 1,
}
const request = vi.fn<typeof fetch>()

function checklist(todo: TodoList = { tasks: [] }): Bootstrap {
  return { ...news, page: undefined, section: 'todo', sign_in_url: '/auth/login?return_to=%2Ftodo', todo }
}

function json(data: unknown) {
  return new Response(JSON.stringify(data), { headers: { 'Content-Type': mediaType } })
}

function saved(todo: TodoList) {
  request.mockResolvedValueOnce(json({ location: '/todo' })).mockResolvedValueOnce(json(checklist(todo)))
}

function postedFields(call: number) {
  const [url, options] = request.mock.calls[call]
  expect(new URL(String(url)).pathname).toBe('/api/v1/todos')
  expect(options).toMatchObject({ method: 'POST', credentials: 'same-origin', headers: { Accept: mediaType } })
  return Object.fromEntries(options?.body as URLSearchParams)
}

beforeEach(() => {
  window.history.replaceState(null, '', '/todo')
  request.mockReset()
  vi.stubGlobal('fetch', request)
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() })
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})

describe('private To-do checklist', () => {
  it('navigates into To-do and saves add, check, reopen, remove and undo in the same view', async () => {
    const user = userEvent.setup()
    vi.spyOn(crypto, 'randomUUID').mockReturnValue(taskID)
    window.history.replaceState(null, '', '/')
    request.mockResolvedValueOnce(json(checklist()))
    render(<BrowserApp initialData={news} />)
    const home = screen.getByRole('link', { name: 'Newsfeed home' })
    await user.click(screen.getByRole('link', { name: 'To-do' }))
    expect(await screen.findByRole('heading', { name: 'To-do' })).toBeVisible()
    expect(screen.getByRole('link', { name: 'To-do', current: 'page' })).toBeVisible()
    const title = screen.getByRole('textbox', { name: 'New task' })
    await user.type(title, task.title)
    await user.type(screen.getByRole('textbox', { name: 'Tags' }), 'Reading, Work')
    const estimate = screen.getByRole('combobox', { name: 'Estimate' })
    for (const option of ['30 min', '2 hours', '4 hours']) {
      expect(within(estimate).getByRole('option', { name: option })).toBeInTheDocument()
    }
    await user.selectOptions(estimate, '120')
    saved({ tasks: [task] })
    await user.click(screen.getByRole('button', { name: 'Add task' }))
    const open = screen.getByRole('region', { name: 'Open tasks' })
    const openHeading = within(open).getByRole('heading', { name: 'Open tasks' })
    expect(await within(open).findByRole('checkbox', { name: task.title })).not.toBeChecked()
    expect(screen.queryByText(/^Completed \(/, { selector: 'summary' })).not.toBeInTheDocument()
    expect(within(open).getByText('Work')).toBeVisible()
    expect(within(open).getByText('Reading')).toBeVisible()
    expect(within(open).getByText('2 hours')).toBeVisible()
    expect(postedFields(1)).toEqual({
      action: 'add', csrf_token: news.csrf, task_id: taskID, title: task.title, tags: 'Reading, Work', estimate_minutes: '120',
    })
    expect(title).toHaveValue('')
    expect(title).toHaveFocus()
    expect(window.location.pathname).toBe('/todo')
    const navigations = vi.mocked(window.scrollTo).mock.calls.length

    await user.type(title, 'A new draft')
    const checked = { ...task, revision: 2, completed_at: Date.now() }
    saved({ tasks: [checked] })
    await user.click(within(open).getByRole('checkbox', { name: task.title }))
    const summary = await screen.findByText('Completed (1)', { selector: 'summary' })
    const completed = summary.closest('details')!
    const completedTask = within(completed).getByLabelText(task.title)
    expect(completed).not.toHaveAttribute('open')
    expect(completedTask).not.toBeVisible()
    expect(openHeading).toHaveFocus()
    expect(postedFields(3)).toMatchObject({ action: 'complete', task_id: taskID, expected_revision: '1' })

    await user.click(summary)
    expect(completedTask).toBeVisible()
    expect(completedTask).toBeChecked()
    const reopened = { ...task, revision: 3 }
    saved({ tasks: [reopened] })
    await user.click(completedTask)
    expect(await within(open).findByRole('checkbox', { name: task.title })).not.toBeChecked()
    expect(summary).not.toBeInTheDocument()
    expect(openHeading).toHaveFocus()
    expect(postedFields(5)).toMatchObject({ action: 'reopen', expected_revision: '2' })

    saved({ tasks: [], undo: { ...task, revision: 4 } })
    await user.click(within(open).getByRole('button', { name: `Remove ${task.title}` }))
    expect(await screen.findByText('Task removed.')).toBeVisible()
    expect(within(open).queryByRole('checkbox')).not.toBeInTheDocument()
    expect(postedFields(7)).toMatchObject({ action: 'remove', expected_revision: '3' })
    saved({ tasks: [{ ...task, revision: 5 }] })
    await user.click(screen.getByRole('button', { name: 'Undo' }))
    expect(await within(open).findByRole('checkbox', { name: task.title })).not.toBeChecked()
    expect(postedFields(9)).toMatchObject({ action: 'undo', expected_revision: '4' })
    expect(screen.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument()
    expect(title).toHaveValue('A new draft')
    expect(screen.getByRole('link', { name: 'Newsfeed home' })).toBe(home)
    expect(window.scrollTo).toHaveBeenCalledTimes(navigations)
  })

  it('keeps Undo visible outside completed tasks after removing a finished task', async () => {
    const user = userEvent.setup()
    const checked = { ...task, revision: 2, completed_at: Date.now() }
    const earlier = {
      id: 'earlier', title: 'Read the storage docs', revision: 2, completed_at: Date.parse('2026-01-09T19:00:00Z'),
    }
    render(<BrowserApp initialData={checklist({ tasks: [checked, earlier] })} />)
    const summary = screen.getByText('Completed (2)', { selector: 'summary' })
    const completed = summary.closest('details')!
    const earlierTask = within(completed).getByLabelText(earlier.title)
    expect(earlierTask).not.toBeVisible()
    await user.click(summary)
    expect(earlierTask).toBeVisible()
    expect(within(completed).getByRole('heading', { name: 'January 9, 2026' })).toBeVisible()

    saved({ tasks: [earlier], undo: { ...checked, revision: 3 } })
    await user.click(within(completed).getByRole('button', { name: `Remove ${task.title}` }))
    const undo = await screen.findByRole('button', { name: 'Undo' })
    expect(summary).toHaveTextContent('Completed (1)')
    expect(summary).toHaveFocus()
    expect(earlierTask).toBeVisible()
    expect(within(completed).queryByLabelText(task.title)).not.toBeInTheDocument()
    expect(postedFields(0)).toMatchObject({ action: 'remove', task_id: taskID, expected_revision: '2' })

    await user.click(summary)
    expect(earlierTask).not.toBeVisible()
    expect(undo).toBeVisible()
    saved({ tasks: [{ ...checked, revision: 4 }, earlier] })
    await user.click(undo)
    await waitFor(() => expect(summary).toHaveTextContent('Completed (2)'))
    expect(completed).not.toHaveAttribute('open')
    const restoredTask = within(completed).getByLabelText(task.title)
    expect(restoredTask).not.toBeVisible()
    await user.click(summary)
    expect(restoredTask).toBeVisible()
    expect(restoredTask).toBeChecked()
    expect(postedFields(2)).toMatchObject({ action: 'undo', task_id: taskID, expected_revision: '3' })
  })

  it('focuses Undo when the last completed task is removed and the summary when it returns', async () => {
    const user = userEvent.setup()
    const completed = { ...task, revision: 2, completed_at: Date.now() }
    const open = { id: 'carry', title: 'Carry forward', revision: 1 }
    render(<BrowserApp initialData={checklist({ tasks: [open, completed] })} />)
    const summary = screen.getByText('Completed (1)', { selector: 'summary' })
    await user.click(summary)

    saved({ tasks: [open], undo: { ...completed, revision: 3 } })
    screen.getByRole('button', { name: `Remove ${task.title}` }).focus()
    await user.keyboard('{Enter}')

    const undo = await screen.findByRole('button', { name: 'Undo' })
    expect(summary).not.toBeInTheDocument()
    expect(undo).toHaveFocus()
    const scrolledElements = vi.mocked(Element.prototype.scrollIntoView).mock.contexts
    expect(scrolledElements).toContain(undo)

    saved({ tasks: [open, { ...completed, revision: 4 }] })
    await user.keyboard('{Enter}')
    const restoredSummary = await screen.findByText('Completed (1)', { selector: 'summary' })
    expect(undo).not.toBeInTheDocument()
    expect(restoredSummary.closest('details')).not.toHaveAttribute('open')
    expect(restoredSummary).toHaveFocus()
    expect(scrolledElements).toContain(restoredSummary)
  })

  it('does not focus Undo for another task removed while the page reloads', async () => {
    const user = userEvent.setup()
    const completed = { ...task, revision: 2, completed_at: Date.now() }
    render(<BrowserApp initialData={checklist({ tasks: [completed] })} />)
    const summary = screen.getByText('Completed (1)', { selector: 'summary' })
    await user.click(summary)

    saved({ tasks: [], undo: { id: 'other', title: 'Another task', revision: 2 } })
    await user.click(screen.getByRole('button', { name: `Remove ${task.title}` }))
    expect(await screen.findByRole('button', { name: 'Undo' })).toBeVisible()
    expect(summary).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Open tasks' })).toHaveFocus()
  })

  it('preserves an unconfirmed draft ID and retries a failed refresh with GET only', async () => {
    const user = userEvent.setup()
    render(<BrowserApp initialData={checklist()} />)
    const title = screen.getByRole('textbox', { name: 'New task' })
    const addButton = screen.getByRole('button', { name: 'Add task' })
    const form = addButton.closest('form')!
    // Model browser named access, where the action field shadows form.action.
    Object.defineProperty(form, 'action', { value: form.elements.namedItem('action') })
    await user.type(title, task.title)
    request.mockRejectedValueOnce(new Error('connection lost'))
    await user.click(addButton)
    expect(await screen.findByRole('alert')).toHaveTextContent('The change could not be confirmed.')
    const original = postedFields(0)
    expect(original.task_id).toBeTruthy()
    expect(title).toHaveValue(task.title)
    expect(request).toHaveBeenCalledTimes(1)

    request.mockResolvedValueOnce(json(checklist()))
    await user.click(screen.getByRole('button', { name: 'Refresh page' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add task' })).toHaveAttribute('aria-disabled', 'false'))
    expect(title).toHaveValue(task.title)

    request.mockResolvedValueOnce(json({ location: '/todo' })).mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
    await user.click(screen.getByRole('button', { name: 'Add task' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The change was saved. This page could not load.')
    expect(postedFields(2)).toEqual(original)
    request.mockResolvedValueOnce(json(checklist({ tasks: [{ ...task, id: original.task_id }] })))
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('checkbox', { name: task.title })).toBeVisible()
    expect(screen.getByRole('textbox', { name: 'New task' })).toHaveValue('')
    expect(request.mock.calls.map(([, options]) => options?.method)).toEqual(['POST', 'GET', 'POST', 'GET', 'GET'])
  })

  it('retains a failed save notice and draft when navigating away during the request', async () => {
    const user = userEvent.setup()
    let finishWrite!: (response: Response) => void
    request.mockReturnValueOnce(new Promise(resolve => { finishWrite = resolve }))
      .mockResolvedValueOnce(json(news)).mockResolvedValueOnce(json(checklist()))
    render(<BrowserApp initialData={checklist()} />)
    await user.type(screen.getByRole('textbox', { name: 'New task' }), task.title)
    await user.click(screen.getByRole('button', { name: 'Add task' }))
    await user.click(screen.getByRole('link', { name: 'News' }))
    await act(async () => finishWrite(new Response('Conflict', { status: 409 })))
    expect(await screen.findByRole('heading', { name: 'Your reading shelf' })).toBeVisible()
    expect(screen.getByRole('alert')).toHaveTextContent('The change was rejected.')
    await user.click(screen.getByRole('link', { name: 'To-do' }))
    expect(await screen.findByRole('textbox', { name: 'New task' })).toHaveValue(task.title)
    expect(request.mock.calls.map(([, options]) => options?.method)).toEqual(['POST', 'GET', 'GET'])
  })

  it('shows a signed-out gate for a direct To-do address', () => {
    render(<BrowserApp initialData={{ ...checklist(), authenticated: false, can_write: false, csrf: '', todo: undefined }} />)
    expect(screen.getByRole('link', { name: 'Sign in' })).toHaveAttribute('href', '/auth/login?return_to=%2Ftodo')
    expect(screen.queryByRole('navigation', { name: 'Sections' })).not.toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'New task' })).not.toBeInTheDocument()
  })

  it('disables writes for a read-only session', () => {
    render(<BrowserApp initialData={{ ...checklist({ tasks: [task] }), can_write: false }} />)
    expect(screen.getByRole('checkbox', { name: task.title })).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Add task' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Remove/ })).not.toBeInTheDocument()
    expect(request).not.toHaveBeenCalled()
  })
})

describe('Eastern completion days', () => {
  it.each([
    ['2026-03-09T04:00:00.000Z', 'March 8, 2026'],
    ['2026-11-02T05:00:00.000Z', 'November 1, 2026'],
  ])('updates the completion day at %s without a page read or lost focus', (midnight, yesterday) => {
    vi.useFakeTimers()
    const boundary = Date.parse(midnight)
    vi.setSystemTime(boundary - 100)
    const completed = { ...task, completed_at: boundary - 60_000 }
    const carry = { id: 'carry', title: 'Carry forward', revision: 1 }
    render(<BrowserApp initialData={checklist({ tasks: [completed, carry] })} />)
    const summary = screen.getByText('Completed (1)', { selector: 'summary' })
    const details = summary.closest('details')!
    fireEvent.click(summary)
    expect(within(details).getByRole('heading', { name: 'Today' })).toBeVisible()
    const checkbox = within(details).getByRole('checkbox', { name: completed.title })
    checkbox.focus()
    act(() => vi.advanceTimersByTime(100))
    expect(within(details).queryByRole('heading', { name: 'Today' })).not.toBeInTheDocument()
    expect(within(details).getByRole('heading', { name: yesterday })).toBeVisible()
    expect(checkbox).toBeVisible()
    expect(checkbox).toBeChecked()
    expect(checkbox).toHaveFocus()
    expect(within(screen.getByRole('region', { name: 'Open tasks' })).getByRole('checkbox', { name: carry.title })).not.toBeChecked()
    expect(request).not.toHaveBeenCalled()
  })

  it('rechecks the date when a backgrounded page resumes', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-01T04:59:59.000Z'))
    const completed = { ...task, completed_at: Date.parse('2026-11-01T04:00:01.000Z') }
    render(<BrowserApp initialData={checklist({ tasks: [completed] })} />)
    const summary = screen.getByText('Completed (1)', { selector: 'summary' })
    const details = summary.closest('details')!
    expect(within(details).getByText('Today')).not.toBeVisible()
    vi.setSystemTime(new Date('2026-11-02T05:01:00.000Z'))
    fireEvent(document, new Event('visibilitychange'))
    expect(details).not.toHaveAttribute('open')
    fireEvent.click(summary)
    expect(within(details).getByRole('heading', { name: 'November 1, 2026' })).toBeVisible()
    expect(within(details).getByRole('checkbox', { name: completed.title })).toBeChecked()
    expect(request).not.toHaveBeenCalled()
  })
})

describe('saved task order', () => {
  it('moves adjacent tasks with revisions, disables boundaries, and preserves keyboard focus', async () => {
    const user = userEvent.setup()
    const next = { id: 'next', title: 'Read the migration guide', revision: 1 }
    render(<BrowserApp initialData={checklist({ tasks: [task, next] })} />)
    expect(screen.getByRole('button', { name: `Move up: ${task.title}` })).toBeDisabled()
    expect(screen.getByRole('button', { name: `Move down: ${next.title}` })).toBeDisabled()
    const order = { tasks: [{ ...next, revision: 2 }, { ...task, revision: 2 }] }
    saved(order)
    screen.getByRole('button', { name: `Move down: ${task.title}` }).focus()
    await user.keyboard('{Enter}')
    await waitFor(() => expect(screen.getAllByRole('checkbox').map(input => input.getAttribute('aria-label')))
      .toEqual([next.title, task.title]))
    expect(postedFields(0)).toMatchObject({ action: 'reorder', task_id: task.id, expected_revision: '1',
      neighbor_id: next.id, neighbor_revision: '1', direction: 'down', idempotency_key: expect.any(String) })
    expect(screen.getByRole('button', { name: `Move down: ${task.title}` })).toBeDisabled()
    expect(screen.getByRole('checkbox', { name: task.title })).toHaveFocus()
    expect(screen.getByText(`${task.title} moved to position 2 of 2.`)).toBeInTheDocument()
  })

  it('retains the exact move intent after a lost response and uses the refreshed server order', async () => {
    const user = userEvent.setup()
    const next = { id: 'next', title: 'Read the migration guide', revision: 1 }
    request.mockRejectedValueOnce(new TypeError('Response lost'))
    render(<BrowserApp initialData={checklist({ tasks: [task, next] })} />)
    await user.click(screen.getByRole('button', { name: `Move down: ${task.title}` }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The change could not be confirmed.')
    const initial = postedFields(0)
    saved({ tasks: [{ ...next, revision: 2 }, { ...task, revision: 2 }] })
    await user.click(screen.getByRole('button', { name: `Move down: ${task.title}` }))
    await waitFor(() => expect(screen.getAllByRole('checkbox')[0]).toHaveAttribute('aria-label', next.title))
    expect(postedFields(1)).toEqual(initial)
  })
})

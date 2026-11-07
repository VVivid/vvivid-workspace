import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { BrowserApp } from '@/browser-app'
import type { Article, Bootstrap, Opportunity } from '@/types'

const mediaType = 'application/vnd.agentnewsfeed.browser+json'
const article: Article = {
  id: 'story', url: 'https://source.example/story', title: 'Saved story', summary: 'Saved introduction',
  revision: 1, created_at: 1793145600000, updated_at: 1793145600000,
}
const opportunity: Opportunity = {
  id: 'listing', url: 'https://source.example/listing', title: 'Saved role', company: 'Source company',
  kind: 'job', revision: 1, created_at: article.created_at, updated_at: article.updated_at,
}
const shelf: Bootstrap = {
  configured: true, authenticated: true, can_write: true, csrf: 'synthetic-csrf',
  view: 'feed', from_archive: false, sign_in_url: '/auth/login?return_to=%2F',
  section: 'news', page: { articles: [article] },
}
const reader: Bootstrap = { ...shelf, page: undefined, view: 'reader', article, mutation_key: 'synthetic-key' }
const jobs: Bootstrap = {
  ...shelf, section: 'jobs', page: undefined, opportunities_page: { opportunities: [opportunity] },
  opportunity, mutation_key: 'listing-key',
}

function json(data: unknown) {
  return new Response(JSON.stringify(data), { headers: { 'Content-Type': mediaType } })
}

function deferredPage() {
  let resolve!: (value: Response) => void
  const response = new Promise<Response>(settle => { resolve = settle })
  return { response, resolve }
}

function mockViewport(width: number) {
  const media = new EventTarget()
  Object.defineProperty(media, 'matches', { get: () => width <= 640 })
  vi.stubGlobal('matchMedia', (query: string) => {
    expect(query).toBe('(max-width: 640px)')
    return media
  })
  return (nextWidth: number) => {
    width = nextWidth
    media.dispatchEvent(new Event('change'))
  }
}

function sizedShelf(size: number): Bootstrap {
  const articles = Array.from({ length: size }, (_, index) => ({ ...article, id: `${article.id}-${index}` }))
  return { ...shelf, page_size: size, page: { articles } }
}

const request = vi.fn<typeof fetch>()

beforeEach(() => {
  window.history.replaceState(null, '', '/')
  request.mockReset()
  vi.stubGlobal('fetch', request)
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
})

describe('browser navigation', () => {
  it('loads a Jobs cursor page and detail, then follows Back and Forward in the same document', async () => {
    const user = userEvent.setup()
    const secondPage = { ...jobs, cursor: 'second-page' }
    const selected = { ...secondPage, detail_selected: true }
    request.mockResolvedValueOnce(json({ ...jobs, opportunities_page: { opportunities: [opportunity], next_cursor: 'second-page' } }))
      .mockResolvedValueOnce(json(secondPage))
      .mockResolvedValueOnce(json(selected))
      .mockResolvedValueOnce(json(secondPage))
      .mockResolvedValueOnce(json(selected))
    render(<BrowserApp initialData={shelf} />)
    const home = screen.getByRole('link', { name: 'Newsfeed home' })

    await user.click(screen.getByRole('link', { name: 'Jobs & Internships' }))
    expect(await screen.findByRole('heading', { name: 'Your next chapter.' })).toBeVisible()
    expect(screen.getByRole('main')).toHaveFocus()
    expect(request).toHaveBeenCalledWith(new URL('/jobs', window.location.origin), expect.objectContaining({
      method: 'GET', mode: 'same-origin', credentials: 'same-origin', cache: 'no-store', redirect: 'error',
      headers: { Accept: mediaType, 'X-Newsfeed-Page-Size': '3' },
    }))
    await user.click(screen.getByRole('link', { name: 'Next' }))
    await waitFor(() => expect(window.location.search).toBe('?cursor=second-page'))
    await user.click(screen.getByRole('link', { name: /View Saved role/ }))
    await waitFor(() => expect(screen.getByRole('article', { name: 'Selected listing' })).toHaveFocus())
    expect(window.location.pathname + window.location.search + window.location.hash).toBe(
      '/jobs/listing?from=feed&cursor=second-page#opportunity-detail',
    )
    expect(screen.getByRole('link', { name: 'Back to listings' })).toHaveAttribute('href', '/jobs?cursor=second-page#opportunity-list')

    window.history.back()
    await waitFor(() => expect(screen.getByRole('main')).toHaveFocus())
    expect(window.location.pathname + window.location.search).toBe('/jobs?cursor=second-page')
    window.history.forward()
    await waitFor(() => expect(screen.getByRole('article', { name: 'Selected listing' })).toHaveFocus())
    expect(screen.getByRole('link', { name: 'Newsfeed home' })).toBe(home)
  })

  it('keeps a loading announcement and ignores a superseded page result', async () => {
    const user = userEvent.setup()
    const earlier = deferredPage()
    request.mockReturnValueOnce(earlier.response).mockResolvedValueOnce(json({ ...shelf, view: 'trash', page: { articles: [] } }))
    render(<BrowserApp initialData={shelf} />)
    await user.click(screen.getByRole('link', { name: 'Archive' }))
    expect(screen.getByRole('status')).toHaveTextContent('Loading page')
    expect(window.location.pathname).toBe('/')
    await user.click(screen.getByRole('link', { name: 'Trash' }))
    expect(await screen.findByRole('heading', { name: 'Your trash' })).toBeVisible()
    await act(async () => earlier.resolve(json({ ...shelf, view: 'archive', page: { articles: [] } })))
    expect(window.location.pathname).toBe('/trash')
    expect(screen.getByRole('heading', { name: 'Your trash' })).toBeVisible()
  })

  it('drops the old Jobs view when a newer load fails after Back changes the address', async () => {
    const user = userEvent.setup()
    const backRead = deferredPage()
    request.mockResolvedValueOnce(json(jobs)).mockReturnValueOnce(backRead.response)
      .mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
      .mockResolvedValueOnce(json({ ...jobs, view: 'archive', opportunity: undefined }))
    render(<BrowserApp initialData={shelf} />)

    await user.click(screen.getByRole('link', { name: 'Jobs & Internships' }))
    const archiveLink = within(await screen.findByRole('navigation', { name: 'Listing views' }))
      .getByRole('link', { name: 'Archive' })
    const archiveButton = screen.getByRole('button', { name: 'Archive listing' })
    window.history.back()
    await waitFor(() => expect(request).toHaveBeenCalledTimes(2))
    expect(window.location.pathname).toBe('/')
    expect(archiveLink).not.toBeVisible()
    expect(archiveButton).toBeDisabled()

    // Exercise a click already dispatched from the old view during the transition.
    fireEvent.click(archiveLink)
    expect(await screen.findByRole('alert')).toHaveTextContent('This page could not load.')
    await act(async () => backRead.resolve(json(shelf)))
    expect(window.location.pathname).toBe('/')
    expect(archiveButton).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Your reading shelf' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to feed' })).toHaveAttribute('href', '/')

    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('link', { name: 'Archive', current: 'page' })).toBeVisible()
    expect(window.location.pathname).toBe('/jobs/archive')
    expect(request.mock.calls.map(([url, options]) => [new URL(String(url)).pathname, options?.method])).toEqual([
      ['/jobs', 'GET'], ['/', 'GET'], ['/jobs/archive', 'GET'], ['/jobs/archive', 'GET'],
    ])
  })

  it('keeps fragment history when Back supersedes a pending page request', async () => {
    const user = userEvent.setup()
    const pendingJobs = deferredPage()
    request.mockReturnValueOnce(pendingJobs.response).mockResolvedValueOnce(json(shelf))
    render(<BrowserApp initialData={shelf} />)

    await user.tab()
    expect(screen.getByRole('link', { name: 'Skip to reading' })).toHaveFocus()
    await user.keyboard('{Enter}')
    await waitFor(() => expect(window.location.hash).toBe('#main'))
    expect(request).not.toHaveBeenCalled()

    await user.click(screen.getByRole('link', { name: 'Jobs & Internships' }))
    expect(screen.getByRole('status')).toHaveTextContent('Loading page')
    window.history.back()
    await waitFor(() => expect(window.location.hash).toBe(''))
    await act(async () => pendingJobs.resolve(json(jobs)))
    expect(window.location.pathname).toBe('/')
    expect(screen.getByRole('heading', { name: 'Your reading shelf' })).toBeVisible()
    expect(screen.getByRole('status')).toBeEmptyDOMElement()

    window.history.forward()
    await waitFor(() => expect(window.location.hash).toBe('#main'))
    expect(request.mock.calls.map(([url]) => String(url))).toEqual([
      window.location.origin + '/jobs', window.location.origin + '/',
    ])
  })

  it.each([
    { link: 'Feed', view: 'feed', thumbnailPath: '/thumbnails/story' },
    { link: 'Trash', view: 'trash', thumbnailPath: '/thumbnails/story?trashed=true' },
  ])('requests a failed thumbnail again after loading $link', async ({ link, view, thumbnailPath }) => {
    const user = userEvent.setup()
    const thumbnailArticle = { ...article, thumbnail_url: 'https://source.example/thumbnail.jpg' }
    const initialData = { ...shelf, page: { articles: [thumbnailArticle] } }
    const loadedArticle = view === 'trash' ? { ...thumbnailArticle, trashed_at: article.updated_at } : thumbnailArticle
    request.mockResolvedValueOnce(json({ ...initialData, view, page: { articles: [loadedArticle] } }))
    const { container } = render(<BrowserApp initialData={initialData} />)
    fireEvent.error(container.querySelector('img')!)
    expect(screen.getByText('A story for a slower moment')).toBeVisible()

    await user.click(screen.getByRole('link', { name: link }))
    await waitFor(() => expect(container.querySelector('img')).toHaveAttribute('src', thumbnailPath))
  })

  it('offers a way back to the feed when an earlier detail is no longer readable', async () => {
    window.history.replaceState(null, '', '/articles/story?from=trash')
    const user = userEvent.setup()
    const trashed = { ...reader, from_trash: true, article: { ...article, trashed_at: article.updated_at } }
    request.mockResolvedValueOnce(json(jobs)).mockResolvedValueOnce(new Response('Unavailable', { status: 404 }))
      .mockResolvedValueOnce(json(shelf))
    render(<BrowserApp initialData={trashed} />)
    await user.click(screen.getByRole('link', { name: 'Jobs & Internships' }))
    expect(await screen.findByRole('heading', { name: 'Your next chapter.' })).toBeVisible()
    window.history.back()
    expect(await screen.findByRole('alert')).toHaveTextContent('unavailable in the requested view')
    expect(screen.queryByRole('heading', { name: 'Your next chapter.' })).not.toBeInTheDocument()
    expect(window.location.pathname + window.location.search).toBe('/articles/story?from=trash')
    await user.click(screen.getByRole('link', { name: 'Back to feed' }))
    expect(await screen.findByRole('heading', { name: 'Your reading shelf' })).toBeVisible()
    expect(window.location.pathname).toBe('/')
  })

  it('keeps modified clicks, skip links, source links, and sign-out native', () => {
    window.history.replaceState(null, '', '/articles/story?from=feed')
    render(<BrowserApp initialData={reader} />)

    function expectNative(element: HTMLElement, event: Event) {
      const observe = vi.fn((actual: Event) => {
        expect(actual.defaultPrevented).toBe(false)
        actual.preventDefault()
      })
      document.addEventListener(event.type, observe, { once: true })
      fireEvent(element, event)
      expect(observe).toHaveBeenCalledOnce()
    }

    const archive = screen.getByRole('link', { name: 'Archive' })
    for (const options of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }]) {
      expectNative(archive, new MouseEvent('click', { bubbles: true, cancelable: true, ...options }))
    }
    for (const link of [screen.getByRole('link', { name: 'Skip to reading' }), screen.getAllByRole('link', { name: 'Read original' })[0]]) {
      expectNative(link, new MouseEvent('click', { bubbles: true, cancelable: true }))
    }
    expectNative(screen.getByRole('button', { name: 'Sign out' }).closest('form')!,
      new SubmitEvent('submit', { bubbles: true, cancelable: true }))
    expect(request).not.toHaveBeenCalled()
  })
})

describe('responsive list pages', () => {
  it('expands a native shell at 641px and refetches the same cursor at 640px', async () => {
    const resize = mockViewport(641)
    window.history.replaceState(null, '', '/?cursor=current')
    const originalHistoryLength = window.history.length
    request.mockResolvedValueOnce(json(sizedShelf(5))).mockResolvedValueOnce(json(sizedShelf(3)))
    render(<StrictMode><BrowserApp initialData={sizedShelf(3)} /></StrictMode>)
    expect(screen.getAllByRole('link', { name: 'Read article' })).toHaveLength(3)
    await waitFor(() => expect(screen.getAllByRole('link', { name: 'Read article' })).toHaveLength(5))

    act(() => resize(640))
    await waitFor(() => expect(screen.getAllByRole('link', { name: 'Read article' })).toHaveLength(3))
    expect(window.history.length).toBe(originalHistoryLength)
    expect(window.location.search).toBe('?cursor=current')
    expect(request.mock.calls.map(([url, options]) => [String(url), options?.headers])).toEqual([
      [window.location.href, { Accept: mediaType, 'X-Newsfeed-Page-Size': '5' }],
      [window.location.href, { Accept: mediaType, 'X-Newsfeed-Page-Size': '3' }],
    ])
  })

  it('finishes a pending navigation with the new viewport size', async () => {
    const resize = mockViewport(640)
    const user = userEvent.setup()
    const archive = deferredPage()
    const originalHistoryLength = window.history.length
    request.mockReturnValueOnce(archive.response)
      .mockResolvedValueOnce(json({ ...sizedShelf(5), view: 'archive', page: { articles: [] } }))
    render(<BrowserApp initialData={sizedShelf(3)} />)
    expect(request).not.toHaveBeenCalled()

    await user.click(screen.getByRole('link', { name: 'Archive' }))
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    act(() => resize(641))
    expect(window.location.pathname).toBe('/')
    expect(request).toHaveBeenCalledTimes(1)
    await act(async () => archive.resolve(json({ ...sizedShelf(3), view: 'archive', page: { articles: [] } })))

    expect(await screen.findByRole('heading', { name: 'Your archive' })).toBeVisible()
    expect(window.history.length).toBe(originalHistoryLength + 1)
    expect(window.location.pathname).toBe('/archive')
    expect(request.mock.calls.map(([url, options]) => [new URL(String(url)).pathname, options?.headers])).toEqual([
      ['/archive', { Accept: mediaType, 'X-Newsfeed-Page-Size': '3' }],
      ['/archive', { Accept: mediaType, 'X-Newsfeed-Page-Size': '5' }],
    ])
  })

  it('keeps a selected listing and its reverse cursor when a resize needs retry', async () => {
    const resize = mockViewport(640)
    const user = userEvent.setup()
    const path = '/jobs/listing?from=feed&before=previous#opportunity-detail'
    const selected = { ...jobs, before: 'previous', page_size: 3, detail_selected: true }
    window.history.replaceState(null, '', path)
    const originalHistoryLength = window.history.length
    request.mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
      .mockResolvedValueOnce(json({ ...selected, page_size: 5 }))
    render(<BrowserApp initialData={selected} />)

    act(() => resize(641))
    expect(await screen.findByRole('alert')).toHaveTextContent('This page could not load.')
    expect(screen.getByRole('article', { name: 'Selected listing' })).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(screen.getByRole('status')).toBeEmptyDOMElement())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to listings' })).toHaveAttribute('href', '/jobs?before=previous#opportunity-list')
    expect(window.location.href).toBe(window.location.origin + path)
    expect(window.history.length).toBe(originalHistoryLength)
    expect(request).toHaveBeenCalledTimes(2)
  })
})

describe('browser state forms', () => {
  it('refreshes the Forward entry after a pending save on the same page', async () => {
    const path = '/articles/story?from=feed'
    window.history.replaceState(null, '', path)
    const user = userEvent.setup()
    const write = deferredPage()
    const archivedReader = { ...reader, article: { ...article, archived_at: article.updated_at } }
    request.mockReturnValueOnce(write.response).mockResolvedValueOnce(json(archivedReader))
    render(<BrowserApp initialData={reader} />)

    await user.tab()
    await user.keyboard('{Enter}')
    await waitFor(() => expect(window.location.hash).toBe('#main'))
    window.history.back()
    await waitFor(() => expect(window.location.hash).toBe(''))
    expect(request).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Archive article' }))
    window.history.forward()
    await waitFor(() => expect(window.location.hash).toBe('#main'))
    expect(request).toHaveBeenCalledOnce()
    await act(async () => write.resolve(json({ location: '/articles/story?from=archive' })))

    expect(await screen.findByRole('button', { name: 'Restore to feed' })).toBeEnabled()
    expect(window.location.pathname + window.location.search + window.location.hash).toBe(path + '#main')
    expect(request).toHaveBeenCalledTimes(2)
    expect(request).toHaveBeenLastCalledWith(new URL(path + '#main', window.location.origin), expect.objectContaining({ method: 'GET' }))
  })

  it('prevents a duplicate pending write and retries only the read after a confirmed change', async () => {
    window.history.replaceState(null, '', '/articles/story?from=feed')
    const user = userEvent.setup()
    const write = deferredPage()
    const destination = '/articles/story?from=trash'
    request.mockReturnValueOnce(write.response)
      .mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
      .mockResolvedValueOnce(json({ ...reader, from_trash: true, article: { ...article, revision: 2, trashed_at: article.updated_at } }))
    render(<BrowserApp initialData={reader} />)
    const button = screen.getByRole('button', { name: 'Move to Trash' })
    await user.click(button)
    expect(screen.getByRole('status')).toHaveTextContent('Saving change')
    expect(button).toBeDisabled()
    fireEvent.submit(button.closest('form')!)
    expect(request).toHaveBeenCalledOnce()
    const [url, init] = request.mock.calls[0]
    expect(url).toEqual(new URL('/api/v1/articles/story/trash', window.location.origin))
    expect(init).toEqual(expect.objectContaining({ method: 'POST', mode: 'same-origin', credentials: 'same-origin', redirect: 'error' }))
    expect(init?.body).toEqual(new URLSearchParams({
      csrf_token: reader.csrf, expected_revision: '1', idempotency_key: reader.mutation_key!,
    }))

    await act(async () => write.resolve(json({ location: destination })))
    const error = await screen.findByRole('alert')
    expect(error).toHaveTextContent('The change was saved. This page could not load.')
    expect(error).toHaveFocus()
    expect(screen.queryByRole('button', { name: 'Move to Trash' })).not.toBeInTheDocument()
    expect(window.location.pathname + window.location.search).toBe(destination)
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('button', { name: 'Restore to feed' })).toBeEnabled()
    expect(request.mock.calls.map(([, options]) => options?.method)).toEqual(['POST', 'GET', 'GET'])
  })

  it('keeps the requested history and retries only the read when a page fails after a pending save', async () => {
    const originalPath = '/articles/story?from=feed'
    window.history.replaceState(null, '', originalPath)
    const user = userEvent.setup()
    const write = deferredPage()
    const archivedReader = { ...reader, article: { ...article, archived_at: article.updated_at } }
    request.mockReturnValueOnce(write.response)
      .mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
      .mockResolvedValueOnce(json(jobs))
      .mockResolvedValueOnce(json(archivedReader))
      .mockResolvedValueOnce(json(jobs))
    render(<BrowserApp initialData={reader} />)

    await user.click(screen.getByRole('button', { name: 'Archive article' }))
    await user.click(screen.getByRole('link', { name: 'Jobs & Internships' }))
    expect(request).toHaveBeenCalledOnce()
    await act(async () => write.resolve(json({ location: '/articles/story?from=archive' })))

    const error = await screen.findByRole('alert')
    expect(error).toHaveTextContent('The change was saved. This page could not load.')
    expect(error).toHaveFocus()
    expect(screen.queryByRole('button', { name: 'Archive article' })).not.toBeInTheDocument()
    expect(window.location.pathname).toBe('/jobs')
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('heading', { name: 'Your next chapter.' })).toBeVisible()
    expect(request.mock.calls.map(([url, options]) => [String(url), options?.method])).toEqual([
      [window.location.origin + '/api/v1/articles/story/archive', 'POST'],
      [window.location.origin + '/jobs', 'GET'],
      [window.location.origin + '/jobs', 'GET'],
    ])

    window.history.back()
    expect(await screen.findByRole('button', { name: 'Restore to feed' })).toBeEnabled()
    expect(window.location.pathname + window.location.search).toBe(originalPath)
    window.history.forward()
    expect(await screen.findByRole('heading', { name: 'Your next chapter.' })).toBeVisible()
    expect(window.location.pathname).toBe('/jobs')
  })

  it('remembers a confirmed save when a newer page load supersedes the destination read', async () => {
    window.history.replaceState(null, '', '/articles/story?from=feed')
    const user = userEvent.setup()
    const destination = '/articles/story?from=archive'
    const destinationRead = deferredPage()
    request.mockResolvedValueOnce(json({ location: destination }))
      .mockReturnValueOnce(destinationRead.response)
      .mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
    render(<BrowserApp initialData={reader} />)

    await user.click(screen.getByRole('button', { name: 'Archive article' }))
    await waitFor(() => expect(request).toHaveBeenLastCalledWith(new URL(destination, window.location.origin), expect.objectContaining({ method: 'GET' })))
    await user.click(screen.getByRole('link', { name: 'Jobs & Internships' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The change was saved. This page could not load.')

    await act(async () => destinationRead.resolve(json({
      ...reader, from_archive: true, article: { ...article, archived_at: article.updated_at },
    })))
    expect(screen.queryByRole('heading', { name: article.title })).not.toBeInTheDocument()
    expect(window.location.pathname).toBe('/jobs')
  })

  it('keeps the view and presents a safe refresh action on a stale listing revision', async () => {
    window.history.replaceState(null, '', '/jobs/listing?from=feed#opportunity-detail')
    const user = userEvent.setup()
    const selected = { ...jobs, detail_selected: true }
    request.mockResolvedValueOnce(new Response('<script>proxy diagnostics</script>', { status: 409 }))
      .mockResolvedValueOnce(json({ ...selected, opportunity: { ...opportunity, revision: 2 } }))
    render(<BrowserApp initialData={selected} />)
    await user.click(screen.getByRole('button', { name: 'Archive listing' }))
    const detail = screen.getByRole('article', { name: 'Selected listing' })
    const error = await within(detail).findByRole('alert')
    expect(error).toHaveTextContent('This item changed.')
    expect(error).toHaveFocus()
    expect(screen.queryByText('proxy diagnostics')).not.toBeInTheDocument()
    expect(window.location.pathname).toBe('/jobs/listing')
    expect(within(detail).getByRole('heading', { name: 'Saved role' })).toBeVisible()
    expect(request.mock.calls[0][1]?.body).toEqual(new URLSearchParams({
      csrf_token: jobs.csrf, expected_revision: '1', idempotency_key: 'listing-key:archive',
    }))
    await user.click(screen.getByRole('button', { name: 'Refresh page' }))
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
    await waitFor(() => expect(screen.getByRole('button', { name: 'Archive listing' }).closest('form')
      ?.querySelector('[name="expected_revision"]')).toHaveValue('2'))
    expect(request.mock.calls.map(([, options]) => options?.method)).toEqual(['POST', 'GET'])
  })

  it('binds saves and save recovery to the source query during history traversal', async () => {
    const previous = '/jobs/listing?from=feed&cursor=first'
    const source = '/jobs/listing?from=feed&cursor=second'
    window.history.replaceState(null, '', previous)
    window.history.pushState(null, '', source)
    const user = userEvent.setup()
    const selected = { ...jobs, detail_selected: true }
    request.mockResolvedValueOnce(new Response('Conflict', { status: 409 }))
      .mockResolvedValueOnce(json({ ...selected, cursor: 'first' }))
    render(<BrowserApp initialData={{ ...selected, cursor: 'second' }} />)

    const archiveButton = screen.getByRole('button', { name: 'Archive listing' })
    await user.click(archiveButton)
    const refresh = await screen.findByRole('button', { name: 'Refresh page' })
    // The URL has already changed when popstate runs, before React handles it.
    const tryOldControls = vi.fn(() => {
      fireEvent.submit(archiveButton.closest('form')!)
      fireEvent.click(refresh)
    })
    window.addEventListener('popstate', tryOldControls, { capture: true, once: true })
    window.history.back()

    await waitFor(() => expect(screen.getByRole('link', { name: 'Back to listings' }))
      .toHaveAttribute('href', '/jobs?cursor=first#opportunity-list'))
    expect(tryOldControls).toHaveBeenCalledOnce()
    expect(screen.getByRole('alert')).toHaveTextContent('This item changed.')
    expect(screen.queryByRole('button', { name: 'Refresh page' })).not.toBeInTheDocument()
    expect(request.mock.calls.map(([url, options]) => [String(url), options?.method])).toEqual([
      [window.location.origin + '/api/v1/opportunities/listing/archive', 'POST'],
      [window.location.origin + previous, 'GET'],
    ])
  })

  it('keeps a rejected Jobs save visible after switching to News', async () => {
    window.history.replaceState(null, '', '/jobs/listing?from=feed#opportunity-detail')
    const user = userEvent.setup()
    const write = deferredPage()
    request.mockReturnValueOnce(write.response).mockResolvedValueOnce(json(shelf))
    render(<BrowserApp initialData={{ ...jobs, detail_selected: true }} />)

    await user.click(screen.getByRole('button', { name: 'Archive listing' }))
    await user.click(screen.getByRole('link', { name: 'News' }))
    expect(request).toHaveBeenCalledOnce()
    await act(async () => write.resolve(new Response('Conflict', { status: 409 })))

    expect(await screen.findByRole('heading', { name: 'Your reading shelf' })).toBeVisible()
    const error = within(screen.getByRole('main')).getByRole('alert')
    expect(error).toHaveTextContent('The change was rejected. This item changed.')
    expect(screen.getByRole('main')).toHaveFocus()
    expect(within(error).queryByRole('button', { name: 'Refresh page' })).not.toBeInTheDocument()
    await user.click(within(error).getByRole('button', { name: 'Dismiss save error' }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(window.location.pathname + window.location.search + window.location.hash).toBe('/')
    expect(request.mock.calls.map(([url, options]) => [String(url), options?.method])).toEqual([
      [window.location.origin + '/api/v1/opportunities/listing/archive', 'POST'],
      [window.location.origin + '/', 'GET'],
    ])
  })

  it('places an outstanding save rejection inside the Jobs list and detail fragments', async () => {
    window.history.replaceState(null, '', '/jobs/listing?from=feed#opportunity-detail')
    const user = userEvent.setup()
    const write = deferredPage()
    const second = { ...opportunity, id: 'second-listing', title: 'Another role' }
    const list = { ...jobs, opportunities_page: { opportunities: [opportunity, second] } }
    request.mockReturnValueOnce(write.response).mockResolvedValueOnce(json(list))
      .mockResolvedValueOnce(json({ ...list, opportunity: second, detail_selected: true }))
    render(<BrowserApp initialData={{ ...list, detail_selected: true }} />)

    await user.click(screen.getByRole('button', { name: 'Archive listing' }))
    await user.click(screen.getByRole('link', { name: 'Back to listings' }))
    await act(async () => write.resolve(new Response('Conflict', { status: 409 })))

    const listRegion = screen.getByRole('region', { name: 'Saved listings' })
    await waitFor(() => expect(listRegion).toHaveFocus())
    expect(within(listRegion).getByRole('alert')).toHaveTextContent('This item changed.')
    await user.click(within(listRegion).getByRole('link', { name: /View Another role/ }))

    const detail = screen.getByRole('article', { name: 'Selected listing' })
    await waitFor(() => expect(detail).toHaveFocus())
    expect(within(detail).getByRole('heading', { name: 'Another role' })).toBeVisible()
    const error = within(detail).getByRole('alert')
    expect(error).toHaveTextContent('This item changed.')
    expect(within(error).queryByRole('button', { name: 'Refresh page' })).not.toBeInTheDocument()
    await user.tab()
    expect(within(error).getByRole('button', { name: 'Dismiss save error' })).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(window.location.pathname + window.location.search + window.location.hash).toBe(
      '/jobs/second-listing?from=feed#opportunity-detail',
    )
    expect(request.mock.calls.map(([, options]) => options?.method)).toEqual(['POST', 'GET', 'GET'])
  })

  it('keeps unconfirmed save feedback when the requested page fails and is retried', async () => {
    window.history.replaceState(null, '', '/jobs/listing?from=feed#opportunity-detail')
    const user = userEvent.setup()
    const write = deferredPage()
    request.mockReturnValueOnce(write.response)
      .mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
      .mockResolvedValueOnce(json(shelf))
    render(<BrowserApp initialData={{ ...jobs, detail_selected: true }} />)

    await user.click(screen.getByRole('button', { name: 'Archive listing' }))
    await user.click(screen.getByRole('link', { name: 'News' }))
    await act(async () => write.resolve(new Response('Unavailable', { status: 503 })))

    const saveError = screen.getByText('The change could not be confirmed.')
    const retry = screen.getByRole('button', { name: 'Try again' })
    expect(saveError).toBeVisible()
    expect(retry.closest('[role="alert"]')).toHaveTextContent('This page could not load.')
    expect(screen.queryByText(/The change was saved/)).not.toBeInTheDocument()
    await user.click(retry)

    expect(await screen.findByRole('heading', { name: 'Your reading shelf' })).toBeVisible()
    expect(screen.getByRole('alert')).toHaveTextContent('The change could not be confirmed.')
    expect(request.mock.calls.map(([url, options]) => [String(url), options?.method])).toEqual([
      [window.location.origin + '/api/v1/opportunities/listing/archive', 'POST'],
      [window.location.origin + '/', 'GET'],
      [window.location.origin + '/', 'GET'],
    ])
  })

  it('clears private content and loads the sign-in gate when a write session expires', async () => {
    window.history.replaceState(null, '', '/articles/story?from=feed')
    const user = userEvent.setup()
    const signInURL = '/auth/login?return_to=%2Farticles%2Fstory%3Ffrom%3Dfeed'
    request.mockResolvedValueOnce(new Response('Sign in to continue.', { status: 401 }))
      .mockResolvedValueOnce(json({
        configured: true, authenticated: false, can_write: false, csrf: '', section: 'news',
        view: 'reader', from_archive: false, sign_in_url: signInURL,
      }))
    render(<BrowserApp initialData={reader} />)
    await user.click(screen.getByRole('button', { name: 'Archive article' }))
    expect(await screen.findByRole('link', { name: 'Sign in' })).toHaveAttribute('href', signInURL)
    expect(screen.queryByRole('heading', { name: article.title })).not.toBeInTheDocument()
    expect(window.location.pathname).toBe('/articles/story')
  })

  it('clears private content after an expired write session even if the requested page fails', async () => {
    window.history.replaceState(null, '', '/articles/story?from=feed')
    const user = userEvent.setup()
    const write = deferredPage()
    request.mockReturnValueOnce(write.response).mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
    render(<BrowserApp initialData={reader} />)

    await user.click(screen.getByRole('button', { name: 'Archive article' }))
    await user.click(screen.getByRole('link', { name: 'Jobs & Internships' }))
    await act(async () => write.resolve(new Response('Sign in to continue.', { status: 401 })))
    const error = await screen.findByRole('alert')
    expect(error).toHaveTextContent('This page could not load.')
    expect(error).not.toHaveTextContent('The change was saved.')
    expect(screen.queryByRole('heading', { name: article.title })).not.toBeInTheDocument()
  })
})

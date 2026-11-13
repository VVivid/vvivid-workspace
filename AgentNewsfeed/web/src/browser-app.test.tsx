import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { BrowserApp } from '@/browser-app'
import { pageSizeStorageKey } from '@/pagination'
import type { Article, Bootstrap } from '@/types'

const mediaType = 'application/vnd.agentnewsfeed.browser+json'
const article: Article = {
  id: 'story', url: 'https://source.example/story', title: 'Saved story', summary: 'Saved introduction',
  revision: 1, created_at: 1793145600000, updated_at: 1793145600000,
}
const shelf: Bootstrap = {
  configured: true, authenticated: true, can_write: true, csrf: 'synthetic-csrf',
  view: 'feed', from_archive: false, sign_in_url: '/auth/login?return_to=%2F',
  section: 'news', page: { articles: [article] },
}
const reader: Bootstrap = { ...shelf, page: undefined, view: 'reader', article, mutation_key: 'synthetic-key' }
const archivedArticle = { ...article, archived_at: article.updated_at }
const archive: Bootstrap = { ...shelf, view: 'archive', page: { articles: [archivedArticle] } }

function json(data: unknown) {
  return new Response(JSON.stringify(data), { headers: { 'Content-Type': mediaType } })
}

function deferredPage() {
  let resolve!: (value: Response) => void
  const response = new Promise<Response>(settle => { resolve = settle })
  return { response, resolve }
}

function sizedShelf(size: number): Bootstrap {
  const articles = Array.from({ length: size }, (_, index) => ({ ...article, id: `${article.id}-${index}` }))
  return { ...shelf, page_size: size, page: { articles } }
}

const request = vi.fn<typeof fetch>()

beforeEach(() => {
  window.history.replaceState(null, '', '/')
  localStorage.removeItem(pageSizeStorageKey)
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
  it('loads an Archive cursor page and reader, then follows Back and Forward in the same document', async () => {
    const user = userEvent.setup()
    const secondPage = { ...archive, cursor: 'second-page' }
    const archivedReader = { ...reader, cursor: secondPage.cursor, from_archive: true, article: archivedArticle }
    request.mockResolvedValueOnce(json({ ...archive, page: { articles: [archivedArticle], next_cursor: secondPage.cursor } }))
      .mockResolvedValueOnce(json(secondPage))
      .mockResolvedValueOnce(json(archivedReader))
      .mockResolvedValueOnce(json(secondPage))
      .mockResolvedValueOnce(json(archivedReader))
    render(<BrowserApp initialData={shelf} />)
    const home = screen.getByRole('link', { name: 'Newsfeed home' })

    await user.click(screen.getByRole('link', { name: 'Archive' }))
    expect(await screen.findByRole('heading', { name: 'Your archive' })).toBeVisible()
    expect(screen.getByRole('main')).toHaveFocus()
    expect(window.scrollTo).toHaveBeenCalledExactlyOnceWith(0, 0)
    expect(request).toHaveBeenCalledWith(new URL('/archive', window.location.origin), expect.objectContaining({
      method: 'GET', mode: 'same-origin', credentials: 'same-origin', cache: 'no-store', redirect: 'error',
      headers: { Accept: mediaType, 'X-Newsfeed-Page-Size': '10' },
    }))
    await user.click(screen.getByRole('link', { name: 'Next' }))
    await waitFor(() => expect(window.location.search).toBe('?cursor=second-page'))
    await user.click(screen.getByRole('link', { name: `Read saved article: ${article.title}` }))
    expect(await screen.findByRole('heading', { name: article.title, level: 1 })).toBeVisible()
    expect(screen.getByRole('main')).toHaveFocus()
    expect(window.scrollTo).toHaveBeenCalledTimes(2)
    expect(window.location.pathname + window.location.search).toBe('/articles/story?from=archive&cursor=second-page')
    expect(screen.getByRole('link', { name: 'Back to archive' })).toHaveAttribute('href', '/archive?cursor=second-page')

    window.history.back()
    expect(await screen.findByRole('heading', { name: 'Your archive' })).toBeVisible()
    expect(screen.getByRole('main')).toHaveFocus()
    expect(window.location.pathname + window.location.search).toBe('/archive?cursor=second-page')
    window.history.forward()
    expect(await screen.findByRole('heading', { name: article.title, level: 1 })).toBeVisible()
    expect(screen.getByRole('main')).toHaveFocus()
    expect(screen.getByRole('link', { name: 'Newsfeed home' })).toBe(home)
  })

  it.each(([
    { path: '/', view: 'feed' }, { path: '/archive', view: 'archive' }, { path: '/trash', view: 'trash' },
  ] as const).flatMap(shelf => ['Next', 'Previous'].map(direction => ({ ...shelf, direction }))))(
    'keeps the $view viewport while loading $direction and focuses the loaded list',
    async ({ path, view, direction }) => {
      window.history.replaceState(null, '', `${path}?cursor=current`)
      const user = userEvent.setup()
      const pending = deferredPage()
      const initialData = {
        ...shelf, view, page: {
          articles: [{
            ...article,
            archived_at: view === 'archive' ? article.updated_at : undefined,
            trashed_at: view === 'trash' ? article.updated_at : undefined,
          }],
          next_cursor: 'next', previous_cursor: 'previous',
        },
      }
      const position = direction === 'Next' ? { cursor: 'next' } : { before: 'previous' }
      request.mockReturnValueOnce(pending.response)
      const { container } = render(<BrowserApp initialData={initialData} />)
      const main = screen.getByRole('main')

      await user.click(screen.getByRole('link', { name: direction }))
      expect(screen.getByRole('status')).toHaveTextContent('Loading page')
      expect(screen.getByRole('status')).toHaveClass('sr-only')
      expect(container.querySelector('.navigation-progress')).toHaveAttribute('aria-hidden', 'true')
      expect(main.closest('[aria-busy]')).toHaveAttribute('aria-busy', 'true')
      expect(screen.getByRole('main')).toBe(main)
      expect(window.location.search).toBe('?cursor=current')
      expect(window.scrollTo).not.toHaveBeenCalled()

      await act(async () => pending.resolve(json({ ...initialData, ...position })))
      expect(window.location.pathname + window.location.search).toBe(`${path}?${direction === 'Next' ? 'cursor=next' : 'before=previous'}`)
      expect(screen.getByRole('main')).toHaveFocus()
      expect(window.scrollTo).not.toHaveBeenCalled()
      expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled()
      expect(container.querySelector('.navigation-progress')).not.toBeInTheDocument()
      expect(screen.getByRole('status')).toBeEmptyDOMElement()
    },
  )

  it('retains a failed cursor request and its viewport behavior when retrying', async () => {
    const user = userEvent.setup()
    request.mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
      .mockResolvedValueOnce(json({ ...shelf, cursor: 'next' }))
    render(<BrowserApp initialData={{ ...shelf, page: { articles: [article], next_cursor: 'next' } }} />)

    await user.click(screen.getByRole('link', { name: 'Next' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('This page could not load.')
    expect(screen.getByRole('alert')).toHaveFocus()
    expect(screen.getByRole('heading', { name: article.title })).toBeVisible()
    expect(window.location.search).toBe('')

    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(window.location.search).toBe('?cursor=next'))
    expect(screen.getByRole('main')).toHaveFocus()
    expect(window.scrollTo).not.toHaveBeenCalled()
    expect(request).toHaveBeenCalledTimes(2)
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

  it('drops the old reader when a newer load fails after Back changes the address', async () => {
    const user = userEvent.setup()
    const backRead = deferredPage()
    request.mockResolvedValueOnce(json(reader)).mockReturnValueOnce(backRead.response)
      .mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
      .mockResolvedValueOnce(json(archive))
    render(<BrowserApp initialData={shelf} />)

    await user.click(screen.getByRole('link', { name: `Read saved article: ${article.title}` }))
    const archiveButton = await screen.findByRole('button', { name: 'Archive article' })
    const archiveLink = screen.getByRole('link', { name: 'Archive' })
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
    expect(window.location.pathname).toBe('/archive')
    expect(request.mock.calls.map(([url, options]) => [new URL(String(url)).pathname, options?.method])).toEqual([
      ['/articles/story', 'GET'], ['/', 'GET'], ['/archive', 'GET'], ['/archive', 'GET'],
    ])
  })

  it('keeps fragment history when Back supersedes a pending page request', async () => {
    const user = userEvent.setup()
    const pendingArchive = deferredPage()
    request.mockReturnValueOnce(pendingArchive.response).mockResolvedValueOnce(json(shelf))
    render(<BrowserApp initialData={shelf} />)

    await user.tab()
    expect(screen.getByRole('link', { name: 'Skip to reading' })).toHaveFocus()
    await user.keyboard('{Enter}')
    await waitFor(() => expect(window.location.hash).toBe('#main'))
    expect(request).not.toHaveBeenCalled()

    await user.click(screen.getByRole('link', { name: 'Archive' }))
    expect(screen.getByRole('status')).toHaveTextContent('Loading page')
    window.history.back()
    await waitFor(() => expect(window.location.hash).toBe(''))
    await act(async () => pendingArchive.resolve(json(archive)))
    expect(window.location.pathname).toBe('/')
    expect(screen.getByRole('heading', { name: 'Your reading shelf' })).toBeVisible()
    expect(screen.getByRole('status')).toBeEmptyDOMElement()

    window.history.forward()
    await waitFor(() => expect(window.location.hash).toBe('#main'))
    expect(request.mock.calls.map(([url]) => String(url))).toEqual([
      window.location.origin + '/archive', window.location.origin + '/',
    ])
  })

  it.each([
    { link: 'Feed', view: 'feed' }, { link: 'Trash', view: 'trash' },
  ])('keeps $link compact without requesting thumbnail images', async ({ link, view }) => {
    const user = userEvent.setup()
    const thumbnailArticle = { ...article, thumbnail_url: 'https://source.example/thumbnail.jpg' }
    const initialData = { ...shelf, page: { articles: [thumbnailArticle] } }
    const loadedArticle = view === 'trash' ? { ...thumbnailArticle, trashed_at: article.updated_at } : thumbnailArticle
    request.mockResolvedValueOnce(json({ ...initialData, view, page: { articles: [loadedArticle] } }))
    const { container } = render(<BrowserApp initialData={initialData} />)
    expect(container.querySelector('img')).not.toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: link }))
    await waitFor(() => expect(screen.getByRole('link', { name: link })).toHaveAttribute('aria-current', 'page'))
    expect(container.querySelector('img')).not.toBeInTheDocument()
  })

  it('offers a way back to the feed when an earlier detail is no longer readable', async () => {
    window.history.replaceState(null, '', '/articles/story?from=trash')
    const user = userEvent.setup()
    const trashed = { ...reader, from_trash: true, article: { ...article, trashed_at: article.updated_at } }
    request.mockResolvedValueOnce(json(archive)).mockResolvedValueOnce(new Response('Unavailable', { status: 404 }))
      .mockResolvedValueOnce(json(shelf))
    render(<BrowserApp initialData={trashed} />)
    await user.click(screen.getByRole('link', { name: 'Archive' }))
    expect(await screen.findByRole('heading', { name: 'Your archive' })).toBeVisible()
    window.history.back()
    expect(await screen.findByRole('alert')).toHaveTextContent('unavailable in the requested view')
    expect(screen.queryByRole('heading', { name: 'Your archive' })).not.toBeInTheDocument()
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

describe('article search and page size', () => {
  it('restores the saved size on reload without a history entry or scrolling', async () => {
    localStorage.setItem(pageSizeStorageKey, '100')
    const originalHistoryLength = window.history.length
    request.mockResolvedValueOnce(json(sizedShelf(100)))
    render(<StrictMode><BrowserApp initialData={sizedShelf(10)} /></StrictMode>)
    await waitFor(() => expect(screen.getAllByRole('link', { name: /^Read saved article:/ })).toHaveLength(100))
    expect(request).toHaveBeenCalledExactlyOnceWith(new URL('/', window.location.origin), expect.objectContaining({
      headers: { Accept: mediaType, 'X-Newsfeed-Page-Size': '100' },
    }))
    expect(window.history.length).toBe(originalHistoryLength)
    expect(window.scrollTo).not.toHaveBeenCalled()
    expect(screen.getByRole('combobox', { name: 'Articles per page' })).toHaveValue('100')
  })

  it('honors a deep-linked size over the device preference and ignores viewport changes', () => {
    localStorage.setItem(pageSizeStorageKey, '100')
    window.history.replaceState(null, '', '/?q=saved&page_size=50')
    render(<BrowserApp initialData={{ ...sizedShelf(50), query: 'saved' }} />)
    window.dispatchEvent(new Event('resize'))
    expect(request).not.toHaveBeenCalled()
    expect(screen.getByRole('combobox', { name: 'Articles per page' })).toHaveValue('50')
    expect(screen.getByRole('searchbox', { name: 'Search saved news' })).toHaveValue('saved')
  })

  it('resets paging when a preset changes and preserves the search and viewport', async () => {
    window.history.replaceState(null, '', '/?q=saved&page_size=10&cursor=current')
    const user = userEvent.setup()
    request.mockResolvedValueOnce(json({ ...sizedShelf(50), query: 'saved' }))
    render(<BrowserApp initialData={{ ...sizedShelf(10), query: 'saved', cursor: 'current' }} />)
    await user.selectOptions(screen.getByRole('combobox', { name: 'Articles per page' }), '50')
    await waitFor(() => expect(window.location.search).toBe('?q=saved&page_size=50'))
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('50')
    expect(window.scrollTo).not.toHaveBeenCalled()
    expect(screen.getByRole('combobox', { name: 'Articles per page' })).toHaveFocus()
    expect(screen.getByRole('link', { name: 'Archive' })).toHaveAttribute('href', '/archive?q=saved&page_size=50')
  })

  it.each([
    { name: 'FEFF', query: '  Kernel\uFEFFsource  ', normalizedQuery: 'kernel\uFEFFsource', position: 'cursor' },
    { name: 'NEL', query: '  Kernel\u0085source  ', normalizedQuery: 'kernel source', position: 'before' },
    { name: 'repeated spaces', query: '  Kernel   source  ', normalizedQuery: 'kernel source', position: 'cursor' },
  ])('sends raw typed $name query for server normalization and resets paging', async ({ query, normalizedQuery, position }) => {
    window.history.replaceState(null, '', `/?q=saved&page_size=20&${position}=current`)
    const user = userEvent.setup()
    request.mockResolvedValueOnce(json({ ...shelf, page_size: 20, query: normalizedQuery }))
    render(<BrowserApp initialData={{ ...shelf, page_size: 20, query: 'saved', [position]: 'current' }} />)
    const input = screen.getByRole('searchbox', { name: 'Search saved news' })

    await user.clear(input)
    await user.type(input, query)
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    const url = new URL(String(request.mock.calls[0][0]))
    expect(url.searchParams.get('q')).toBe(query)
    expect(url.searchParams.get('page_size')).toBe('20')
    expect(url.searchParams.has('cursor')).toBe(false)
    expect(url.searchParams.has('before')).toBe(false)
    expect(request.mock.calls[0][1]?.headers).toEqual({ Accept: mediaType, 'X-Newsfeed-Page-Size': '20' })
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('q')).toBe(query))
    expect(input).toHaveValue(query)
    expect(input).toHaveFocus()
    expect(screen.getByRole('combobox', { name: 'Articles per page' })).toHaveValue('20')
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('20')
    expect(window.scrollTo).not.toHaveBeenCalled()
  })

  it('validates custom sizes, cancels without a request, and remembers 500', async () => {
    const user = userEvent.setup()
    render(<BrowserApp initialData={sizedShelf(10)} />)
    const select = screen.getByRole('combobox', { name: 'Articles per page' })
    await user.selectOptions(select, 'custom')
    let input = screen.getByRole('spinbutton', { name: /Custom page size/ })
    expect(input).toHaveFocus()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(select).toHaveFocus()
    expect(request).not.toHaveBeenCalled()
    expect(select).toHaveValue('10')
    await user.selectOptions(select, 'custom')
    input = screen.getByRole('spinbutton', { name: /Custom page size/ })
    for (const value of ['', '0', '501', '12.5']) {
      fireEvent.change(input, { target: { value } })
      await user.click(screen.getByRole('button', { name: 'Apply' }))
      expect(screen.getByRole('alert')).toHaveTextContent('Enter a whole number from 1 to 500.')
      expect(input).toHaveAttribute('aria-invalid', 'true')
    }
    expect(request).not.toHaveBeenCalled()
    request.mockResolvedValueOnce(json({ ...shelf, page_size: 500 }))
    fireEvent.change(input, { target: { value: '500' } })
    await user.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(window.location.search).toBe('?page_size=500'))
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('500')
    expect(select).toHaveDisplayValue('Custom (500)')
    expect(select).toHaveFocus()
  })

  it('keeps a newly selected size when typing while that page is still loading', async () => {
    const user = userEvent.setup()
    const first = deferredPage()
    request.mockReturnValueOnce(first.response).mockResolvedValueOnce(json({ ...shelf, page_size: 50, query: 'source' }))
    render(<BrowserApp initialData={sizedShelf(10)} />)
    const select = screen.getByRole('combobox', { name: 'Articles per page' })
    await user.selectOptions(select, '50')
    expect(select).toHaveValue('50')
    await user.type(screen.getByRole('searchbox', { name: 'Search saved news' }), 'source')
    await waitFor(() => expect(request).toHaveBeenCalledTimes(2))
    const second = new URL(String(request.mock.calls[1][0]))
    expect(second.searchParams.get('page_size')).toBe('50')
    await act(async () => first.resolve(json(sizedShelf(50))))
    expect(select).toHaveValue('50')
    expect(screen.getByRole('searchbox', { name: 'Search saved news' })).toHaveValue('source')
  })

  it('searches literal punctuation, ignores obsolete responses, and keeps typing focus through empty and clear results', async () => {
    const user = userEvent.setup()
    const first = deferredPage()
    const second = deferredPage()
    request.mockReturnValueOnce(first.response).mockReturnValueOnce(second.response)
    render(<BrowserApp initialData={{ ...sizedShelf(10), cursor: 'old' }} />)
    const input = screen.getByRole('searchbox', { name: 'Search saved news' })
    await user.click(input)
    fireEvent.change(input, { target: { value: 'C++ [50%]' } })
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1))
    await user.type(input, ' source')
    await waitFor(() => expect(request).toHaveBeenCalledTimes(2))
    await act(async () => second.resolve(json({ ...shelf, page_size: 10, query: 'c++ [50%] source', page: { articles: [], total: 0, start: 0, end: 0 } })))
    expect(screen.getByRole('heading', { name: 'No matching articles' })).toBeVisible()
    expect(screen.getByRole('link', { name: 'Clear search' })).toHaveAttribute('href', '/?page_size=10')
    expect(input).toHaveValue('C++ [50%] source')
    expect(input).toHaveFocus()
    const query = new URLSearchParams(window.location.search)
    expect(query.get('q')).toBe('C++ [50%] source')
    expect(query.has('cursor')).toBe(false)
    await act(async () => first.resolve(json({ ...sizedShelf(10), query: 'c++ [50%]' })))
    expect(screen.getByRole('heading', { name: 'No matching articles' })).toBeVisible()
    request.mockResolvedValueOnce(json(sizedShelf(10)))
    await user.keyboard('{Escape}')
    await waitFor(() => expect(window.location.search).toBe('?page_size=10'))
    expect(input).toHaveValue('')
    expect(input).toHaveFocus()
    expect(window.scrollTo).not.toHaveBeenCalled()
  })

  it('keeps query and size through reader navigation, a state action and Back', async () => {
    const user = userEvent.setup()
    const context = '?q=saved&page_size=20&cursor=current'
    window.history.replaceState(null, '', '/' + context)
    const current = { ...shelf, page_size: 20, query: 'saved', cursor: 'current' }
    const savedReader = { ...reader, page_size: 20, query: 'saved', cursor: 'current' }
    const readerPath = '/articles/story?from=feed&q=saved&page_size=20&cursor=current'
    request.mockResolvedValueOnce(json(savedReader))
      .mockResolvedValueOnce(json({ location: readerPath }))
      .mockResolvedValueOnce(json({ ...savedReader, article: { ...article, archived_at: article.updated_at } }))
      .mockResolvedValueOnce(json(current))
    render(<BrowserApp initialData={current} />)
    await user.click(screen.getByRole('link', { name: `Read saved article: ${article.title}` }))
    await waitFor(() => expect(window.location.pathname).toBe('/articles/story'))
    expect(screen.getByRole('link', { name: 'Back to feed' })).toHaveAttribute('href', '/' + context)
    await user.click(screen.getByRole('button', { name: 'Archive article' }))
    expect(await screen.findByText(/Archived .*Kept here for another day/)).toBeVisible()
    expect(screen.getByRole('link', { name: 'Back to feed' })).toHaveAttribute('href', '/' + context)
    await user.click(screen.getByRole('link', { name: 'Back to feed' }))
    expect(await screen.findByRole('searchbox', { name: 'Search saved news' })).toHaveValue('saved')
    expect(window.location.search).toBe(context)
  })
})

describe('pending search navigation', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    localStorage.setItem(pageSizeStorageKey, '100')
    window.history.replaceState(null, '', '/?page_size=10')
  })

  afterEach(() => vi.useRealTimers())

  it.each<{ name: string; path: string; heading: string; data: Bootstrap }>([
    { name: 'Archive', path: '/archive?page_size=10', heading: 'Your archive', data: { ...archive, page_size: 10 } },
    { name: `Read saved article: ${article.title}`, path: '/articles/story?from=feed&page_size=10', heading: article.title, data: { ...reader, page_size: 10 } },
    { name: 'To-do', path: '/todo', heading: 'To-do', data: { ...shelf, section: 'todo', page: undefined, todo: { tasks: [] } } },
    { name: 'Next', path: '/?page_size=10&cursor=next', heading: 'Your reading shelf', data: { ...shelf, page_size: 10, cursor: 'next' } },
  ])('cancels an unsent search when $name starts a slow navigation', async ({ name, path, heading, data }) => {
    const destination = deferredPage()
    const push = vi.spyOn(window.history, 'pushState')
    request.mockReturnValueOnce(destination.response).mockResolvedValue(json({ ...shelf, page_size: 10, query: 'kernel' }))
    render(<BrowserApp initialData={{ ...shelf, page_size: 10, page: { articles: [article], next_cursor: 'next' } }} />)

    fireEvent.change(screen.getByRole('searchbox', { name: 'Search saved news' }), { target: { value: 'kernel' } })
    expect(request).not.toHaveBeenCalled()
    await act(async () => { fireEvent.click(screen.getByRole('link', { name })) })
    expect(request).toHaveBeenCalledExactlyOnceWith(new URL(path, window.location.origin), expect.objectContaining({ method: 'GET' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    expect(request).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('100')
    expect(push).not.toHaveBeenCalled()

    await act(async () => destination.resolve(json(data)))
    expect(window.location.pathname + window.location.search).toBe(path)
    expect(screen.getByRole('heading', { name: heading, level: 1 })).toBeVisible()
    expect(push).toHaveBeenCalledTimes(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    expect(request).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('100')
    expect(push).toHaveBeenCalledTimes(1)
  })

  it('lets a slow history destination cancel the old search without pushing or changing the size preference', async () => {
    const destination = deferredPage()
    const push = vi.spyOn(window.history, 'pushState')
    request.mockReturnValueOnce(destination.response).mockResolvedValue(json({ ...archive, page_size: 10, query: 'kernel' }))
    render(<BrowserApp initialData={{ ...shelf, page_size: 10 }} />)
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search saved news' }), { target: { value: 'kernel' } })

    await act(async () => {
      window.history.replaceState(null, '', '/archive?page_size=20')
      window.dispatchEvent(new PopStateEvent('popstate'))
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    expect(request).toHaveBeenCalledExactlyOnceWith(new URL('/archive?page_size=20', window.location.origin), expect.objectContaining({ method: 'GET' }))
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('100')

    await act(async () => destination.resolve(json({ ...archive, page_size: 20 })))
    expect(screen.getByRole('heading', { name: 'Your archive' })).toBeVisible()
    expect(window.location.pathname + window.location.search).toBe('/archive?page_size=20')
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    expect(request).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('100')
    expect(push).not.toHaveBeenCalled()
  })

  it('cancels an unsent search during idle fragment history without fetching a page', async () => {
    render(<BrowserApp initialData={{ ...shelf, page_size: 10 }} />)
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search saved news' }), { target: { value: 'kernel' } })
    await act(async () => {
      window.history.replaceState(null, '', '/?page_size=10#main')
      window.dispatchEvent(new PopStateEvent('popstate'))
      await vi.advanceTimersByTimeAsync(300)
    })
    expect(request).not.toHaveBeenCalled()
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('100')
    expect(window.location.search + window.location.hash).toBe('?page_size=10#main')
  })

  it('cancels an unsent search when a state action waits for its write before refreshing', async () => {
    const write = deferredPage()
    request.mockReturnValueOnce(write.response).mockResolvedValueOnce(json({ ...shelf, page_size: 10, page: { articles: [] } }))
    render(<BrowserApp initialData={{ ...shelf, page_size: 10 }} />)
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search saved news' }), { target: { value: 'kernel' } })
    fireEvent.click(screen.getByRole('button', { name: `Archive ${article.title}` }))

    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    expect(request).toHaveBeenCalledTimes(1)
    expect(request.mock.calls[0][1]?.method).toBe('POST')
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('100')
    expect(window.location.search).toBe('?page_size=10')

    await act(async () => write.resolve(json({ location: '/?page_size=10' })))
    expect(request).toHaveBeenCalledTimes(2)
    expect(request.mock.calls[1][0]).toEqual(new URL('/?page_size=10', window.location.origin))
    expect(request.mock.calls[1][1]?.method).toBe('GET')
    expect(screen.getByRole('heading', { name: 'A little space for a good story.' })).toBeVisible()
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    expect(request).toHaveBeenCalledTimes(2)
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('100')
    expect(window.location.search).toBe('?page_size=10')
  })

  it('keeps the latest typing timer through completed search reads and ignores older in-flight results', async () => {
    const first = deferredPage()
    const second = deferredPage()
    const third = deferredPage()
    request.mockReturnValueOnce(first.response).mockReturnValueOnce(second.response).mockReturnValueOnce(third.response)
    render(<BrowserApp initialData={{ ...shelf, page_size: 10 }} />)
    const input = screen.getByRole('searchbox', { name: 'Search saved news' })
    input.focus()
    fireEvent.change(input, { target: { value: 'kernel' } })
    await act(async () => { await vi.advanceTimersByTimeAsync(249) })
    expect(request).not.toHaveBeenCalled()
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    expect(request).toHaveBeenCalledTimes(1)

    fireEvent.change(input, { target: { value: 'kernel cache' } })
    await act(async () => { await vi.advanceTimersByTimeAsync(125) })
    fireEvent.change(input, { target: { value: 'kernel scheduler' } })
    await act(async () => { await vi.advanceTimersByTimeAsync(249) })
    expect(request).toHaveBeenCalledTimes(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    expect(request).toHaveBeenCalledTimes(2)

    fireEvent.change(input, { target: { value: 'kernel scheduler source' } })
    await act(async () => { await vi.advanceTimersByTimeAsync(100) })
    await act(async () => second.resolve(json({ ...shelf, page_size: 10, query: 'kernel scheduler' })))
    expect(input).toHaveValue('kernel scheduler source')
    expect(input).toHaveFocus()
    await act(async () => { await vi.advanceTimersByTimeAsync(150) })
    expect(request).toHaveBeenCalledTimes(3)
    await act(async () => third.resolve(json({ ...shelf, page_size: 10, query: 'kernel scheduler source' })))
    await act(async () => first.resolve(json({ ...shelf, page_size: 10, query: 'kernel' })))

    expect(request.mock.calls.map(([url]) => new URL(String(url)).searchParams.get('q'))).toEqual(['kernel', 'kernel scheduler', 'kernel scheduler source'])
    for (const [, options] of request.mock.calls) expect(options).not.toHaveProperty('signal')
    expect(new URLSearchParams(window.location.search).get('q')).toBe('kernel scheduler source')
    expect(input).toHaveValue('kernel scheduler source')
    expect(input).toHaveFocus()
    expect(window.scrollTo).not.toHaveBeenCalled()
    expect(screen.getByRole('status')).toBeEmptyDOMElement()
  })

  it.each(['submit', 'clear', 'custom size'] as const)('applies %s immediately and cancels the pending typing timer', async action => {
    const query = action === 'clear' ? '' : 'kernel'
    const size = action === 'custom size' ? 7 : 10
    request.mockResolvedValueOnce(json({ ...shelf, page_size: size, query }))
    render(<BrowserApp initialData={{ ...shelf, page_size: 10 }} />)
    const input = screen.getByRole('searchbox', { name: 'Search saved news' })
    fireEvent.change(input, { target: { value: 'kernel' } })
    await act(async () => {
      if (action === 'submit') fireEvent.submit(input.closest('form')!)
      else if (action === 'clear') fireEvent.click(screen.getByRole('button', { name: 'Clear' }))
      else {
        fireEvent.change(screen.getByRole('combobox', { name: 'Articles per page' }), { target: { value: 'custom' } })
      }
    })
    if (action === 'custom size') {
      fireEvent.change(screen.getByRole('spinbutton', { name: /Custom page size/ }), { target: { value: '7' } })
      await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Apply' })) })
    }
    expect(request).toHaveBeenCalledTimes(1)
    const url = new URL(String(request.mock.calls[0][0]))
    expect(url.searchParams.get('q') ?? '').toBe(query)
    expect(url.searchParams.get('page_size')).toBe(String(size))
    expect(localStorage.getItem(pageSizeStorageKey)).toBe(String(size))
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    expect(request).toHaveBeenCalledTimes(1)
    expect(window.location.search).toBe(url.search)
  })

  it('discards an unsent search when the browser app unmounts', async () => {
    const { unmount } = render(<BrowserApp initialData={{ ...shelf, page_size: 10 }} />)
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search saved news' }), { target: { value: 'kernel' } })
    unmount()
    await act(async () => { await vi.advanceTimersByTimeAsync(300) })
    expect(request).not.toHaveBeenCalled()
    expect(localStorage.getItem(pageSizeStorageKey)).toBe('100')
    expect(window.location.search).toBe('?page_size=10')
  })

  describe('search draft ownership', () => {
    it('restores the loaded size when Next supersedes a pending size choice with unchanged loaded props', async () => {
      const preset = deferredPage()
      const destination = deferredPage()
      const search = deferredPage()
      request.mockReturnValueOnce(preset.response).mockReturnValueOnce(destination.response).mockReturnValueOnce(search.response)
      render(<BrowserApp initialData={{ ...shelf, page_size: 10, page: { articles: [article], next_cursor: 'next' } }} />)
      const select = screen.getByRole('combobox', { name: 'Articles per page' })
      await act(async () => { fireEvent.change(select, { target: { value: '100' } }) })
      expect(select).toHaveValue('100')
      await act(async () => { fireEvent.click(screen.getByRole('link', { name: 'Next' })) })
      await act(async () => { await vi.advanceTimersByTimeAsync(400) })
      await act(async () => destination.resolve(json({ ...shelf, page_size: 10, cursor: 'next' })))

      expect(select).toHaveValue('10')
      const input = screen.getByRole('searchbox', { name: 'Search saved news' })
      fireEvent.change(input, { target: { value: 'kernel' } })
      await act(async () => { await vi.advanceTimersByTimeAsync(250) })
      expect(request).toHaveBeenCalledTimes(3)
      const searchURL = new URL(String(request.mock.calls[2][0]))
      expect(searchURL.searchParams.get('page_size')).toBe('10')
      expect(searchURL.searchParams.get('q')).toBe('kernel')
      await act(async () => search.resolve(json({ ...shelf, page_size: 10, query: 'kernel' })))
      await act(async () => preset.resolve(json({ ...shelf, page_size: 100 })))
      expect(select).toHaveValue('10')
      expect(input).toHaveValue('kernel')
      expect(window.location.search).toBe(searchURL.search)
      for (const [, options] of request.mock.calls) expect(options).not.toHaveProperty('signal')
    })

    it('keeps a genuinely newer size choice when older size and Next responses finish', async () => {
      const preset = deferredPage()
      const destination = deferredPage()
      const latest = deferredPage()
      request.mockReturnValueOnce(preset.response).mockReturnValueOnce(destination.response).mockReturnValueOnce(latest.response)
      render(<BrowserApp initialData={{ ...shelf, page_size: 10, page: { articles: [article], next_cursor: 'next' } }} />)
      const select = screen.getByRole('combobox', { name: 'Articles per page' })
      await act(async () => { fireEvent.change(select, { target: { value: '100' } }) })
      await act(async () => { fireEvent.click(screen.getByRole('link', { name: 'Next' })) })
      await act(async () => { fireEvent.change(select, { target: { value: '20' } }) })
      await act(async () => destination.resolve(json({ ...shelf, page_size: 10, cursor: 'next' })))
      expect(select).toHaveValue('20')
      await act(async () => latest.resolve(json({ ...shelf, page_size: 20 })))
      await act(async () => preset.resolve(json({ ...shelf, page_size: 100 })))
      expect(select).toHaveValue('20')
      expect(request).toHaveBeenCalledTimes(3)
      expect(window.location.search).toBe('?page_size=20')
    })

    it.each(['Next', 'history', 'reload'] as const)('restores the loaded query after %s cancels a draft without changing that query', async navigation => {
      const destination = deferredPage()
      const nextPath = navigation === 'reload' ? '/' : '/?page_size=10&cursor=next'
      const nextData = { ...shelf, page_size: 10, cursor: navigation === 'reload' ? undefined : 'next' }
      localStorage.setItem(pageSizeStorageKey, '10')
      request.mockReturnValueOnce(destination.response).mockResolvedValueOnce(json({ ...shelf, page_size: 20 }))
      render(<BrowserApp initialData={{ ...shelf, page_size: 10, page: { articles: [article], next_cursor: 'next' } }} />)
      const input = screen.getByRole('searchbox', { name: 'Search saved news' })
      fireEvent.change(input, { target: { value: 'kernel' } })

      await act(async () => {
        if (navigation === 'history') {
          window.history.replaceState(null, '', nextPath)
          window.dispatchEvent(new PopStateEvent('popstate'))
        } else fireEvent.click(screen.getByRole('link', { name: navigation === 'Next' ? 'Next' : 'Newsfeed home' }))
      })
      await act(async () => { await vi.advanceTimersByTimeAsync(400) })
      expect(request).toHaveBeenCalledTimes(1)
      await act(async () => destination.resolve(json(nextData)))

      expect(window.location.pathname + window.location.search).toBe(nextPath)
      expect(input).toHaveValue('')
      await act(async () => {
        fireEvent.change(screen.getByRole('combobox', { name: 'Articles per page' }), { target: { value: '20' } })
      })
      expect(request).toHaveBeenCalledTimes(2)
      const sizeRequest = new URL(String(request.mock.calls[1][0]))
      expect(sizeRequest.searchParams.has('q')).toBe(false)
      expect(sizeRequest.searchParams.get('page_size')).toBe('20')
      expect(input).toHaveValue('')
      await act(async () => { await vi.advanceTimersByTimeAsync(300) })
      expect(request).toHaveBeenCalledTimes(2)
    })

    it('restores the loaded query after a state action cancels an unsent draft', async () => {
      const write = deferredPage()
      const refreshed = deferredPage()
      request.mockReturnValueOnce(write.response).mockReturnValueOnce(refreshed.response)
      render(<BrowserApp initialData={{ ...shelf, page_size: 10 }} />)
      const input = screen.getByRole('searchbox', { name: 'Search saved news' })
      fireEvent.change(input, { target: { value: 'kernel' } })
      fireEvent.click(screen.getByRole('button', { name: `Archive ${article.title}` }))

      await act(async () => { await vi.advanceTimersByTimeAsync(300) })
      await act(async () => write.resolve(json({ location: '/?page_size=10' })))
      await act(async () => { await vi.advanceTimersByTimeAsync(300) })
      await act(async () => refreshed.resolve(json({ ...shelf, page_size: 10, page: { articles: [] } })))
      expect(request.mock.calls.map(([, options]) => options?.method)).toEqual(['POST', 'GET'])
      expect(input).toHaveValue('')
      expect(screen.queryByRole('button', { name: 'Clear' })).not.toBeInTheDocument()
    })

    it('restores the loaded query rather than raw URL text after idle fragment history', async () => {
      const address = '/?q=SyStEmS+&page_size=10'
      window.history.replaceState(null, '', address)
      render(<BrowserApp initialData={{ ...shelf, page_size: 10, query: 'systems' }} />)
      const input = screen.getByRole('searchbox', { name: 'Search saved news' })
      fireEvent.change(input, { target: { value: 'kernel' } })

      await act(async () => {
        window.history.replaceState(null, '', address + '#main')
        window.dispatchEvent(new PopStateEvent('popstate'))
        await vi.advanceTimersByTimeAsync(300)
      })
      expect(request).not.toHaveBeenCalled()
      expect(input).toHaveValue('systems')
    })

    it('uses the loaded query when a size change follows a canceled draft during a state write', async () => {
      const write = deferredPage()
      window.history.replaceState(null, '', '/?q=saved&page_size=10')
      request.mockReturnValueOnce(write.response).mockResolvedValueOnce(json({ ...shelf, page_size: 20, query: 'saved' }))
      render(<BrowserApp initialData={{ ...shelf, page_size: 10, query: 'saved' }} />)
      const input = screen.getByRole('searchbox', { name: 'Search saved news' })
      fireEvent.change(input, { target: { value: 'kernel' } })
      fireEvent.click(screen.getByRole('button', { name: `Archive ${article.title}` }))
      await act(async () => {
        fireEvent.change(screen.getByRole('combobox', { name: 'Articles per page' }), { target: { value: '20' } })
      })
      expect(request).toHaveBeenCalledTimes(1)
      expect(input).toHaveValue('saved')
      await act(async () => write.resolve(json({ location: '/?q=saved&page_size=10' })))

      expect(request).toHaveBeenCalledTimes(2)
      const searchURL = new URL(String(request.mock.calls[1][0]))
      expect(searchURL.searchParams.get('q')).toBe('saved')
      expect(searchURL.searchParams.get('page_size')).toBe('20')
      expect(input).toHaveValue('saved')
      await act(async () => { await vi.advanceTimersByTimeAsync(300) })
      expect(request).toHaveBeenCalledTimes(2)
    })

    it('retains a refinement typed after navigation begins until that search wins', async () => {
      const destination = deferredPage()
      const search = deferredPage()
      const rawQuery = '  SyStEmS   source '
      request.mockReturnValueOnce(destination.response).mockReturnValueOnce(search.response)
      render(<BrowserApp initialData={{ ...shelf, page_size: 10, page: { articles: [article], next_cursor: 'next' } }} />)
      const input = screen.getByRole('searchbox', { name: 'Search saved news' })
      fireEvent.change(input, { target: { value: 'kernel' } })
      await act(async () => { fireEvent.click(screen.getByRole('link', { name: 'Next' })) })
      fireEvent.change(input, { target: { value: rawQuery } })

      await act(async () => { await vi.advanceTimersByTimeAsync(100) })
      await act(async () => destination.resolve(json({ ...shelf, page_size: 10, cursor: 'next' })))
      expect(input).toHaveValue(rawQuery)
      expect(screen.getByRole('main')).toHaveFocus()
      await act(async () => { await vi.advanceTimersByTimeAsync(150) })
      expect(request).toHaveBeenCalledTimes(2)
      const searchURL = new URL(String(request.mock.calls[1][0]))
      expect(searchURL.searchParams.get('q')).toBe(rawQuery)
      expect(searchURL.searchParams.has('cursor')).toBe(false)
      await act(async () => search.resolve(json({ ...shelf, page_size: 10, query: 'systems source' })))
      expect(input).toHaveValue(rawQuery)
      expect(window.location.search).toBe(searchURL.search)
    })

    it('keeps newer blurred input when an older search response finishes before its debounce', async () => {
      const first = deferredPage()
      const latest = deferredPage()
      const rawQuery = ' Kernel   source '
      request.mockReturnValueOnce(first.response).mockReturnValueOnce(latest.response)
      render(<BrowserApp initialData={{ ...shelf, page_size: 10 }} />)
      const input = screen.getByRole('searchbox', { name: 'Search saved news' })
      input.focus()
      fireEvent.change(input, { target: { value: 'kernel' } })
      await act(async () => { await vi.advanceTimersByTimeAsync(250) })
      fireEvent.change(input, { target: { value: rawQuery } })
      screen.getByRole('combobox', { name: 'Articles per page' }).focus()

      await act(async () => { await vi.advanceTimersByTimeAsync(100) })
      await act(async () => first.resolve(json({ ...shelf, page_size: 10, query: 'kernel' })))
      expect(input).toHaveValue(rawQuery)
      await act(async () => { await vi.advanceTimersByTimeAsync(150) })
      expect(request).toHaveBeenCalledTimes(2)
      expect(new URL(String(request.mock.calls[1][0])).searchParams.get('q')).toBe(rawQuery)
      await act(async () => latest.resolve(json({ ...shelf, page_size: 10, query: 'kernel source' })))
      expect(input).toHaveValue(rawQuery)
      expect(screen.getByRole('combobox', { name: 'Articles per page' })).toHaveFocus()
    })

    it('keeps refinements typed during a state write through its automatic page refresh', async () => {
      const write = deferredPage()
      const refreshed = deferredPage()
      const search = deferredPage()
      const rawQuery = ' Kernel   source '
      request.mockReturnValueOnce(write.response).mockReturnValueOnce(refreshed.response).mockReturnValueOnce(search.response)
      render(<BrowserApp initialData={{ ...shelf, page_size: 10 }} />)
      const input = screen.getByRole('searchbox', { name: 'Search saved news' })
      fireEvent.change(input, { target: { value: 'kernel' } })
      fireEvent.click(screen.getByRole('button', { name: `Archive ${article.title}` }))
      fireEvent.change(input, { target: { value: rawQuery } })
      await act(async () => { await vi.advanceTimersByTimeAsync(100) })
      await act(async () => write.resolve(json({ location: '/?page_size=10' })))
      expect(request).toHaveBeenCalledTimes(2)
      await act(async () => refreshed.resolve(json({ ...shelf, page_size: 10, page: { articles: [] } })))
      expect(input).toHaveValue(rawQuery)
      await act(async () => { await vi.advanceTimersByTimeAsync(150) })
      expect(request).toHaveBeenCalledTimes(3)
      expect(new URL(String(request.mock.calls[2][0])).searchParams.get('q')).toBe(rawQuery)
      await act(async () => search.resolve(json({ ...shelf, page_size: 10, query: 'kernel source' })))
      expect(input).toHaveValue(rawQuery)
      expect(request.mock.calls.map(([, options]) => options?.method)).toEqual(['POST', 'GET', 'GET'])
      for (const [, options] of request.mock.calls) expect(options).not.toHaveProperty('signal')
    })

    it('keeps the same input, raw text, focus and caret when a live search starts on page two', async () => {
      const search = deferredPage()
      const rawQuery = 'SyStEmS '
      window.history.replaceState(null, '', '/?page_size=10&cursor=second')
      request.mockReturnValueOnce(search.response)
      render(<BrowserApp initialData={{ ...shelf, page_size: 10, cursor: 'second' }} />)
      const input = screen.getByRole<HTMLInputElement>('searchbox', { name: 'Search saved news' })
      input.focus()
      fireEvent.change(input, { target: { value: rawQuery } })
      input.setSelectionRange(3, 5)
      await act(async () => { await vi.advanceTimersByTimeAsync(250) })
      expect(new URL(String(request.mock.calls[0][0])).searchParams.has('cursor')).toBe(false)
      await act(async () => search.resolve(json({ ...shelf, page_size: 10, query: 'systems' })))

      expect(screen.getByRole('searchbox', { name: 'Search saved news' })).toBe(input)
      expect(input).toHaveValue(rawQuery)
      expect(input).toHaveFocus()
      expect(input.selectionStart).toBe(3)
      expect(input.selectionEnd).toBe(5)
      expect(window.scrollTo).not.toHaveBeenCalled()
      expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled()
    })
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
      .mockResolvedValueOnce(json(archive))
      .mockResolvedValueOnce(json(archivedReader))
      .mockResolvedValueOnce(json(archive))
    render(<BrowserApp initialData={reader} />)

    await user.click(screen.getByRole('button', { name: 'Archive article' }))
    await user.click(screen.getByRole('link', { name: 'Archive' }))
    expect(request).toHaveBeenCalledOnce()
    await act(async () => write.resolve(json({ location: '/articles/story?from=archive' })))

    const error = await screen.findByRole('alert')
    expect(error).toHaveTextContent('The change was saved. This page could not load.')
    expect(error).toHaveFocus()
    expect(screen.queryByRole('button', { name: 'Archive article' })).not.toBeInTheDocument()
    expect(window.location.pathname).toBe('/archive')
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('heading', { name: 'Your archive' })).toBeVisible()
    expect(request.mock.calls.map(([url, options]) => [String(url), options?.method])).toEqual([
      [window.location.origin + '/api/v1/articles/story/archive', 'POST'],
      [window.location.origin + '/archive', 'GET'],
      [window.location.origin + '/archive', 'GET'],
    ])

    window.history.back()
    expect(await screen.findByRole('button', { name: 'Restore to feed' })).toBeEnabled()
    expect(window.location.pathname + window.location.search).toBe(originalPath)
    window.history.forward()
    expect(await screen.findByRole('heading', { name: 'Your archive' })).toBeVisible()
    expect(window.location.pathname).toBe('/archive')
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
    await user.click(screen.getByRole('link', { name: 'Archive' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The change was saved. This page could not load.')

    await act(async () => destinationRead.resolve(json({
      ...reader, from_archive: true, article: { ...article, archived_at: article.updated_at },
    })))
    expect(screen.queryByRole('heading', { name: article.title })).not.toBeInTheDocument()
    expect(window.location.pathname).toBe('/archive')
  })

  it('keeps the view and presents a safe refresh action on a stale article revision', async () => {
    window.history.replaceState(null, '', '/articles/story?from=feed')
    const user = userEvent.setup()
    request.mockResolvedValueOnce(new Response('<script>proxy diagnostics</script>', { status: 409 }))
      .mockResolvedValueOnce(json({ ...reader, article: { ...article, revision: 2 } }))
    render(<BrowserApp initialData={reader} />)
    await user.click(screen.getByRole('button', { name: 'Move to Trash' }))
    const detail = screen.getByRole('main')
    const error = await within(detail).findByRole('alert')
    expect(error).toHaveTextContent('This item changed.')
    expect(error).toHaveFocus()
    expect(screen.queryByText('proxy diagnostics')).not.toBeInTheDocument()
    expect(window.location.pathname).toBe('/articles/story')
    expect(within(detail).getByRole('heading', { name: article.title })).toBeVisible()
    expect(request.mock.calls[0][1]?.body).toEqual(new URLSearchParams({
      csrf_token: reader.csrf, expected_revision: '1', idempotency_key: reader.mutation_key!,
    }))
    await user.click(screen.getByRole('button', { name: 'Refresh page' }))
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
    await waitFor(() => expect(screen.getByRole('button', { name: 'Move to Trash' }).closest('form')
      ?.querySelector('[name="expected_revision"]')).toHaveValue('2'))
    expect(request.mock.calls.map(([, options]) => options?.method)).toEqual(['POST', 'GET'])
  })

  it('binds saves and save recovery to the source query during history traversal', async () => {
    const previous = '/articles/story?from=feed&cursor=first'
    const source = '/articles/story?from=feed&cursor=second'
    window.history.replaceState(null, '', previous)
    window.history.pushState(null, '', source)
    const user = userEvent.setup()
    request.mockResolvedValueOnce(new Response('Conflict', { status: 409 }))
      .mockResolvedValueOnce(json({ ...reader, cursor: 'first' }))
    render(<BrowserApp initialData={{ ...reader, cursor: 'second' }} />)

    const trashButton = screen.getByRole('button', { name: 'Move to Trash' })
    await user.click(trashButton)
    const refresh = await screen.findByRole('button', { name: 'Refresh page' })
    // The URL has already changed when popstate runs, before React handles it.
    const tryOldControls = vi.fn(() => {
      fireEvent.submit(trashButton.closest('form')!)
      fireEvent.click(refresh)
    })
    window.addEventListener('popstate', tryOldControls, { capture: true, once: true })
    window.history.back()

    await waitFor(() => expect(screen.getByRole('link', { name: 'Back to feed' }))
      .toHaveAttribute('href', '/?cursor=first'))
    expect(tryOldControls).toHaveBeenCalledOnce()
    expect(screen.getByRole('alert')).toHaveTextContent('This item changed.')
    expect(screen.queryByRole('button', { name: 'Refresh page' })).not.toBeInTheDocument()
    expect(request.mock.calls.map(([url, options]) => [String(url), options?.method])).toEqual([
      [window.location.origin + '/api/v1/articles/story/trash?return_to=' + encodeURIComponent(source), 'POST'],
      [window.location.origin + previous, 'GET'],
    ])
  })

  it('keeps a rejected article save visible after returning to the feed', async () => {
    window.history.replaceState(null, '', '/articles/story?from=feed')
    const user = userEvent.setup()
    const write = deferredPage()
    request.mockReturnValueOnce(write.response).mockResolvedValueOnce(json(shelf))
    render(<BrowserApp initialData={reader} />)

    await user.click(screen.getByRole('button', { name: 'Move to Trash' }))
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
      [window.location.origin + '/api/v1/articles/story/trash', 'POST'],
      [window.location.origin + '/', 'GET'],
    ])
  })

  it('keeps an outstanding save rejection accessible through the feed and another reader', async () => {
    window.history.replaceState(null, '', '/articles/story?from=feed')
    const user = userEvent.setup()
    const write = deferredPage()
    const second = { ...article, id: 'second-story', title: 'Another story' }
    const list = { ...shelf, page: { articles: [article, second] } }
    request.mockReturnValueOnce(write.response).mockResolvedValueOnce(json(list))
      .mockResolvedValueOnce(json({ ...reader, article: second }))
    render(<BrowserApp initialData={reader} />)

    await user.click(screen.getByRole('button', { name: 'Move to Trash' }))
    await user.click(screen.getByRole('link', { name: 'Back to feed' }))
    await act(async () => write.resolve(new Response('Conflict', { status: 409 })))

    expect(await screen.findByRole('heading', { name: 'Your reading shelf' })).toBeVisible()
    const feed = screen.getByRole('main')
    expect(feed).toHaveFocus()
    expect(within(feed).getByRole('alert')).toHaveTextContent('This item changed.')
    await user.click(within(feed).getByRole('link', { name: `Read saved article: ${second.title}` }))

    expect(await screen.findByRole('heading', { name: second.title })).toBeVisible()
    const articlePage = screen.getByRole('main')
    expect(articlePage).toHaveFocus()
    const error = within(articlePage).getByRole('alert')
    expect(error).toHaveTextContent('This item changed.')
    expect(within(error).queryByRole('button', { name: 'Refresh page' })).not.toBeInTheDocument()
    await user.tab()
    expect(within(error).getByRole('button', { name: 'Dismiss save error' })).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(window.location.pathname + window.location.search + window.location.hash).toBe(
      '/articles/second-story?from=feed',
    )
    expect(request.mock.calls.map(([, options]) => options?.method)).toEqual(['POST', 'GET', 'GET'])
  })

  it('keeps unconfirmed save feedback when the requested page fails and is retried', async () => {
    window.history.replaceState(null, '', '/articles/story?from=feed')
    const user = userEvent.setup()
    const write = deferredPage()
    request.mockReturnValueOnce(write.response)
      .mockResolvedValueOnce(new Response('Unavailable', { status: 503 }))
      .mockResolvedValueOnce(json(shelf))
    render(<BrowserApp initialData={reader} />)

    await user.click(screen.getByRole('button', { name: 'Archive article' }))
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
      [window.location.origin + '/api/v1/articles/story/archive', 'POST'],
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
    await user.click(screen.getByRole('link', { name: 'Archive' }))
    await act(async () => write.resolve(new Response('Sign in to continue.', { status: 401 })))
    const error = await screen.findByRole('alert')
    expect(error).toHaveTextContent('This page could not load.')
    expect(error).not.toHaveTextContent('The change was saved.')
    expect(screen.queryByRole('heading', { name: article.title })).not.toBeInTheDocument()
  })
})

import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from 'react'
import type { FormEvent, MouseEvent } from 'react'
import { App } from '@/app'
import { Button } from '@/components/ui/button'
import { observeViewportPageSize, pageSizeHeader, viewportPageSize } from '@/pagination'
import { todoFormPath } from '@/todo'
import type { TodoDraft } from '@/todo'
import type { Bootstrap } from '@/types'

const browserMediaType = 'application/vnd.agentnewsfeed.browser+json'
const pagePath = /^\/(?:archive|trash|articles\/[^/]+|jobs(?:\/[^/]+)?|todo)?$/
const stateFormPath = /^\/api\/v1\/(?:articles\/[^/]+\/(?:archive|restore|trash|restore-trash)|opportunities\/[^/]+\/(?:archive|unarchive|trash|restore))$/

type PageRequest = {
  url: URL
  history: 'push' | 'current'
  refresh?: 'preserve-viewport' | 'viewport-change'
}
type Page = { data: Bootstrap; pathAndQuery: string; focus?: { hash: string; scrollTop: boolean } }
type PageProblem = { message: string; retry: PageRequest }
type SaveProblem = { message: string; sourcePathAndQuery: string }

function sameOriginURL(href: string): URL | undefined {
  try {
    const url = new URL(href, window.location.href)
    if (url.origin === window.location.origin && !url.username && !url.password) return url
  } catch {
    return undefined
  }
}

function pageURL(href: string): URL | undefined {
  const url = sameOriginURL(href)
  if (url && pagePath.test(url.pathname)) return url
}

function pathAndQuery(url: Location | URL) {
  return url.pathname + url.search
}

class HTTPFailure extends Error {
  constructor(readonly status: number) {
    super(`HTTP ${status}`)
  }
}

async function browserJSON<T>(url: URL, options: { form?: URLSearchParams; pageSize?: number } = {}): Promise<T> {
  const { form, pageSize } = options
  const headers: Record<string, string> = { Accept: browserMediaType }
  if (pageSize !== undefined) headers[pageSizeHeader] = String(pageSize)
  const response = await fetch(url, {
    method: form ? 'POST' : 'GET',
    body: form,
    headers,
    mode: 'same-origin',
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
  })
  const expectedType = response.headers.get('Content-Type') === browserMediaType
  // The unconfigured server returns the setup page as browser JSON with 503.
  const isSetupPage = !form && response.status === 503 && expectedType
  if (!response.ok && !isSetupPage) {
    throw new HTTPFailure(response.status)
  }
  if (!expectedType) throw new Error('Unexpected browser response')
  return await response.json() as T
}

function pageError(status: number | undefined) {
  switch (status) {
    case 400: return 'This page address or cursor is invalid.'
    case 401: return 'Sign in to continue.'
    case 403: return 'This sign-in cannot open the requested page.'
    case 404: return 'This article or listing is unavailable in the requested view.'
    default: return 'This page could not load. Try again.'
  }
}

function saveError(status: number | undefined, todoWrite: boolean) {
  switch (status) {
    case 400: return todoWrite ? 'This task request is invalid. Check the title, tags and estimate.' : 'This save action is no longer available.'
    case 403: return 'The change could not be verified.'
    case 404: return todoWrite ? 'The change could not be confirmed. This task is unavailable.' : 'The change could not be confirmed. This article or listing is unavailable.'
    case 409: return 'The change was rejected. This item changed.'
    default: return 'The change could not be confirmed.'
  }
}

export function BrowserApp({ initialData }: { initialData: Bootstrap }) {
  const viewportSize = useSyncExternalStore(observeViewportPageSize, viewportPageSize)
  const [addressPathAndQuery, setAddressPathAndQuery] = useState(() => pathAndQuery(window.location))
  const [page, setPage] = useState<Page | null>(() => ({
    data: initialData,
    pathAndQuery: addressPathAndQuery,
    focus: window.location.hash ? { hash: window.location.hash, scrollTop: false } : undefined,
  }))
  const [pageProblem, setPageProblem] = useState<PageProblem | null>(null)
  const [saveProblem, setSaveProblem] = useState<SaveProblem | null>(null)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [todoDraft, setTodoDraft] = useState<(TodoDraft & { id: string; csrf: string }) | null>(null)
  const container = useRef<HTMLDivElement>(null)
  const pageErrorNotice = useRef<HTMLDivElement>(null)
  const saveErrorNotice = useRef<HTMLDivElement>(null)
  const navigation = useRef({ sequence: 0, loadingPage: false, savedSinceLoad: false })
  // History events need the last observed address before React commits its render.
  const lastObservedAddress = useRef(addressPathAndQuery)
  const pendingWrite = useRef<Promise<URL> | null>(null)

  const loadPage = useCallback(async (request: PageRequest) => {
    const { url, history, refresh } = request
    const requestedPathAndQuery = pathAndQuery(url)
    if (history === 'current' && requestedPathAndQuery !== pathAndQuery(window.location)) return
    const attempt = ++navigation.current.sequence
    const updateAddress = () => {
      if (history === 'push' && url.href !== window.location.href) {
        window.history.pushState(null, '', url.href)
      }
      lastObservedAddress.current = pathAndQuery(window.location)
      setAddressPathAndQuery(lastObservedAddress.current)
    }
    navigation.current.loadingPage = true
    setLoading(true)
    // Navigation can resolve a page error while the save outcome still needs attention.
    setPageProblem(null)
    try {
      // A navigation started during a write should read the resulting state.
      await pendingWrite.current?.catch(() => undefined)
      if (attempt !== navigation.current.sequence) return

      // Jobs details carry the same responsive list alongside the selected listing.
      const hasResponsiveList = url.pathname !== '/todo' && !url.pathname.startsWith('/articles/')
      let data: Bootstrap
      for (;;) {
        const requestedSize = hasResponsiveList ? viewportPageSize() : undefined
        // Let superseded GETs finish because canceling a Keycloak grant
        // refresh can revoke the session. Only the latest result changes the UI.
        data = await browserJSON<Bootstrap>(url, { pageSize: requestedSize })
        if (attempt !== navigation.current.sequence) return
        if (typeof data?.authenticated !== 'boolean' ||
          (data.authenticated && data.section === 'todo' && !Array.isArray(data.todo?.tasks)) ||
          (data.page_size !== undefined && data.page_size !== requestedSize)) {
          throw new Error('Invalid page response')
        }
        // Finish this same navigation at the new size if the viewport changed during the GET.
        if (data.page_size === undefined || data.page_size === viewportPageSize()) break
      }
      updateAddress()
      navigation.current.savedSinceLoad = false
      setTodoDraft(draft => data.authenticated && draft?.csrf === data.csrf ? draft : null)
      setPage({
        data,
        pathAndQuery: requestedPathAndQuery,
        focus: refresh ? undefined : { hash: window.location.hash, scrollTop: history === 'push' },
      })
    } catch (error) {
      if (attempt !== navigation.current.sequence) return
      const status = error instanceof HTTPFailure ? error.status : undefined
      const { savedSinceLoad } = navigation.current
      if (status === 401 || status === 403) setTodoDraft(null)
      // A failed resize can keep the last successfully loaded page at this address.
      if ((history === 'current' && refresh !== 'viewport-change') || savedSinceLoad || status === 401 || status === 403) {
        updateAddress()
        setPage(null)
      } else {
        // A newer link request can fail after Back has already moved the address.
        const currentPathAndQuery = pathAndQuery(window.location)
        setPage(previous => previous?.pathAndQuery === currentPathAndQuery ? previous : null)
      }
      const message = savedSinceLoad ? `The change was saved. ${pageError(status)}` : pageError(status)
      setPageProblem({ message, retry: request })
    } finally {
      if (attempt === navigation.current.sequence) {
        navigation.current.loadingPage = false
        setLoading(false)
      }
    }
  }, [])

  useEffect(() => {
    function onPopState() {
      const url = pageURL(window.location.href)
      if (!url) return
      // Keep idle fragment navigation native. During a request, history chooses the next view.
      const nextAddress = pathAndQuery(url)
      const sameAddress = nextAddress === lastObservedAddress.current
      const requestPending = navigation.current.loadingPage || pendingWrite.current !== null
      if (sameAddress && !requestPending) return
      lastObservedAddress.current = nextAddress
      setAddressPathAndQuery(nextAddress)
      void loadPage({ url, history: 'current' })
    }
    const navigationState = navigation.current
    window.addEventListener('popstate', onPopState)
    return () => {
      window.removeEventListener('popstate', onPopState)
      navigationState.sequence++
    }
  }, [loadPage])

  useEffect(() => {
    if (!page) return
    const loadedSize = page.data.page_size
    if (loading || saving || pageProblem || loadedSize === undefined || loadedSize === viewportSize) return
    let active = true
    // Let a foreground navigation or effect cleanup take precedence over this refresh.
    queueMicrotask(() => {
      if (!active || navigation.current.loadingPage || pendingWrite.current || loadedSize === viewportPageSize()) return
      const url = new URL(window.location.href)
      if (page.pathAndQuery === pathAndQuery(url)) void loadPage({ url, history: 'current', refresh: 'viewport-change' })
    })
    return () => { active = false }
  }, [loadPage, loading, page, pageProblem, saving, viewportSize])

  useLayoutEffect(() => {
    // Leave focus to the destination when the user navigates during a save.
    if (saveProblem?.sourcePathAndQuery === pathAndQuery(window.location) && !navigation.current.loadingPage) {
      saveErrorNotice.current?.focus({ preventScroll: true })
      saveErrorNotice.current?.scrollIntoView({ block: 'nearest' })
    }
  }, [saveProblem])

  useLayoutEffect(() => {
    if (!page?.focus || page.pathAndQuery !== pathAndQuery(window.location)) return
    const { hash, scrollTop } = page.focus
    const isFocusAnchor = hash === '#main' || hash === '#opportunity-detail' || hash === '#opportunity-list'
    const anchor = isFocusAnchor ? container.current?.querySelector<HTMLElement>(hash) : null
    const target = anchor ?? container.current?.querySelector<HTMLElement>('#main')
    if (anchor) anchor.scrollIntoView()
    else if (scrollTop) window.scrollTo(0, 0)
    target?.focus({ preventScroll: true })
    // Even after scrolling to the anchor, a short viewport can cut off the notice.
    saveErrorNotice.current?.scrollIntoView({ block: 'nearest' })
  }, [page])

  useLayoutEffect(() => {
    if (pageProblem) pageErrorNotice.current?.focus()
  }, [pageProblem])

  function onClick(event: MouseEvent<HTMLDivElement>) {
    if (event.defaultPrevented || event.button !== 0 || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
    const link = event.target instanceof Element ? event.target.closest<HTMLAnchorElement>('a[href]') : null
    if (!link || link.hasAttribute('download') || (link.target && link.target.toLowerCase() !== '_self')) return
    if (link.getAttribute('href')?.startsWith('#')) return
    const url = pageURL(link.href)
    if (!url) return
    event.preventDefault()
    void loadPage({ url, history: 'push' })
  }

  async function submitState(url: URL, form: URLSearchParams, sourcePathAndQuery: string) {
    const startedAt = navigation.current.sequence
    const todoWrite = url.pathname === todoFormPath
    setPageProblem(null)
    setSaveProblem(null)
    setSaving(true)
    const write = browserJSON<{ location: string }>(url, { form }).then(result => {
      const destination = typeof result?.location === 'string' ? pageURL(result.location) : undefined
      if (!destination) throw new Error('Unexpected mutation destination')
      // A newer navigation can choose the next page, but the old view is stale.
      navigation.current.savedSinceLoad = true
      return destination
    })
    pendingWrite.current = write
    let destination: URL
    try {
      destination = await write
    } catch (error) {
      const status = error instanceof HTTPFailure ? error.status : undefined
      // Remove private content on expiry even if another page load is pending.
      if (status === 401) {
        setTodoDraft(null)
        setPage(null)
        if (startedAt === navigation.current.sequence) {
          void loadPage({ url: new URL(window.location.href), history: 'current' })
        }
      } else {
        setSaveProblem({ message: saveError(status, todoWrite), sourcePathAndQuery })
      }
      return
    } finally {
      pendingWrite.current = null
      setSaving(false)
    }
    if (todoWrite && form.get('action') === 'add') {
      const savedDraftID = form.get('task_id')
      setTodoDraft(draft => draft?.id === savedDraftID ? null : draft)
    }
    if (startedAt === navigation.current.sequence) {
      if (todoWrite) {
        // Keep the current URL, scroll and compose controls during checklist saves.
        void loadPage({ url: new URL(window.location.href), history: 'current', refresh: 'preserve-viewport' })
      } else {
        void loadPage({ url: destination, history: 'push' })
      }
    }
  }

  function onSubmit(event: FormEvent<HTMLDivElement>) {
    const form = event.target
    if (event.defaultPrevented || !(form instanceof HTMLFormElement) || form.method !== 'post' ||
      form.enctype !== 'application/x-www-form-urlencoded' || form.target) return
    // The To-do field named "action" shadows the form.action property in browsers.
    const actionAttribute = form.getAttribute('action')
    const url = actionAttribute ? sameOriginURL(actionAttribute) : undefined
    if (!url || (url.pathname !== todoFormPath && !stateFormPath.test(url.pathname))) return
    event.preventDefault()
    // A history traversal can change the address before the old controls rerender.
    if (!page || page.pathAndQuery !== pathAndQuery(window.location) || pendingWrite.current || navigation.current.loadingPage) return
    const fields = new URLSearchParams()
    for (const [key, value] of new FormData(form)) {
      if (typeof value === 'string') fields.append(key, value)
    }
    if (url.pathname === todoFormPath && (page.data.section !== 'todo' || !page.data.can_write ||
      (fields.get('action') === 'add' && !fields.get('title')?.trim()))) return
    void submitState(url, fields, page.pathAndQuery)
  }

  function refreshSaveSource(sourcePathAndQuery: string) {
    const url = new URL(window.location.href)
    if (sourcePathAndQuery !== pathAndQuery(url) || pendingWrite.current || navigation.current.loadingPage) return
    setSaveProblem(null)
    const refresh = url.pathname === '/todo' ? 'preserve-viewport' : undefined
    void loadPage({ url, history: 'current', refresh })
  }

  const busy = loading || saving
  const pageMatchesAddress = page?.pathAndQuery === addressPathAndQuery
  const todoPage = pageMatchesAddress && page.data.section === 'todo'
  const canRefreshSaveSource = pageMatchesAddress && page.data.authenticated && !busy &&
    saveProblem?.sourcePathAndQuery === page.pathAndQuery
  // Keep the save failure beside the page retry while a page error is showing.
  const showSaveNoticeInPage = pageMatchesAddress && !pageProblem
  const saveNotice = saveProblem && (
    <div ref={saveErrorNotice} className={`navigation-error save-error${showSaveNoticeInPage ? '' : ' page-width'}`} role="alert" tabIndex={-1}>
      <p>{saveProblem.message}</p>
      {canRefreshSaveSource && (
        <Button type="button" variant="outline" size="sm" onClick={() => refreshSaveSource(saveProblem.sourcePathAndQuery)}>
          Refresh page
        </Button>
      )}
      <Button type="button" variant="outline" size="sm" aria-label="Dismiss save error" onClick={() => setSaveProblem(null)}>
        Dismiss
      </Button>
    </div>
  )
  return <div ref={container} onClick={onClick} onSubmit={onSubmit}>
    <p className={todoPage ? 'sr-only' : 'navigation-status page-width'} role="status">{saving ? 'Saving change…' : loading ? 'Loading page…' : ''}</p>
    {pageProblem && <div ref={pageErrorNotice} className="navigation-error page-width" role="alert" tabIndex={-1}>
      <p>{pageProblem.message}</p>
      <Button type="button" variant="outline" size="sm" onClick={() => void loadPage(pageProblem.retry)}>Try again</Button>
      {!pageMatchesAddress && <Button asChild variant="link" size="sm"><a href="/">Back to feed</a></Button>}
    </div>}
    {!showSaveNoticeInPage && saveNotice}
    {/* Keep the shell mounted across history loads while hiding any old view. */}
    <div aria-busy={busy} hidden={!pageMatchesAddress}>
      {page && <App data={page.data} actionsPending={busy || !pageMatchesAddress}
        saveNotice={showSaveNoticeInPage ? saveNotice : undefined} focusHash={window.location.hash}
        todoComposer={{
          draft: todoDraft?.csrf === page.data.csrf ? todoDraft : undefined,
          // Editing creates a new Add intent. An unchanged retry keeps the same ID.
          onChange: draft => setTodoDraft({ ...draft, id: crypto.randomUUID(), csrf: page.data.csrf }),
        }} />}
    </div>
  </div>
}

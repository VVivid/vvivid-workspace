export const pageSizeHeader = 'X-Newsfeed-Page-Size'
export const pageSizeStorageKey = 'agentnewsfeed:articles-per-page'
const pageSizeChange = 'newsfeed:page-size'

export function validPageSize(value: string | null) {
  if (!value || !/^[1-9]\d*$/.test(value)) return undefined
  const size = Number(value)
  return Number.isInteger(size) && size <= 500 ? size : undefined
}

export function preferredPageSize() {
  try {
    return validPageSize(localStorage.getItem(pageSizeStorageKey)) ?? 10
  } catch {
    return 10
  }
}

export function rememberPageSize(size: number) {
  try { localStorage.setItem(pageSizeStorageKey, String(size)) } catch { /* The URL still keeps this selection. */ }
  window.dispatchEvent(new Event(pageSizeChange))
}

export function observePageSize(update: () => void) {
  window.addEventListener('storage', update)
  window.addEventListener(pageSizeChange, update)
  return () => {
    window.removeEventListener('storage', update)
    window.removeEventListener(pageSizeChange, update)
  }
}

export function requestedPageSize(url: URL) {
  return validPageSize(url.searchParams.get('page_size')) ?? preferredPageSize()
}

export interface PagePosition {
  cursor?: string
  before?: string
  query?: string
  page_size?: number
}

export function urlWithPagePosition(path: string, position: PagePosition, from?: string) {
  const query = new URLSearchParams()
  if (from) query.set('from', from)
  if (position.query) query.set('q', position.query)
  if (position.page_size) query.set('page_size', String(position.page_size))
  if (position.cursor) query.set('cursor', position.cursor)
  if (position.before) query.set('before', position.before)
  return query.size ? `${path}?${query}` : path
}

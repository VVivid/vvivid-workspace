const narrowViewport = '(max-width: 640px)'

export const pageSizeHeader = 'X-Newsfeed-Page-Size'

export function viewportPageSize() {
  return window.matchMedia?.(narrowViewport).matches === false ? 5 : 3
}

export function observeViewportPageSize(update: () => void) {
  const media = window.matchMedia?.(narrowViewport)
  media?.addEventListener('change', update)
  return () => media?.removeEventListener('change', update)
}

export function urlWithPagePosition(path: string, position: { cursor?: string; before?: string }, from?: string) {
  const query = new URLSearchParams()
  if (from) query.set('from', from)
  if (position.cursor) query.set('cursor', position.cursor)
  if (position.before) query.set('before', position.before)
  return query.size ? `${path}?${query}` : path
}

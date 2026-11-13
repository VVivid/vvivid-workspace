import { Arrow } from '@/components/arrow'
import { Button } from '@/components/ui/button'
import { urlWithPagePosition } from '@/pagination'
import type { PageLinks } from '@/types'
import type { PagePosition } from '@/pagination'

function PageLink({ direction, href }: { direction: 'prev' | 'next'; href?: string }) {
  const contents = direction === 'prev' ? <><Arrow direction="left" /> Previous</> : <>Next <Arrow /></>
  return href
    ? <Button asChild variant="outline"><a href={href} rel={direction}>{contents}</a></Button>
    : <Button type="button" variant="outline" disabled>{contents}</Button>
}

export function Pagination({ path, page, stale, position = {} }: { path: string; page?: PageLinks; stale: boolean; position?: PagePosition }) {
  const previousCursor = page?.previous_cursor
  const nextCursor = page?.next_cursor
  if (!stale && !previousCursor && !nextCursor) return null

  const context = { query: position.query, page_size: position.page_size }
  const previousURL = previousCursor ? urlWithPagePosition(path, { ...context, before: previousCursor }) : undefined
  const nextURL = nextCursor ? urlWithPagePosition(path, { ...context, cursor: nextCursor }) : undefined
  return <nav className="pagination" aria-label="Pagination">
    <PageLink direction="prev" href={previousURL} />
    <PageLink direction="next" href={nextURL} />
    {stale && <a className="pagination-restart" href={urlWithPagePosition(path, context)}>Start of list</a>}
  </nav>
}

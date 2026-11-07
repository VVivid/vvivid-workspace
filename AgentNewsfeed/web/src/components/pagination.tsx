import { Arrow } from '@/components/arrow'
import { Button } from '@/components/ui/button'
import { urlWithPagePosition } from '@/pagination'
import type { PageLinks } from '@/types'

function PageLink({ direction, href }: { direction: 'prev' | 'next'; href?: string }) {
  const contents = direction === 'prev' ? <><Arrow direction="left" /> Previous</> : <>Next <Arrow /></>
  return href
    ? <Button asChild variant="outline"><a href={href} rel={direction}>{contents}</a></Button>
    : <Button type="button" variant="outline" disabled>{contents}</Button>
}

export function Pagination({ path, page, stale }: { path: string; page?: PageLinks; stale: boolean }) {
  const previousCursor = page?.previous_cursor
  const nextCursor = page?.next_cursor
  if (!stale && !previousCursor && !nextCursor) return null

  const previousURL = previousCursor ? urlWithPagePosition(path, { before: previousCursor }) : undefined
  const nextURL = nextCursor ? urlWithPagePosition(path, { cursor: nextCursor }) : undefined
  return <nav className="pagination" aria-label="Pagination">
    <PageLink direction="prev" href={previousURL} />
    <PageLink direction="next" href={nextURL} />
    {stale && <a className="pagination-restart" href={path}>Start of list</a>}
  </nav>
}

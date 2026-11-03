import { Arrow } from '@/components/arrow'
import { Leaf } from '@/components/leaf'
import { ArticleMarkdown } from '@/components/markdown'
import { Button } from '@/components/ui/button'
import type { Bootstrap, Opportunity } from '@/types'

const workplaceLabels = { remote: 'Remote', hybrid: 'Hybrid', on_site: 'On-site' }
const employmentLabels = {
  full_time: 'Full-time', part_time: 'Part-time', contract: 'Contract',
  temporary: 'Temporary', seasonal: 'Seasonal', other: 'Other',
}

function formatDate(timestamp: number) {
  return new Intl.DateTimeFormat('en', { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' }).format(timestamp)
}

function viewPath(view: Bootstrap['view']) {
  return view === 'archive' ? '/jobs/archive' : view === 'trash' ? '/jobs/trash' : '/jobs'
}

function listURL(data: Bootstrap) {
  return viewPath(data.view) + (data.cursor ? `?cursor=${encodeURIComponent(data.cursor)}` : '')
}

function detailURL(data: Bootstrap, id: string) {
  const query = new URLSearchParams({ from: data.view })
  if (data.cursor) query.set('cursor', data.cursor)
  return `/jobs/${encodeURIComponent(id)}?${query}#opportunity-detail`
}

function sourceURL(value: string) {
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && !url.username && !url.password ? url : undefined
  } catch {
    return undefined
  }
}

function Tags({ tags }: { tags?: string[] }) {
  return tags?.length ? <ul className="opportunity-tags" aria-label="Tags">
    {tags.map(tag => <li key={tag}>{tag}</li>)}
  </ul> : null
}

function StateForm({ data, opportunity, action, children }: {
  data: Bootstrap; opportunity: Opportunity; action: 'archive' | 'unarchive' | 'trash' | 'restore'; children: string
}) {
  return <form method="post" action={`/api/v1/opportunities/${encodeURIComponent(opportunity.id)}/${action}`}>
    <input type="hidden" name="csrf_token" value={data.csrf} />
    <input type="hidden" name="expected_revision" value={opportunity.revision} />
    <input type="hidden" name="idempotency_key" value={data.mutation_key ? `${data.mutation_key}:${action}` : ''} />
    <Button variant="link" type="submit" disabled={!data.can_write}>{children}</Button>
  </form>
}

function Detail({ data, opportunity }: { data: Bootstrap; opportunity: Opportunity }) {
  const original = sourceURL(opportunity.url)
  const archived = Boolean(opportunity.archived_at)
  const trashed = Boolean(opportunity.trashed_at)
  return <article id="opportunity-detail" className="opportunity-detail" tabIndex={-1} aria-label="Selected listing">
    <a className="back-link listing-back" href={`${listURL(data)}#opportunity-list`}><Arrow direction="left" /> Back to listings</a>
    <header>
      <p className="opportunity-company">{opportunity.company}</p>
      <p className="article-meta">Saved <time dateTime={new Date(opportunity.created_at).toISOString()}>{formatDate(opportunity.created_at)}</time></p>
      <h2>{opportunity.title}</h2>
      <p className="opportunity-kind">{opportunity.kind === 'internship' ? 'Internship' : 'Job'}</p>
    </header>
    <dl className="opportunity-facts">
      <div><dt>Location</dt><dd>{opportunity.location || 'Location not provided'}</dd></div>
      <div><dt>Workplace</dt><dd>{opportunity.workplace ? workplaceLabels[opportunity.workplace] : 'Workplace not provided'}</dd></div>
      <div><dt>Employment type</dt><dd>{opportunity.employment_type ? employmentLabels[opportunity.employment_type] : 'Employment type not provided'}</dd></div>
      <div><dt>Period</dt><dd>{opportunity.employment_period || 'Period not provided'}</dd></div>
      <div><dt>Compensation</dt><dd>{opportunity.compensation || 'Compensation not provided'}</dd></div>
      <div><dt>Application deadline</dt><dd>{opportunity.deadline
        ? <time dateTime={opportunity.deadline}>{formatDate(Date.parse(`${opportunity.deadline}T00:00:00Z`))}</time>
        : 'No deadline provided'}</dd></div>
    </dl>
    <section className="opportunity-description" aria-label="Source description">
      <h3>About the opportunity</h3>
      {opportunity.source_description
        ? <ArticleMarkdown content={opportunity.source_description} sourceURL={opportunity.url} />
        : <p className="missing-fact">No source description saved. Read the original listing for details.</p>}
    </section>
    <Tags tags={opportunity.tags} />
    <section className="opportunity-note" aria-label="Agent's note">
      <h3>Agent&apos;s note</h3>
      {opportunity.agent_note
        ? <ArticleMarkdown content={opportunity.agent_note} sourceURL={opportunity.url} />
        : <p className="missing-fact">No agent note saved.</p>}
    </section>
    {original ? <>
      <p className="attribution">Source: {original.hostname.replace(/^www\./, '')}. Saved by your agent.</p>
      <Button asChild className="opportunity-original"><a href={original.href} target="_blank" rel="noopener noreferrer">View original listing <Arrow direction="up-right" /></a></Button>
    </> : <p className="missing-fact">Original listing link unavailable.</p>}
    {trashed ? <p className="archive-note">In Trash since {formatDate(opportunity.trashed_at!)}. Restore to {archived ? 'Archive' : 'Active'} whenever you are ready.</p>
      : archived && <p className="archive-note">Archived {formatDate(opportunity.archived_at!)}. Kept for another day.</p>}
    <div className="opportunity-actions">
      {trashed ? <StateForm data={data} opportunity={opportunity} action="restore">{archived ? 'Restore to archive' : 'Restore to active'}</StateForm> : <>
        <StateForm data={data} opportunity={opportunity} action={archived ? 'unarchive' : 'archive'}>{archived ? 'Restore to active' : 'Archive listing'}</StateForm>
        <StateForm data={data} opportunity={opportunity} action="trash">Move to Trash</StateForm>
      </>}
    </div>
    {!data.can_write && <p className="permission-note" role="status">Listing actions are unavailable with this sign-in. You can still read your saved listings.</p>}
  </article>
}

export function Opportunities({ data }: { data: Bootstrap }) {
  const opportunities = data.opportunities_page?.opportunities ?? []
  const archived = data.view === 'archive'
  const trashed = data.view === 'trash'
  return <main id="main" className="jobs">
    <header className="jobs-heading">
      <p className="eyebrow">Jobs &amp; Internships</p>
      <h1>Your next chapter.</h1>
      <p className="subtitle">Opportunities collected by your agent. A place to take a closer look.</p>
    </header>
    <div className="jobs-subnav">
      <nav aria-label="Listing views">
        <a href="/jobs" aria-current={!archived && !trashed ? 'page' : undefined}>Active</a>
        <a href="/jobs/archive" aria-current={archived ? 'page' : undefined}>Archive</a>
        <a href="/jobs/trash" aria-current={trashed ? 'page' : undefined}>Trash</a>
      </nav>
      <span>{opportunities.length} {opportunities.length === 1 ? 'listing' : 'listings'} on this page</span>
    </div>
    <div className={`jobs-workspace${data.detail_selected ? ' detail-selected' : ''}`}>
      <section id="opportunity-list" className="opportunity-list" aria-label="Saved listings" tabIndex={-1}>
        {opportunities.length ? <>
          <p className="listing-order">{trashed ? 'Newest trashed first' : archived ? 'Newest archived first' : 'Newest saved first'}</p>
          <ul className="listing-rows">
            {opportunities.map(opportunity => <li key={opportunity.id}>
              <a className="opportunity-row" href={detailURL(data, opportunity.id)} aria-label={`View ${opportunity.title} at ${opportunity.company}`} aria-current={data.opportunity?.id === opportunity.id ? 'page' : undefined}>
                <span className="opportunity-row-meta"><span>{opportunity.company}</span><span className="opportunity-kind">{opportunity.kind === 'internship' ? 'Internship' : 'Job'}</span></span>
                <h2>{opportunity.title}</h2>
                <span className="listing-location">{opportunity.location || 'Location not provided'} · {opportunity.workplace ? workplaceLabels[opportunity.workplace] : 'Workplace not provided'}</span>
                <Tags tags={opportunity.tags} />
              </a>
            </li>)}
          </ul>
        </> : <div className="empty-shelf jobs-empty">
          <Leaf />
          <h2>{trashed ? 'Nothing in the trash.' : archived ? 'A little room for later.' : data.cursor ? 'You have reached the end.' : 'Room for the next find.'}</h2>
          <p>{trashed ? 'Listings moved to Trash remain recoverable.' : archived ? 'Archived listings stay here until you restore them.' : data.cursor ? 'Return to the start to see your most recently saved listings.' : 'Listings saved by your connected agent will appear here.'}</p>
        </div>}
        <div className="jobs-pagination">
          {data.cursor && <a href={viewPath(data.view)}>Start of list</a>}
          {data.opportunities_page?.next_cursor && <a className="reading-link" href={`${viewPath(data.view)}?cursor=${encodeURIComponent(data.opportunities_page.next_cursor)}`}>Earlier listings <Arrow /></a>}
        </div>
      </section>
      {data.opportunity && <Detail data={data} opportunity={data.opportunity} />}
    </div>
  </main>
}

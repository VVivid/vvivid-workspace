import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { App } from '@/app'
import type { Bootstrap, Opportunity } from '@/types'

const listing: Opportunity = {
  id: 'synthetic-owned-opportunity', kind: 'internship',
  url: 'https://careers.example/positions/systems-intern',
  title: 'Systems Engineering Intern', company: 'Synthetic Research',
  location: 'Boston, MA', workplace: 'hybrid', employment_type: 'full_time',
  employment_period: 'Summer 2027 · 12 weeks', compensation: '$42 per hour',
  deadline: '2026-12-15', tags: ['Go', 'Systems'],
  source_description: 'Original source description.\n\n- Source requirement',
  agent_note: 'Agent assessment, separate from the source.',
  revision: 3, created_at: 1793145600000, updated_at: 1793145600000,
}
const jobs: Bootstrap = {
  configured: true, authenticated: true, can_write: true, csrf: 'synthetic-csrf',
  section: 'jobs', view: 'feed', from_archive: false, mutation_key: 'synthetic-intent',
  sign_in_url: '/auth/login?return_to=%2Fjobs', detail_selected: true,
  opportunities_page: { opportunities: [listing], next_cursor: 'next+/=', previous_cursor: 'prev+/=' }, opportunity: listing,
}

describe('Jobs & Internships', () => {
  it('has fixed section navigation and separate view, page and selected-detail links', () => {
    render(<App data={{ ...jobs, view: 'archive', before: 'current+/=' }} />)
    const sections = screen.getByRole('navigation', { name: 'Sections' })
    expect(within(sections).getByRole('link', { name: 'News' })).toHaveAttribute('href', '/')
    expect(within(sections).getByRole('link', { name: 'Jobs & Internships' })).toHaveAttribute('aria-current', 'page')
    const views = screen.getByRole('navigation', { name: 'Listing views' })
    expect(within(views).getByRole('link', { name: 'Active' })).toHaveAttribute('href', '/jobs')
    expect(within(views).getByRole('link', { name: 'Archive' })).toHaveAttribute('aria-current', 'page')
    expect(within(views).getByRole('link', { name: 'Trash' })).toHaveAttribute('href', '/jobs/trash')
    expect(screen.getByRole('link', { name: `View ${listing.title} at ${listing.company}` })).toHaveAttribute('href', '/jobs/synthetic-owned-opportunity?from=archive&before=current%2B%2F%3D#opportunity-detail')
    expect(screen.getByRole('link', { name: 'Next' })).toHaveAttribute('href', '/jobs/archive?cursor=next%2B%2F%3D')
    expect(screen.getByRole('link', { name: 'Previous' })).toHaveAttribute('href', '/jobs/archive?before=prev%2B%2F%3D')
    expect(screen.getByRole('link', { name: 'Back to listings' })).toHaveAttribute('href', '/jobs/archive?before=current%2B%2F%3D#opportunity-list')
    expect(screen.getByRole('article', { name: 'Selected listing' })).toHaveAttribute('id', 'opportunity-detail')
    expect(screen.queryByRole('navigation', { name: 'Library' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /add|match|apply|schedule/i })).not.toBeInTheDocument()
  })

  it('presents stored facts, source description, tags and the distinct agent note', () => {
    render(<App data={jobs} />)
    const detail = screen.getByRole('article', { name: 'Selected listing' })
    for (const text of [listing.company, listing.title, 'Internship', 'Boston, MA', 'Hybrid', 'Full-time', 'Summer 2027 · 12 weeks', '$42 per hour', 'Dec 15, 2026']) {
      expect(within(detail).getByText(text)).toBeVisible()
    }
    expect(within(detail).getByText('Dec 15, 2026')).toHaveAttribute('datetime', '2026-12-15')
    expect(within(screen.getByRole('region', { name: 'Source description' })).getByText('Original source description.')).toBeVisible()
    expect(within(screen.getByRole('region', { name: "Agent's note" })).getByText(listing.agent_note!)).toBeVisible()
    expect(within(detail).getByRole('list', { name: 'Tags' })).toHaveTextContent('GoSystems')
    const source = screen.getByRole('link', { name: 'View original listing' })
    expect(source).toHaveAttribute('href', listing.url)
    expect(source).toHaveAttribute('rel', 'noopener noreferrer')
    expect(source).toHaveAttribute('target', '_blank')
  })

  it('leaves missing source facts explicitly unknown and does not substitute agent commentary', () => {
    const minimal: Opportunity = { id: listing.id, kind: 'job', url: listing.url, title: listing.title, company: listing.company, revision: 1, created_at: listing.created_at, updated_at: listing.updated_at, agent_note: 'Only the agent knows this assessment.' }
    render(<App data={{ ...jobs, opportunity: minimal, opportunities_page: { opportunities: [minimal] } }} />)
    const detail = screen.getByRole('article', { name: 'Selected listing' })
    for (const text of ['Location not provided', 'Workplace not provided', 'Employment type not provided', 'Period not provided', 'Compensation not provided', 'No deadline provided']) {
      expect(within(detail).getByText(text)).toBeVisible()
    }
    const source = screen.getByRole('region', { name: 'Source description' })
    expect(source).toHaveTextContent('No source description saved.')
    expect(source).not.toHaveTextContent(minimal.agent_note!)
    expect(within(detail).queryByRole('list', { name: 'Tags' })).not.toBeInTheDocument()
  })

  it('keeps Markdown safe without loading images or treating source and agent text as instructions', () => {
    const { container } = render(<App data={{ ...jobs, opportunity: {
      ...listing, title: '<img src=x onerror=alert(1)>',
      source_description: '# Source heading\n\n<script>run()</script>\n\n<img src="https://evil.example/raw.png">\n\n![Source figure](https://evil.example/figure.png)\n\n[Source details](/details)\n\n[Unsafe](javascript:alert%281%29)',
      agent_note: '![Agent image](https://evil.example/agent.png)\n\n[Agent unsafe](data:text/html,evil)',
    } }} />)
    expect(screen.getByRole('heading', { name: '<img src=x onerror=alert(1)>' })).toBeVisible()
    expect(container.querySelector('script')).not.toBeInTheDocument()
    expect(container.querySelector('img')).not.toBeInTheDocument()
    expect(screen.getByText('Source figure')).toBeVisible()
    expect(screen.getByText('Agent image')).toBeVisible()
    expect(screen.getByRole('link', { name: 'Source details' })).toHaveAttribute('href', 'https://careers.example/details')
    expect(screen.queryByRole('link', { name: 'Unsafe' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Agent unsafe' })).not.toBeInTheDocument()
  })

  it.each(['javascript:alert(1)', 'https://user:secret@careers.example/job', 'invalid-url'])('does not expose an unsafe original link: %s', url => {
    render(<App data={{ ...jobs, opportunity: { ...listing, url } }} />)
    expect(screen.queryByRole('link', { name: 'View original listing' })).not.toBeInTheDocument()
    expect(screen.getByText('Original listing link unavailable.')).toBeVisible()
  })

  it('uses stable separate action keys, revision and CSRF on explicit native forms', () => {
    const { rerender } = render(<App data={jobs} />)
    const archive = screen.getByRole('button', { name: 'Archive listing' }).closest('form')!
    const trash = screen.getByRole('button', { name: 'Move to Trash' }).closest('form')!
    for (const [form, action] of [[archive, 'archive'], [trash, 'trash']] as const) {
      expect(form).toHaveAttribute('method', 'post')
      expect(form).toHaveAttribute('action', `/api/v1/opportunities/${listing.id}/${action}`)
      expect(form.querySelector('[name="csrf_token"]')).toHaveValue(jobs.csrf)
      expect(form.querySelector('[name="expected_revision"]')).toHaveValue('3')
      expect(form.querySelector('[name="idempotency_key"]')).toHaveValue(`synthetic-intent:${action}`)
    }
    rerender(<App data={{ ...jobs }} />)
    expect(archive.querySelector('[name="idempotency_key"]')).toHaveValue('synthetic-intent:archive')
    rerender(<App data={{ ...jobs, view: 'trash', opportunity: { ...listing, archived_at: listing.created_at, trashed_at: listing.updated_at, revision: 5 } }} />)
    const restore = screen.getByRole('button', { name: 'Restore to archive' }).closest('form')!
    expect(restore).toHaveAttribute('action', `/api/v1/opportunities/${listing.id}/restore`)
    expect(restore.querySelector('[name="expected_revision"]')).toHaveValue('5')
    expect(restore.querySelector('[name="idempotency_key"]')).toHaveValue('synthetic-intent:restore')
    expect(screen.queryByRole('button', { name: 'Move to Trash' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /delete/i })).not.toBeInTheDocument()
  })

  it('shows archived state and disables actions for a read-only sign-in', () => {
    render(<App data={{ ...jobs, can_write: false, opportunity: { ...listing, archived_at: listing.created_at } }} />)
    expect(screen.getByText(/Archived Oct 28, 2026/)).toBeVisible()
    expect(screen.getByRole('button', { name: 'Restore to active' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Move to Trash' })).toBeDisabled()
    expect(screen.getByRole('status')).toHaveTextContent('You can still read your saved listings.')
    expect(screen.getByRole('button', { name: 'Restore to active' }).closest('form')).toHaveAttribute('action', `/api/v1/opportunities/${listing.id}/unarchive`)
  })

  it('offers honest empty views without sample listings', () => {
    const { rerender } = render(<App data={{ ...jobs, opportunity: undefined, opportunities_page: { opportunities: [] } }} />)
    expect(screen.getByText('Room for the next find.')).toBeVisible()
    expect(screen.queryByRole('article', { name: 'Selected listing' })).not.toBeInTheDocument()
    expect(screen.queryByText(listing.title)).not.toBeInTheDocument()
    rerender(<App data={{ ...jobs, view: 'trash', opportunity: undefined, opportunities_page: { opportunities: [] } }} />)
    expect(screen.getByText('Nothing in the trash.')).toBeVisible()
    expect(screen.getByText('Listings moved to Trash remain recoverable.')).toBeVisible()
  })

  it('keeps keyboard navigation usable through sections, views and listing selection', async () => {
    const user = userEvent.setup()
    render(<App data={jobs} />)
    for (const target of [
      screen.getByRole('link', { name: 'Skip to reading' }), screen.getByRole('link', { name: 'Newsfeed home' }),
      screen.getByRole('button', { name: 'Sign out' }), screen.getByRole('link', { name: 'News' }),
      screen.getByRole('link', { name: 'Jobs & Internships' }), screen.getByRole('link', { name: 'To-do' }),
      screen.getByRole('link', { name: 'Active' }),
      screen.getByRole('link', { name: 'Archive' }), screen.getByRole('link', { name: 'Trash' }),
      screen.getByRole('link', { name: `View ${listing.title} at ${listing.company}` }),
    ]) {
      await user.tab()
      expect(target).toHaveFocus()
    }
  })

  it('keeps a signed-out deep link private and preserves its sign-in return path', () => {
    const signIn = '/auth/login?return_to=%2Fjobs%2Fowned%3Ffrom%3Dtrash'
    render(<App data={{ ...jobs, authenticated: false, opportunity: undefined, opportunities_page: undefined, sign_in_url: signIn }} />)
    expect(screen.getByRole('link', { name: 'Sign in' })).toHaveAttribute('href', signIn)
    expect(screen.queryByRole('navigation', { name: 'Sections' })).not.toBeInTheDocument()
    expect(screen.queryByRole('article', { name: 'Selected listing' })).not.toBeInTheDocument()
  })
})

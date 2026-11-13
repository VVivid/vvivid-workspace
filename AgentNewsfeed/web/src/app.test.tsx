import { createHash } from 'node:crypto'
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from '@/app'
import type { Article, Bootstrap } from '@/types'
import imageFixture from '../../internal/thumbnail/testdata/markdown-images.json'

const article: Article = {
  id: 'synthetic-owned-article',
  url: 'https://publisher.example/original',
  title: 'A story worth keeping',
  summary: 'The introduction, as supplied by the source.',
  revision: 1,
  created_at: 1793145600000,
  updated_at: 1793145600000,
}
const shelf: Bootstrap = {
  configured: true,
  authenticated: true,
  can_write: true,
  csrf: 'synthetic-csrf-value',
  view: 'feed',
  from_archive: false,
  sign_in_url: '/auth/login?return_to=%2F',
  page: { articles: [article], next_cursor: 'opaque-cursor' },
}

function reader(overrides: Partial<Bootstrap> = {}): Bootstrap {
  return { ...shelf, page: undefined, view: 'reader', article, ...overrides }
}

describe('private reading shelf', () => {
  it('opens the full compact row at the source and keeps a separate saved reader and cursor navigation', () => {
    render(<App data={shelf} />)
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Your reading shelf')
    expect(screen.getByText(article.summary)).toBeVisible()
    expect(screen.getByRole('link', { name: article.title })).toHaveAttribute('href', article.url)
    expect(screen.getByRole('link', { name: article.title })).toHaveAttribute('rel', 'noopener noreferrer')
    expect(screen.getByRole('link', { name: article.title })).toHaveAttribute('target', '_blank')
    expect(screen.getByRole('link', { name: /^Read saved article:/ })).toHaveAttribute('href', '/articles/synthetic-owned-article?from=feed')
    expect(screen.getByRole('link', { name: 'Next' })).toHaveAttribute('href', '/?cursor=opaque-cursor')
    expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
    expect(screen.getByRole('navigation', { name: 'Library' })).toHaveTextContent('FeedArchiveTrash')
    const sections = within(screen.getByRole('navigation', { name: 'Sections' }))
    expect(sections.getAllByRole('link')).toHaveLength(3)
    expect(sections.getByRole('link', { name: 'News', current: 'page' })).toHaveAttribute('href', '/')
    expect(sections.getByRole('link', { name: 'To-do' })).toHaveAttribute('href', '/todo')
    expect(screen.getByRole('searchbox', { name: 'Search saved news' })).toBeVisible()
    expect(screen.getByRole('combobox', { name: 'Articles per page' })).toHaveValue('10')
  })

  it('supports keyboard navigation through the skip link, library and reader', async () => {
    const user = userEvent.setup()
    render(<App data={shelf} />)
    for (const [role, name] of [
      ['link', 'Skip to reading'], ['link', 'Newsfeed home'], ['button', 'Light'], ['button', 'Dark'],
      ['button', 'Sign out'], ['link', 'News'], ['link', 'To-do'], ['link', 'Settings'], ['link', 'Feed'], ['link', 'Archive'], ['link', 'Trash'],
      ['searchbox', 'Search saved news'], ['combobox', 'Articles per page'],
      ['link', article.title], ['link', `Read saved article: ${article.title}`],
    ]) {
      await user.tab()
      expect(screen.getByRole(role, { name })).toHaveFocus()
    }
  })

  it('highlights literal search terms and shows the introduction around a late match', () => {
    const introduction = 'Source background. '.repeat(30) + 'C++ [50%]_ source detail follows.'
    const { container, rerender } = render(<App data={{ ...shelf, query: 'c++ [50%]_', page_size: 20, page: {
      articles: [{ ...article, title: 'C++ [50%]_ source title', summary: introduction }], total: 1, start: 1, end: 1,
    } }} />)
    const summary = container.querySelector('.summary')!
    expect(summary.textContent).toMatch(/^…/)
    expect(summary.textContent).toContain('C++ [50%]_ source detail')
    expect(Array.from(summary.querySelectorAll('mark'), mark => mark.textContent)).toEqual(['C++', '[50%]_'])
    expect(screen.getByText('1–1 of 1 matching articles')).toBeVisible()
    rerender(<App data={{ ...shelf, page: { articles: [{ ...article, summary: introduction }] } }} />)
    expect(container.querySelector('.summary')).toHaveTextContent(introduction)
    expect(container.querySelector('mark')).not.toBeInTheDocument()
  })

  it.each([
    { name: 'Turkish dotted I', title: 'İstanbul source', query: 'istanbul', matches: ['İstanbul'] },
    { name: 'offset after Turkish dotted I', title: 'İstanbul source', query: 'source', matches: ['source'] },
    { name: 'Greek capital final sigma', title: 'ΟΣ ος', query: 'οσ', matches: ['ΟΣ'] },
    { name: 'distinct Greek lowercase final sigma', title: 'ΟΣ ος', query: 'ος', matches: ['ος'] },
    { name: 'supplementary letters and offsets', title: '😀𐐀 İstanbul source', query: '𐐨 source', matches: ['𐐀', 'source'] },
    { name: 'Unicode 16 Cyrillic mapping', title: '\u1c89 \u1c8a', query: '\u1c8a', matches: ['\u1c8a'] },
    { name: 'Unicode 16 Latin mapping', title: '\ua7cb \u0264', query: '\u0264', matches: ['\u0264'] },
    { name: 'Unicode 16 Garay mapping', title: '\u{10d50} \u{10d70}', query: '\u{10d70}', matches: ['\u{10d70}'] },
    { name: 'Unicode 17 Beria Erfe mapping', title: '\u{16ea0} \u{16ebb}', query: '\u{16ebb}', matches: ['\u{16ebb}'] },
  ])('matches backend lowercase semantics for $name without changing source text', ({ title, query, matches }) => {
    const { container } = render(<App data={{ ...shelf, query, page: { articles: [{ ...article, title }] } }} />)
    const heading = container.querySelector('.article-introduction h2')!
    expect(heading.textContent).toBe(`${title} `)
    expect(Array.from(heading.querySelectorAll('mark'), mark => mark.textContent)).toEqual(matches)
  })

  it('shows the original introduction around a late Unicode match', () => {
    const introduction = 'İ '.repeat(150) + 'İstanbul source detail follows.'
    const { container } = render(<App data={{ ...shelf, query: 'istanbul source', page: {
      articles: [{ ...article, summary: introduction }],
    } }} />)
    const summary = container.querySelector('.summary')!
    expect(summary.textContent).toMatch(/^…/)
    expect(summary.textContent).toContain('İstanbul source detail follows.')
    expect(Array.from(summary.querySelectorAll('mark'), mark => mark.textContent)).toEqual(['İstanbul', 'source'])
  })

  it.each([
    { name: 'CJK', prefix: '漢字'.repeat(120) },
    { name: 'wide ASCII', prefix: 'W'.repeat(400) },
  ])('starts the introduction excerpt at its first match after a long $name prefix', ({ prefix }) => {
    const introduction = `${prefix} needle following source detail.`
    const { container } = render(<App data={{ ...shelf, query: 'needle', page: {
      articles: [{ ...article, summary: introduction }],
    } }} />)
    const summary = container.querySelector('.summary')!
    // The two-line clamp must not spend its visible lines on leading context.
    expect(summary.textContent).toBe('…needle following source detail.')
    expect(summary.querySelector('mark')).toHaveTextContent('needle')
  })

  it('keeps the whole first search match when a valid single term exceeds the excerpt budget', () => {
    const term = 'W'.repeat(300)
    const introduction = `Original source context. ${term} following source detail.`
    const { container, rerender } = render(<App data={{ ...shelf, query: term.toLowerCase(), page: {
      articles: [{ ...article, summary: introduction }],
    } }} />)
    const summary = container.querySelector('.summary')!
    expect(summary.textContent).toBe(`…${term}…`)
    expect(summary.querySelector('mark')?.textContent).toBe(term)
    rerender(<App data={{ ...shelf, page: { articles: [{ ...article, summary: introduction }] } }} />)
    expect(container.querySelector('.summary')?.textContent).toBe(introduction)
    expect(container.querySelector('mark')).not.toBeInTheDocument()
  })

  it.each([
    { match: 'match', query: 'match', trailingEmoji: 127 },
    { match: '𐐀match', query: '𐐨match', trailingEmoji: 126 },
  ])('keeps supplementary characters whole at both introduction excerpt boundaries for $match', ({ match, query, trailingEmoji }) => {
    const introduction = '😀'.repeat(40) + match + '😀'.repeat(160)
    const { container } = render(<App data={{ ...shelf, query, page: {
      articles: [{ ...article, summary: introduction }],
    } }} />)
    const summary = container.querySelector('.summary')!
    expect(summary.textContent).toBe(`…${match}${'😀'.repeat(trailingEmoji)}…`)
    expect(summary.querySelector('mark')?.textContent).toBe(match)
  })

  it('highlights markup and regex punctuation as escaped literal source text', () => {
    const title = '<img src=x onerror=alert(1)> C++ [50%]_'
    const { container } = render(<App data={{ ...shelf, query: '<img c++ [50%]_', page: {
      articles: [{ ...article, title }],
    } }} />)
    const heading = container.querySelector('.article-introduction h2')!
    expect(heading.textContent).toBe(`${title} `)
    expect(Array.from(heading.querySelectorAll('mark'), mark => mark.textContent)).toEqual(['<img', 'C++', '[50%]_'])
    expect(container.querySelector('img, script, [onerror]')).not.toBeInTheDocument()
  })

  it('keeps the compact list free of image requests even when a stored thumbnail exists', () => {
    const { container } = render(<App data={{ ...shelf, page: { articles: [{ ...article, thumbnail_url: 'https://images.publisher.example/source.jpg', thumbnail_status: 'stored' }] } }} />)
    expect(container.querySelector('img')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: article.title })).toContainElement(screen.getByText(article.summary))
    const action = screen.getByRole('button', { name: `Archive ${article.title}` })
    expect(screen.getByRole('link', { name: article.title })).not.toContainElement(action)
    expect(action.closest('form')).toHaveAttribute('action', '/api/v1/articles/synthetic-owned-article/archive?return_to=%2F')
  })

  it('keeps archived navigation and distinguishes an empty archive', () => {
    render(<App data={{ ...shelf, view: 'archive', page: { articles: [] } }} />)
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Your archive')
    expect(screen.getByRole('link', { name: 'Archive' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByText(/Articles you archive will be waiting here/)).toBeVisible()
    expect(screen.getByRole('link', { name: 'Back to your feed' })).toHaveAttribute('href', '/')
  })

  it('shows archive timestamps and keeps pagination and reader back links in the archive', () => {
    const archived = { ...article, archived_at: 1793232000000 }
    const { rerender } = render(<App data={{ ...shelf, view: 'archive', cursor: 'current', page: {
      articles: [archived], next_cursor: 'archive-cursor', previous_cursor: 'newer-archive',
    } }} />)
    expect(screen.getByText('Oct 29, 2026').closest('.feed-date')).toHaveTextContent('Archived Oct 29, 2026')
    expect(screen.getByRole('link', { name: /^Read saved article:/ })).toHaveAttribute('href', '/articles/synthetic-owned-article?from=archive&cursor=current')
    expect(screen.getByRole('link', { name: 'Previous' })).toHaveAttribute('href', '/archive?before=newer-archive')
    expect(screen.getByRole('link', { name: 'Next' })).toHaveAttribute('href', '/archive?cursor=archive-cursor')

    rerender(<App data={reader({ from_archive: true, cursor: 'current', article: archived })} />)
    expect(screen.getByRole('link', { name: 'Back to archive' })).toHaveAttribute('href', '/archive?cursor=current')
  })

  it('offers a restart from a stale page and hides the pager for a complete single page', () => {
    const { rerender } = render(<App data={{ ...shelf, page: { articles: [article] } }} />)
    expect(screen.queryByRole('navigation', { name: 'Pagination' })).not.toBeInTheDocument()
    rerender(<App data={{ ...shelf, view: 'trash', before: 'stale', page: { articles: [] } }} />)
    expect(screen.getByRole('heading', { name: 'This page is empty.' })).toBeVisible()
    expect(screen.getByRole('link', { name: 'Start of list' })).toHaveAttribute('href', '/trash')
  })
})

describe('article reader', () => {
  it('renders original Markdown text, headings, lists, quotes, code and safe links', () => {
    const content = '# Original heading\n\nOriginal paragraph.\n\n- First source point\n- Second source point\n\n> Original quotation\n\n[Publisher reference](https://publisher.example/reference)\n\n[Relative docs](/docs) [Next story](next) [Source section](#history) [Another publisher](//reference.example/story)\n\n`inline code`\n\n```go\nfmt.Println("source")\n```'
    render(<App data={reader({ article: { ...article, content_markdown: content } })} />)
    expect(screen.getByRole('heading', { name: 'Original heading' })).toBeVisible()
    expect(screen.getAllByRole('listitem')).toHaveLength(2)
    expect(screen.getByText('Original quotation').closest('blockquote')).toBeInTheDocument()
    expect(screen.getByText('inline code').tagName).toBe('CODE')
    const link = screen.getByRole('link', { name: 'Publisher reference' })
    expect(link).toHaveAttribute('href', 'https://publisher.example/reference')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    expect(link).toHaveAttribute('target', '_blank')
    for (const [name, href] of [
      ['Relative docs', 'https://publisher.example/docs'],
      ['Next story', 'https://publisher.example/next'],
      ['Source section', 'https://publisher.example/original#history'],
      ['Another publisher', 'https://reference.example/story'],
    ]) {
      expect(screen.getByRole('link', { name })).toHaveAttribute('href', href)
    }
  })

  it('disables raw HTML, executable URLs and images absent from the server manifest while escaping source titles', () => {
    const { container } = render(<App data={reader({ article: { ...article, title: '<img src=x onerror=alert(1)>', content_markdown: '# Source\n\n<script>window.compromised=true</script>\n\n<img src="https://evil.example/raw.png" onerror="alert(1)">\n\n![inline](https://evil.example/markdown.png)\n\n[unsafe](javascript:alert%281%29)\n\nSafe source text.' } })} />)
    expect(screen.getByRole('heading', { name: '<img src=x onerror=alert(1)>' })).toBeVisible()
    expect(container.querySelector('script')).not.toBeInTheDocument()
    expect(container.querySelector('img')).not.toBeInTheDocument()
    expect(container.querySelector('[onerror]')).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'unsafe' })).not.toBeInTheDocument()
    expect(screen.getByText('Safe source text.')).toBeVisible()
  })

  it('matches the shared Markdown image destinations to protected routes without using parser order', () => {
    const images = Object.fromEntries(Object.entries(imageFixture.images).map(([destination, url]) => [
      destination, `/thumbnails/${article.id}/content/${createHash('sha256').update(url).digest('hex')}`,
    ]))
    const { container } = render(<App data={reader({
      article: { ...article, url: imageFixture.source_url, content_markdown: imageFixture.markdown },
      content_images: images,
    })} />)
    const rendered = container.querySelectorAll('.article-prose img')
    expect(rendered).toHaveLength(5)
    for (const [alt, destination] of [
      ['Relative', '../images/photo.png'],
      ['Reference', '//cdn.publisher.example/source.png'],
      ['Repeated', '../images/photo.png'],
      ['Escaped', 'images/pic(1).png?width=100&height=80'],
      ['Unicode', 'images/%C3%A9t%C3%A9.png'],
    ]) {
      expect(screen.getByRole('img', { name: alt })).toHaveAttribute('src', images[destination])
    }
    expect(container.querySelector('img[src^="https:"]')).not.toBeInTheDocument()
    expect(screen.queryByRole('img', { name: 'Private' })).not.toBeInTheDocument()
    expect(screen.getByText('Private', { selector: '.article-image-fallback' })).toBeVisible()
    fireEvent.error(screen.getByRole('img', { name: 'Reference' }))
    expect(screen.queryByRole('img', { name: 'Reference' })).not.toBeInTheDocument()
    expect(screen.getByText('Reference', { selector: '.article-image-fallback' })).toBeVisible()
  })

  it('rejects external and malformed image routes even if supplied in a manifest', () => {
    const { container } = render(<App data={reader({
      article: { ...article, content_markdown: '![External](photo.png) ![Inherited](__proto__) ![Wrong route](other.png)' },
      content_images: { 'photo.png': 'https://attacker.example/image.png', 'other.png': '/thumbnails/another?url=https://attacker.example/image.png' },
    })} />)
    expect(container.querySelector('img')).not.toBeInTheDocument()
    expect(screen.getByText('External', { selector: '.article-image-fallback' })).toBeVisible()
    expect(screen.getByText('Inherited', { selector: '.article-image-fallback' })).toBeVisible()
  })

  it('opens without a mutation and archives only through an explicit CSRF form', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    render(<App data={reader({ article: { ...article, content_markdown: 'Original source text.' } })} />)
    expect(fetchSpy).not.toHaveBeenCalled()
    const form = screen.getByRole('button', { name: 'Archive article' }).closest('form')!
    expect(form.closest('footer')).toHaveClass('reader-footer')
    expect(form).toHaveAttribute('method', 'post')
    expect(form).toHaveAttribute('action', '/api/v1/articles/synthetic-owned-article/archive')
    expect(form.querySelector('input[name="csrf_token"]')).toHaveValue('synthetic-csrf-value')
    expect(screen.getByRole('link', { name: 'Back to feed' })).toHaveAttribute('href', '/')
    fetchSpy.mockRestore()
  })

  it('restores from the archive and offers the original-link fallback for old records', () => {
    render(<App data={reader({ from_archive: true, article: { ...article, archived_at: 1793232000000 } })} />)
    expect(screen.getByRole('link', { name: 'Back to archive' })).toHaveAttribute('href', '/archive')
    expect(screen.getByRole('button', { name: 'Restore to feed' }).closest('form')).toHaveAttribute('action', '/api/v1/articles/synthetic-owned-article/restore')
    expect(screen.getByText('The full article has not been saved here yet. Follow the original link to keep reading.')).toBeVisible()
    for (const link of screen.getAllByRole('link', { name: 'Read original' })) {
      expect(link).toHaveAttribute('href', article.url)
      expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    }
  })

  it('leaves old read-only grants usable and explains why archiving is unavailable', () => {
    render(<App data={reader({ can_write: false })} />)
    expect(screen.getByRole('button', { name: 'Archive article' })).toBeDisabled()
    expect(screen.getByRole('status')).toHaveTextContent('Article actions are unavailable with this sign-in.')
    expect(screen.getByText(article.summary)).toBeVisible()
  })
})

describe('private shell', () => {
  it('offers sign-in with the deep-link return destination and exposes no saved content', () => {
    render(<App data={{ ...shelf, authenticated: false, sign_in_url: '/auth/login?return_to=%2Farchive', page: undefined }} />)
    expect(screen.getByRole('link', { name: 'Sign in' })).toHaveAttribute('href', '/auth/login?return_to=%2Farchive')
    expect(screen.queryByRole('link', { name: article.title })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Sign out' })).not.toBeInTheDocument()
  })

  it('shows the unconfigured shell without a sign-in action', () => {
    render(<App data={{ ...shelf, authenticated: false, configured: false, page: undefined }} />)
    expect(screen.getByText('Your shelf is being prepared.')).toBeVisible()
    expect(screen.queryByRole('link', { name: 'Sign in' })).not.toBeInTheDocument()
  })
})

describe('recoverable Trash', () => {
  it('keeps Trash navigation, ordering and pagination separate from Archive', () => {
    const trashed = { ...article, trashed_at: 1793232000000 }
    render(<App data={{ ...shelf, view: 'trash', page: { articles: [trashed], next_cursor: 'trash-cursor' } }} />)
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Your trash')
    expect(screen.getByRole('link', { name: 'Trash' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'Feed' })).not.toHaveAttribute('aria-current')
    expect(screen.getByRole('link', { name: /^Read saved article:/ })).toHaveAttribute('href', '/articles/synthetic-owned-article?from=trash')
    expect(screen.getByRole('link', { name: 'Next' })).toHaveAttribute('href', '/trash?cursor=trash-cursor')
    expect(screen.getByText('Newest trashed first')).toBeVisible()
  })

  it('uses explicit CSRF, revision and retry-key forms for Trash and Restore', () => {
    const { rerender } = render(<App data={reader({ mutation_key: 'unique-intent' })} />)
    const move = screen.getByRole('button', { name: 'Move to Trash' }).closest('form')!
    expect(move).toHaveAttribute('method', 'post')
    expect(move).toHaveAttribute('action', '/api/v1/articles/synthetic-owned-article/trash')
    expect(move.querySelector('input[name="csrf_token"]')).toHaveValue(shelf.csrf)
    expect(move.querySelector('input[name="expected_revision"]')).toHaveValue('1')
    expect(move.querySelector('input[name="idempotency_key"]')).toHaveValue('unique-intent')
    rerender(<App data={reader({ from_trash: true, mutation_key: 'restore-intent', article: { ...article, revision: 3, archived_at: 1793145600000, trashed_at: 1793232000000 } })} />)
    const restore = screen.getByRole('button', { name: 'Restore to archive' }).closest('form')!
    expect(restore).toHaveAttribute('action', '/api/v1/articles/synthetic-owned-article/restore-trash')
    expect(restore.querySelector('input[name="expected_revision"]')).toHaveValue('3')
    expect(restore.querySelector('input[name="idempotency_key"]')).toHaveValue('restore-intent')
    expect(screen.queryByRole('button', { name: 'Move to Trash' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Archive article' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to trash' })).toHaveAttribute('href', '/trash')
  })

  it('shows recoverability and permission state without a permanent-delete action', () => {
    const { rerender } = render(<App data={{ ...shelf, view: 'trash', page: { articles: [] } }} />)
    expect(screen.getByText('Nothing in the trash.')).toBeVisible()
    expect(screen.getByText(/Nothing is permanently deleted/)).toBeVisible()
    rerender(<App data={reader({ can_write: false, from_trash: true, article: { ...article, trashed_at: 1793232000000 } })} />)
    expect(screen.getByRole('button', { name: 'Restore to feed' })).toBeDisabled()
    expect(screen.getByRole('status')).toHaveTextContent('Article actions are unavailable')
    expect(screen.queryByRole('button', { name: /delete/i })).not.toBeInTheDocument()
  })

  it('loads stored Trash images only through the explicitly selected protected route', () => {
    const path = `/thumbnails/${article.id}/content/${'a'.repeat(64)}?trashed=true`
    const { container, rerender } = render(<App data={reader({ from_trash: true, article: { ...article, trashed_at: 1793232000000, content_markdown: '![Source figure](photo.png)' }, content_images: { 'photo.png': path } })} />)
    expect(screen.getByRole('img', { name: 'Source figure' })).toHaveAttribute('src', path)
    rerender(<App data={{ ...shelf, view: 'trash', page: { articles: [{ ...article, trashed_at: 1793232000000, thumbnail_url: 'https://images.example.com/source.jpg', thumbnail_status: 'stored' }] } }} />)
    expect(container.querySelector('img')).not.toBeInTheDocument()
    rerender(<App data={{ ...shelf, page: { articles: [{ ...article, thumbnail_url: 'https://images.example.com/source.jpg', thumbnail_status: 'unavailable' }] } }} />)
    expect(container.querySelector('img')).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: article.title })).toHaveAttribute('href', article.url)
  })
})

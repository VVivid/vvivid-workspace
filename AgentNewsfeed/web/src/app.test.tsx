import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from '@/app'
import type { Article, Bootstrap } from '@/types'

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
  it('keeps source text, reader links and cursor navigation in an editorial list', () => {
    render(<App data={shelf} />)
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Your reading shelf')
    expect(screen.getByText(article.summary)).toBeVisible()
    expect(screen.getByRole('link', { name: article.title })).toHaveAttribute('href', '/articles/synthetic-owned-article?from=feed')
    expect(screen.getByRole('link', { name: 'Read article' })).toHaveAttribute('href', '/articles/synthetic-owned-article?from=feed')
    expect(screen.getByRole('link', { name: 'Older articles' })).toHaveAttribute('href', '/?cursor=opaque-cursor')
    expect(screen.getByRole('navigation', { name: 'Library' })).toHaveTextContent('FeedArchive')
    expect(screen.queryByRole('searchbox')).not.toBeInTheDocument()
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
  })

  it('supports keyboard navigation through the skip link, library and reader', async () => {
    const user = userEvent.setup()
    render(<App data={shelf} />)
    await user.tab()
    expect(screen.getByRole('link', { name: 'Skip to reading' })).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('link', { name: 'Newsfeed home' })).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('link', { name: 'Feed' })).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('link', { name: 'Archive' })).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('button', { name: 'Sign out' })).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('link', { name: article.title })).toHaveFocus()
  })

  it('uses only the owned thumbnail proxy and falls back when it cannot load', () => {
    const { container } = render(<App data={{ ...shelf, page: { articles: [{ ...article, thumbnail_url: 'https://images.publisher.example/source.jpg' }] } }} />)
    const img = container.querySelector('img')!
    expect(img).toHaveAttribute('src', '/thumbnails/synthetic-owned-article')
    fireEvent.error(img)
    expect(container.querySelector('img')).not.toBeInTheDocument()
    expect(screen.getByText('A story for a slower moment')).toBeVisible()
  })

  it('keeps archived navigation and distinguishes an empty archive', () => {
    render(<App data={{ ...shelf, view: 'archive', page: { articles: [] } }} />)
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Your archive')
    expect(screen.getByRole('link', { name: 'Archive' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByText(/Articles you archive will be waiting here/)).toBeVisible()
    expect(screen.getByRole('link', { name: 'Back to your feed' })).toHaveAttribute('href', '/')
  })

  it('shows archive timestamps and keeps pagination and reader back links in the archive', () => {
    render(<App data={{ ...shelf, view: 'archive', page: { articles: [{ ...article, archived_at: 1793232000000 }], next_cursor: 'archive-cursor' } }} />)
    expect(screen.getByText(/Archived/, { selector: '.article-meta span' })).toHaveTextContent('Archived Oct 29, 2026')
    expect(screen.getByRole('link', { name: 'Read article' })).toHaveAttribute('href', '/articles/synthetic-owned-article?from=archive')
    expect(screen.getByRole('link', { name: 'Earlier archived articles' })).toHaveAttribute('href', '/archive?cursor=archive-cursor')
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

  it('disables raw HTML, executable URLs and inline images while escaping source titles', () => {
    const { container } = render(<App data={reader({ article: { ...article, title: '<img src=x onerror=alert(1)>', content_markdown: '# Source\n\n<script>window.compromised=true</script>\n\n<img src="https://evil.example/raw.png" onerror="alert(1)">\n\n![inline](https://evil.example/markdown.png)\n\n[unsafe](javascript:alert%281%29)\n\nSafe source text.' } })} />)
    expect(screen.getByRole('heading', { name: '<img src=x onerror=alert(1)>' })).toBeVisible()
    expect(container.querySelector('script')).not.toBeInTheDocument()
    expect(container.querySelector('img')).not.toBeInTheDocument()
    expect(container.querySelector('[onerror]')).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'unsafe' })).not.toBeInTheDocument()
    expect(screen.getByText('Safe source text.')).toBeVisible()
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
    expect(screen.getByRole('status')).toHaveTextContent('Archiving is unavailable with this sign-in.')
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

import { useLayoutEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { Leaf } from '@/components/leaf'
import { Arrow } from '@/components/arrow'
import { ArticleMarkdown } from '@/components/markdown'
import { Pagination } from '@/components/pagination'
import { urlWithPagePosition } from '@/pagination'
import { Todo } from '@/todo'
import { ThemePicker } from '@/theme'
import { Settings } from '@/settings'
import type { SettingsEditor } from '@/settings'
import type { TodoComposer } from '@/todo'
import type { Article, Bootstrap } from '@/types'

function formatDate(timestamp: number) {
  return new Intl.DateTimeFormat('en', {
    month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC',
  }).format(timestamp)
}

function sourceHostname(url: string) {
  return new URL(url).hostname.replace(/^www\./, '')
}

function Header({ data }: { data: Bootstrap }) {
  return (
    <header className="site-header">
      <div className="header-inner">
        <a className="brand" href="/" aria-label="Newsfeed home">AgentNewsfeed</a>
        <span className="private-label">Private</span>
        <ThemePicker />
        {data.authenticated && (
          <>
            <form method="post" action="/auth/logout">
              <input type="hidden" name="csrf_token" value={data.csrf} />
              <Button variant="outline" type="submit" size="sm">Sign out</Button>
            </form>
          </>
        )}
      </div>
    </header>
  )
}

function Sections({ data }: { data: Bootstrap }) {
  return (
    <aside className="section-sidebar">
      <nav aria-label="Sections">
        <a href="/" aria-current={!data.section || data.section === 'news' ? 'page' : undefined}><Leaf /><span>News</span></a>
        <a href="/todo" aria-current={data.section === 'todo' ? 'page' : undefined}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="m3 6 2 2 4-4M12 6h9M12 17h9M3 14h6v6H3z" />
          </svg>
          <span>To-do</span>
        </a>
        <a href="/settings" aria-current={data.section === 'settings' ? 'page' : undefined}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true">
            <path d="M4 6h16M4 12h16M4 18h16M8 3v6M16 9v6M10 15v6" />
          </svg>
          <span>Settings</span>
        </a>
      </nav>
    </aside>
  )
}

function NewsViews({ data }: { data: Bootstrap }) {
  const readerShelf = data.from_trash ? 'trash' : data.from_archive ? 'archive' : 'feed'
  const selectedShelf = data.view === 'reader' ? readerShelf : data.view
  return <nav className="news-views" aria-label="Library">
    <a href="/" aria-current={selectedShelf === 'feed' ? 'page' : undefined}>Feed</a>
    <a href="/archive" aria-current={selectedShelf === 'archive' ? 'page' : undefined}>Archive</a>
    <a href="/trash" aria-current={selectedShelf === 'trash' ? 'page' : undefined}>Trash</a>
  </nav>
}

function ArticleActions({ article, data, pending }: { article: Article; data: Bootstrap; pending: boolean }) {
  const [idempotencyKey] = useState(() => crypto.randomUUID())
  const trashed = Boolean(article.trashed_at)
  const archived = Boolean(article.archived_at)
  const actions = trashed ? [['restore-trash', 'Restore']] : [
    [archived ? 'restore' : 'archive', archived ? 'Restore' : 'Archive'], ['trash', 'Trash'],
  ]
  const path = data.view === 'feed' ? '/' : `/${data.view}`
  const returnTo = encodeURIComponent(urlWithPagePosition(path, data))
  return <div className="article-actions">
    <a href={urlWithPagePosition(`/articles/${encodeURIComponent(article.id)}`, data, data.view)}
      aria-label={`Read saved article: ${article.title}`}>Saved copy</a>
    {actions.map(([action, label]) => <form key={action} method="post"
      action={`/api/v1/articles/${encodeURIComponent(article.id)}/${action}?return_to=${returnTo}`}>
      <input type="hidden" name="csrf_token" value={data.csrf} />
      {(action === 'trash' || trashed) && <>
        <input type="hidden" name="expected_revision" value={article.revision} />
        <input type="hidden" name="idempotency_key" value={idempotencyKey} />
      </>}
      <button type="submit" aria-label={`${label} ${article.title}`} disabled={!data.can_write || pending}>{label}</button>
    </form>)}
  </div>
}

function Shelf({ data, actionsPending, saveNotice }: { data: Bootstrap; actionsPending: boolean; saveNotice?: ReactNode }) {
  const archived = data.view === 'archive'
  const trashed = data.view === 'trash'
  const articles = data.page?.articles ?? []
  const shelfPath = trashed ? '/trash' : archived ? '/archive' : '/'
  const stalePage = articles.length === 0 && Boolean(data.cursor || data.before)
  const main = useRef<HTMLElement>(null)
  const submittedControl = useRef<HTMLElement | null>(null)
  useLayoutEffect(() => {
    const control = submittedControl.current
    if (actionsPending || !control) return
    submittedControl.current = null
    if (!control.isConnected && document.activeElement === document.body) main.current?.focus({ preventScroll: true })
  }, [actionsPending, data.page])
  return (
    <main ref={main} id="main" tabIndex={-1} className="shelf page-width" onSubmitCapture={() => {
      if (document.activeElement instanceof HTMLElement) submittedControl.current = document.activeElement
    }}>
      {saveNotice}
      <div className="shelf-intro">
        <h1>{trashed ? 'Your trash' : archived ? 'Your archive' : 'Your reading shelf'}</h1>
      </div>
      <div className="list-heading">
        <span>{trashed ? 'Trashed articles' : archived ? 'Archived articles' : 'Saved articles'}</span>
        <span>{trashed ? 'Newest trashed first' : archived ? 'Newest archived first' : 'Newest saved first'}</span>
      </div>
      {articles.length > 0 ? (
        <div className="article-list">
          <div className="feed-columns" aria-hidden="true"><span>Article</span><span>Source</span><span>{trashed ? 'Trashed' : archived ? 'Archived' : 'Saved'}</span><span>Actions</span></div>
          {articles.map(article => {
            const timestamp = trashed ? article.trashed_at! : archived ? article.archived_at! : article.created_at
            return (
              <article className="feed-row" key={article.id}>
                <a href={article.url} className="feed-entry" aria-label={article.title} target="_blank" rel="noopener noreferrer">
                  <div className="article-introduction">
                    <h2>{article.title} <Arrow direction="up-right" /></h2>
                    <p className="summary">{article.summary}</p>
                  </div>
                  <span className="feed-source">{sourceHostname(article.url)}</span>
                  <span className="feed-date"><span className="sr-only">{trashed ? 'Trashed' : archived ? 'Archived' : 'Saved'} </span>
                    <time dateTime={new Date(timestamp).toISOString()}>{formatDate(timestamp)}</time>
                  </span>
                </a>
                <ArticleActions key={`${article.id}:${article.revision}`} article={article} data={data} pending={actionsPending} />
              </article>
            )
          })}
        </div>
      ) : (
        <div className="empty-shelf">
          <Leaf />
          <h2>{stalePage ? 'This page is empty.' : trashed ? 'Nothing in the trash.' : archived ? 'Room for stories you have read.' : 'A little space for a good story.'}</h2>
          <p>{stalePage ? 'Return to the start to see the current list.' : trashed ? 'Trashed articles stay recoverable, with their original content and images. Nothing is permanently deleted.' : archived ? 'Articles you archive will be waiting here. Restore one whenever you want to return to it.' : 'Save a sourced article with your connected agent, then settle in here when you have a moment.'}</p>
          {!stalePage && (archived || trashed) && <a className="reading-link" href="/">Back to your feed <Arrow /></a>}
        </div>
      )}
      <Pagination path={shelfPath} page={data.page} stale={stalePage} />
    </main>
  )
}

function Reader({ data, article, actionsPending, saveNotice }: {
  data: Bootstrap; article: Article; actionsPending: boolean; saveNotice?: ReactNode
}) {
  const archived = Boolean(article.archived_at)
  const trashed = Boolean(article.trashed_at)
  const action = trashed ? 'restore-trash' : archived ? 'restore' : 'archive'
  const actionLabel = trashed
    ? archived ? 'Restore to archive' : 'Restore to feed'
    : archived ? 'Restore to feed' : 'Archive article'
  const back = data.from_trash ? 'trash' : data.from_archive ? 'archive' : 'feed'
  return (
    <main id="main" tabIndex={-1} className="reader page-width">
      {saveNotice}
      <a className="back-link" href={urlWithPagePosition(back === 'feed' ? '/' : `/${back}`, data)}>
        <Arrow direction="left" /> Back to {back}
      </a>
      <article className="reader-article">
        <header className="reader-heading">
          <p className="article-meta">
            <span className="source">{sourceHostname(article.url)}</span>
            <span aria-hidden="true">·</span>
            <span>Saved <time dateTime={new Date(article.created_at).toISOString()}>{formatDate(article.created_at)}</time></span>
          </p>
          <h1>{article.title}</h1>
          <p className="reader-summary">{article.summary}</p>
          <p className="attribution">
            From <a href={article.url} target="_blank" rel="noopener noreferrer">{sourceHostname(article.url)}</a>. {article.content_markdown ? 'Original article, supplied by your agent.' : 'Source introduction, saved by your agent.'}
          </p>
          <div className="reader-actions">
            <Button asChild variant="link" className="original-link">
              <a href={article.url} target="_blank" rel="noopener noreferrer">Read original <Arrow direction="up-right" /></a>
            </Button>
          </div>
          {trashed && <p className="archive-note">In Trash since {formatDate(article.trashed_at!)}. Restore to {archived ? 'archive' : 'feed'} whenever you are ready.</p>}
          {!trashed && archived && <p className="archive-note">Archived {formatDate(article.archived_at!)}. Kept here for another day.</p>}
        </header>
        <Separator />
        {article.content_markdown ? (
          <ArticleMarkdown content={article.content_markdown} sourceURL={article.url} images={data.content_images} />
        ) : (
          <div className="reader-fallback">
            <h2>This story is waiting at the source.</h2>
            <p>The full article has not been saved here yet. Follow the original link to keep reading.</p>
            <Button asChild>
              <a href={article.url} target="_blank" rel="noopener noreferrer">Read original <Arrow direction="up-right" /></a>
            </Button>
          </div>
        )}
        <footer className="reader-footer">
          <Separator />
          <div className="reader-end">
            <p>{trashed ? 'This story is safely kept. Restore it to its previous place.' : archived ? 'Bring this story back to your reading shelf.' : 'Finished for now? Keep this story in your archive.'}</p>
            <form method="post" action={`/api/v1/articles/${encodeURIComponent(article.id)}/${action}`}>
              <input type="hidden" name="csrf_token" value={data.csrf} />
              {trashed && <>
                <input type="hidden" name="expected_revision" value={article.revision} />
                <input type="hidden" name="idempotency_key" value={data.mutation_key ?? ''} />
              </>}
              <Button variant="outline" type="submit" disabled={!data.can_write || actionsPending}>
                {actionLabel}
              </Button>
            </form>
          </div>
          {!trashed && <div className="reader-end">
            <p>Want to set this story aside? You can restore it later.</p>
            <form method="post" action={`/api/v1/articles/${encodeURIComponent(article.id)}/trash`}>
              <input type="hidden" name="csrf_token" value={data.csrf} />
              <input type="hidden" name="expected_revision" value={article.revision} />
              <input type="hidden" name="idempotency_key" value={data.mutation_key ?? ''} />
              <Button variant="outline" type="submit" disabled={!data.can_write || actionsPending}>Move to Trash</Button>
            </form>
          </div>}
          {!data.can_write && (
            <p className="permission-note" role="status">Article actions are unavailable with this sign-in. Sign in again after your account has write permission.</p>
          )}
        </footer>
      </article>
    </main>
  )
}

function Gate({ data, saveNotice }: { data: Bootstrap; saveNotice?: ReactNode }) {
  const todo = data.section === 'todo'
  const settings = data.section === 'settings'
  return (
    <main id="main" tabIndex={-1} className="gate page-width">
      {saveNotice}
      <p className="eyebrow">A quiet corner of the internet</p>
      <h1>{settings ? 'Reading preferences' : todo ? 'To-do' : 'Your reading shelf'}</h1>
      <p className="subtitle">{settings ? 'Guide what your agent finds for you.' : todo ? 'Your private checklist.' : 'Good stories, saved for a slower moment.'}</p>
      <Separator />
      <h2>{data.configured ? 'Make yourself at home.' : 'Your shelf is being prepared.'}</h2>
      {data.configured
        ? <p>{settings ? 'Sign in to view your reading preferences.' : todo ? 'Sign in to view your tasks.' : 'Sign in to read your private collection of stories, ideas, and things worth keeping.'}</p>
        : <p>Newsfeed identity is not configured. Your account administrator can finish the setup.</p>}
      {data.configured && (
        <Button asChild><a href={data.sign_in_url}>Sign in <Arrow /></a></Button>
      )}
    </main>
  )
}

export function App({ data, actionsPending = false, saveNotice, todoComposer, settingsEditor }: {
  data: Bootstrap
  actionsPending?: boolean
  saveNotice?: ReactNode
  todoComposer?: TodoComposer
  settingsEditor?: SettingsEditor
}) {
  return (
    <>
      <a className="skip-link" href="#main">{data.section === 'todo' ? 'Skip to tasks' : 'Skip to reading'}</a>
      <Header data={data} />
      {!data.authenticated ? (
        <Gate data={data} saveNotice={saveNotice} />
      ) : (
        <div className="library-layout">
          <Sections data={data} />
          <div className="library-content">
            {(!data.section || data.section === 'news') && <NewsViews data={data} />}
            {data.section === 'settings'
              ? data.settings && settingsEditor && <Settings data={data} editor={settingsEditor} pending={actionsPending} saveNotice={saveNotice} />
              : data.section === 'todo'
                ? data.todo && <Todo list={data.todo} csrf={data.csrf} canWrite={data.can_write}
                  pending={actionsPending} composer={todoComposer} saveNotice={saveNotice} />
                : data.view === 'reader' && data.article
                  ? <Reader data={data} article={data.article} actionsPending={actionsPending} saveNotice={saveNotice} />
                  : <Shelf data={data} actionsPending={actionsPending} saveNotice={saveNotice} />}
          </div>
        </div>
      )}
      <footer className="site-footer page-width">
        <Separator />
        <div><Leaf /><span>Your articles. Your space.</span></div>
      </footer>
    </>
  )
}

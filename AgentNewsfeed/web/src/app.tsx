import { useState } from 'react'
import type { ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { Leaf } from '@/components/leaf'
import { Arrow } from '@/components/arrow'
import { ArticleMarkdown } from '@/components/markdown'
import { Pagination } from '@/components/pagination'
import { urlWithPagePosition } from '@/pagination'
import { Opportunities } from '@/opportunities'
import { Todo } from '@/todo'
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

function Thumbnail({ article }: { article: Article }) {
  // A fresh article can retry even when the proxy URL is unchanged.
  const [failedArticle, setFailedArticle] = useState<Article | null>(null)
  return (
    <div className="article-thumbnail">
      {article.thumbnail_url && !['unavailable', 'budget_exceeded'].includes(article.thumbnail_status ?? '') && failedArticle !== article ? (
        <img
          src={`/thumbnails/${encodeURIComponent(article.id)}${article.trashed_at ? '?trashed=true' : ''}`}
          alt=""
          loading="lazy"
          width="312"
          height="184"
          onError={() => setFailedArticle(article)}
        />
      ) : (
        <div className="thumbnail-placeholder">
          <Leaf /><span>A story for a slower moment</span>
        </div>
      )}
    </div>
  )
}

function Header({ data }: { data: Bootstrap }) {
  const readerShelf = data.from_trash ? 'trash' : data.from_archive ? 'archive' : 'feed'
  const selectedShelf = data.view === 'reader' ? readerShelf : data.view
  const isNews = !data.section || data.section === 'news'
  return (
    <header className="site-header">
      <div className="header-inner">
        <a className="brand" href="/" aria-label="Newsfeed home"><Leaf /><span>Newsfeed</span></a>
        <span className="private-label">Private</span>
        {data.authenticated && (
          <>
            {isNews && <nav aria-label="Library">
              <a href="/" aria-current={selectedShelf === 'feed' ? 'page' : undefined}>Feed</a>
              <a href="/archive" aria-current={selectedShelf === 'archive' ? 'page' : undefined}>Archive</a>
              <a href="/trash" aria-current={selectedShelf === 'trash' ? 'page' : undefined}>Trash</a>
            </nav>}
            <form method="post" action="/auth/logout" className={isNews ? undefined : 'header-signout'}>
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
      <p className="eyebrow">Your sections</p>
      <nav aria-label="Sections">
        <a href="/" aria-current={!data.section || data.section === 'news' ? 'page' : undefined}><Leaf /><span>News</span></a>
        <a href="/jobs" aria-current={data.section === 'jobs' ? 'page' : undefined}><Arrow direction="up-right" /><span>Jobs &amp; Internships</span></a>
        <a href="/todo" aria-current={data.section === 'todo' ? 'page' : undefined}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="m3 6 2 2 4-4M12 6h9M12 17h9M3 14h6v6H3z" />
          </svg>
          <span>To-do</span>
        </a>
      </nav>
    </aside>
  )
}

function Shelf({ data, saveNotice }: { data: Bootstrap; saveNotice?: ReactNode }) {
  const archived = data.view === 'archive'
  const trashed = data.view === 'trash'
  const articles = data.page?.articles ?? []
  const shelfPath = trashed ? '/trash' : archived ? '/archive' : '/'
  const stalePage = articles.length === 0 && Boolean(data.cursor || data.before)
  return (
    <main id="main" tabIndex={-1} className="shelf page-width">
      {saveNotice}
      <div className="shelf-intro">
        <p className="eyebrow">{trashed ? 'Room to reconsider' : archived ? 'Kept for another day' : 'Your private collection'}</p>
        <h1>{trashed ? 'Your trash' : archived ? 'Your archive' : 'Your reading shelf'}</h1>
        <p className="subtitle">{trashed ? 'Stories kept safely until you want them back.' : archived ? 'Good stories, read and kept close.' : 'Good stories, saved for a slower moment.'}</p>
      </div>
      <div className="list-heading">
        <span>{trashed ? 'Trashed articles' : archived ? 'Archived articles' : 'Saved articles'}</span>
        <span>{trashed ? 'Newest trashed first' : archived ? 'Newest archived first' : 'Newest saved first'}</span>
      </div>
      <Separator />
      {articles.length > 0 ? (
        <div className="article-list">
          {articles.map(article => {
            const readerURL = urlWithPagePosition(`/articles/${encodeURIComponent(article.id)}`, data, data.view)
            const timestamp = trashed ? article.trashed_at! : archived ? article.archived_at! : article.created_at
            return (
              <article className="feed-row" key={article.id}>
                <a href={readerURL} className="thumbnail-link" tabIndex={-1} aria-hidden="true">
                  <Thumbnail article={article} />
                </a>
                <div className="article-introduction">
                  <p className="article-meta">
                    <span className="source">{sourceHostname(article.url)}</span>
                    <span aria-hidden="true">·</span>
                    <span>
                      {trashed ? 'Trashed' : archived ? 'Archived' : 'Saved'}{' '}
                      <time dateTime={new Date(timestamp).toISOString()}>{formatDate(timestamp)}</time>
                    </span>
                  </p>
                  <h2><a href={readerURL}>{article.title}</a></h2>
                  <p className="summary">{article.summary}</p>
                  <a className="reading-link" href={readerURL}>Read article <Arrow /></a>
                </div>
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
  return (
    <main id="main" tabIndex={-1} className="gate page-width">
      {saveNotice}
      <p className="eyebrow">A quiet corner of the internet</p>
      <h1>{todo ? 'To-do' : 'Your reading shelf'}</h1>
      <p className="subtitle">{todo ? 'Your private checklist.' : 'Good stories, saved for a slower moment.'}</p>
      <Separator />
      <h2>{data.configured ? 'Make yourself at home.' : 'Your shelf is being prepared.'}</h2>
      {data.configured
        ? <p>{todo ? 'Sign in to view your tasks.' : 'Sign in to read your private collection of stories, ideas, and things worth keeping.'}</p>
        : <p>Newsfeed identity is not configured. Your account administrator can finish the setup.</p>}
      {data.configured && (
        <Button asChild><a href={data.sign_in_url}>Sign in <Arrow /></a></Button>
      )}
    </main>
  )
}

export function App({ data, actionsPending = false, saveNotice, focusHash, todoComposer }: {
  data: Bootstrap; actionsPending?: boolean; saveNotice?: ReactNode; focusHash?: string; todoComposer?: TodoComposer
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
            {data.section === 'todo'
              ? data.todo && <Todo list={data.todo} csrf={data.csrf} canWrite={data.can_write}
                pending={actionsPending} composer={todoComposer} saveNotice={saveNotice} />
              : data.section === 'jobs'
                ? <Opportunities data={data} actionsPending={actionsPending} saveNotice={saveNotice} focusHash={focusHash} />
                : data.view === 'reader' && data.article
                  ? <Reader data={data} article={data.article} actionsPending={actionsPending} saveNotice={saveNotice} />
                  : <Shelf data={data} saveNotice={saveNotice} />}
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

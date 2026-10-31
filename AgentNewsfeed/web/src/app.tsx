import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { Leaf } from '@/components/leaf'
import { Arrow } from '@/components/arrow'
import { ArticleMarkdown } from '@/components/markdown'
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
  const [failed, setFailed] = useState(false)
  return (
    <div className="article-thumbnail">
      {article.thumbnail_url && !failed ? (
        <img
          src={`/thumbnails/${encodeURIComponent(article.id)}`}
          alt=""
          loading="lazy"
          width="312"
          height="184"
          onError={() => setFailed(true)}
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
  const archiveSelected = data.view === 'archive' || (data.view === 'reader' && data.from_archive)
  return (
    <header className="site-header">
      <div className="header-inner">
        <a className="brand" href="/" aria-label="Newsfeed home"><Leaf /><span>Newsfeed</span></a>
        <span className="private-label">Private</span>
        {data.authenticated && (
          <>
            <nav aria-label="Library">
              <a href="/" aria-current={!archiveSelected ? 'page' : undefined}>Feed</a>
              <a href="/archive" aria-current={archiveSelected ? 'page' : undefined}>Archive</a>
            </nav>
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

function Shelf({ data }: { data: Bootstrap }) {
  const archived = data.view === 'archive'
  const articles = data.page?.articles ?? []
  const shelfPath = archived ? '/archive' : '/'
  return (
    <main id="main" className="shelf page-width">
      <div className="shelf-intro">
        <p className="eyebrow">{archived ? 'Kept for another day' : 'Your private collection'}</p>
        <h1>{archived ? 'Your archive' : 'Your reading shelf'}</h1>
        <p className="subtitle">{archived ? 'Good stories, read and kept close.' : 'Good stories, saved for a slower moment.'}</p>
      </div>
      <div className="list-heading">
        <span>{archived ? 'Archived articles' : 'Saved articles'}</span>
        <span>{archived ? 'Newest archived first' : 'Newest saved first'}</span>
      </div>
      <Separator />
      {articles.length > 0 ? (
        <div className="article-list">
          {articles.map(article => {
            const readerURL = `/articles/${encodeURIComponent(article.id)}?from=${archived ? 'archive' : 'feed'}`
            const timestamp = archived ? article.archived_at! : article.created_at
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
                      {archived ? 'Archived' : 'Saved'}{' '}
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
          <h2>{archived ? 'Room for stories you have read.' : 'A little space for a good story.'}</h2>
          <p>{archived ? 'Articles you archive will be waiting here. Restore one whenever you want to return to it.' : 'Save a sourced article with your connected agent, then settle in here when you have a moment.'}</p>
          {archived && <a className="reading-link" href="/">Back to your feed <Arrow /></a>}
        </div>
      )}
      {data.page?.next_cursor && (
        <div className="pagination">
          <Button asChild variant="outline">
            <a href={`${shelfPath}?cursor=${encodeURIComponent(data.page.next_cursor)}`}>
              {archived ? 'Earlier archived articles' : 'Older articles'} <Arrow />
            </a>
          </Button>
        </div>
      )}
    </main>
  )
}

function Reader({ data, article }: { data: Bootstrap; article: Article }) {
  const archived = Boolean(article.archived_at)
  const action = archived ? 'restore' : 'archive'
  return (
    <main id="main" className="reader page-width">
      <a className="back-link" href={data.from_archive ? '/archive' : '/'}>
        <Arrow direction="left" /> Back to {data.from_archive ? 'archive' : 'feed'}
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
          {archived && <p className="archive-note">Archived {formatDate(article.archived_at!)}. Kept here for another day.</p>}
        </header>
        <Separator />
        {article.content_markdown ? (
          <ArticleMarkdown content={article.content_markdown} sourceURL={article.url} />
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
            <p>{archived ? 'Bring this story back to your reading shelf.' : 'Finished for now? Keep this story in your archive.'}</p>
            <form method="post" action={`/api/v1/articles/${encodeURIComponent(article.id)}/${action}`}>
              <input type="hidden" name="csrf_token" value={data.csrf} />
              <Button variant="outline" type="submit" disabled={!data.can_write}>
                {archived ? 'Restore to feed' : 'Archive article'}
              </Button>
            </form>
          </div>
          {!data.can_write && (
            <p className="permission-note" role="status">Archiving is unavailable with this sign-in. Sign in again after your account has write permission.</p>
          )}
        </footer>
      </article>
    </main>
  )
}

function Gate({ data }: { data: Bootstrap }) {
  return (
    <main id="main" className="gate page-width">
      <p className="eyebrow">A quiet corner of the internet</p>
      <h1>Your reading shelf</h1>
      <p className="subtitle">Good stories, saved for a slower moment.</p>
      <Separator />
      <h2>{data.configured ? 'Make yourself at home.' : 'Your shelf is being prepared.'}</h2>
      <p>{data.configured ? 'Sign in to read your private collection of stories, ideas, and things worth keeping.' : 'Newsfeed identity is not configured. Your account administrator can finish the setup.'}</p>
      {data.configured && (
        <Button asChild><a href={data.sign_in_url}>Sign in <Arrow /></a></Button>
      )}
    </main>
  )
}

export function App({ data }: { data: Bootstrap }) {
  return (
    <>
      <a className="skip-link" href="#main">Skip to reading</a>
      <Header data={data} />
      {!data.authenticated ? (
        <Gate data={data} />
      ) : data.view === 'reader' && data.article ? (
        <Reader data={data} article={data.article} />
      ) : (
        <Shelf data={data} />
      )}
      <footer className="site-footer page-width">
        <Separator />
        <div><Leaf /><span>Your articles. Your space.</span></div>
      </footer>
    </>
  )
}

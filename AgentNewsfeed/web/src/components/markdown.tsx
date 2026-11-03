import { useState } from 'react'
import Markdown, { defaultUrlTransform } from 'react-markdown'

function ArticleImage({ src, alt, title }: { src: string; alt: string; title?: string }) {
  const [failed, setFailed] = useState(false)
  return src && !failed
    ? <img src={src} alt={alt} title={title} loading="lazy" decoding="async" referrerPolicy="no-referrer" onError={() => setFailed(true)} />
    : <span className="article-image-fallback">{alt || 'Image unavailable'}</span>
}

// Never enable raw HTML. Images must match the server's saved-content manifest;
// an unknown destination renders its alt text without any browser request.
export function ArticleMarkdown({ content, sourceURL, images = {} }: { content: string; sourceURL: string; images?: Record<string, string> }) {
  return <div className="article-prose"><Markdown
    skipHtml
    urlTransform={(value, key) => {
      if (key === 'src') {
        const route = Object.hasOwn(images, value) ? images[value] : ''
        return /^\/thumbnails\/[^/?#]+\/content\/[a-f0-9]{64}(?:\?trashed=true)?$/.test(route) ? route : ''
      }
      const safe = defaultUrlTransform(value)
      if (!safe) return ''
      try {
        // Source-relative references belong to the publication, including
        // fragment links; the reader does not serve the source's URL tree.
        return new URL(safe, sourceURL).href
      } catch {
        return ''
      }
    }}
    components={{
      a: ({ href, children }) => href
        ? <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>
        : <span>{children}</span>,
      img: ({ src, alt, title }) => <ArticleImage key={typeof src === 'string' ? src : ''} src={typeof src === 'string' ? src : ''} alt={alt ?? ''} title={title} />,
    }}
  >{content}</Markdown></div>
}

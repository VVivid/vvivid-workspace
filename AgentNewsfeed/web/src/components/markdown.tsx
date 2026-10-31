import Markdown, { defaultUrlTransform } from 'react-markdown'

// The reader is text-only apart from the separately protected list thumbnail.
// Never enable raw HTML plugins or inline Markdown images here.
export function ArticleMarkdown({ content, sourceURL }: { content: string; sourceURL: string }) {
  return <div className="article-prose"><Markdown
    skipHtml
    disallowedElements={['img']}
    urlTransform={(value) => {
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
    }}
  >{content}</Markdown></div>
}

export interface Article {
  id: string
  url: string
  title: string
  summary: string
  thumbnail_url?: string
  revision: number
  created_at: number
  updated_at: number
  content_markdown?: string
  archived_at?: number
}

export interface Page {
  articles: Article[]
  next_cursor?: string
}

export interface Bootstrap {
  configured: boolean
  authenticated: boolean
  can_write: boolean
  csrf: string
  view: 'feed' | 'archive' | 'reader'
  from_archive: boolean
  sign_in_url: string
  page?: Page
  article?: Article
}

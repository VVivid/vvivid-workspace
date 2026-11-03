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
  trashed_at?: number
  thumbnail_status?: 'legacy' | 'stored' | 'unavailable' | 'budget_exceeded'
  thumbnail_hash?: string
  thumbnail_width?: number
  thumbnail_height?: number
  thumbnail_bytes?: number
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
  view: 'feed' | 'archive' | 'trash' | 'reader'
  from_archive: boolean
  from_trash?: boolean
  mutation_key?: string
  sign_in_url: string
  page?: Page
  article?: Article
  content_images?: Record<string, string>
}

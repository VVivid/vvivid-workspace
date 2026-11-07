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

export interface PageLinks {
  previous_cursor?: string
  next_cursor?: string
}

export interface Page extends PageLinks {
  articles: Article[]
}

export interface Opportunity {
  id: string
  kind: 'job' | 'internship'
  url: string
  title: string
  company: string
  location?: string
  workplace?: 'remote' | 'hybrid' | 'on_site'
  employment_type?: 'full_time' | 'part_time' | 'contract' | 'temporary' | 'seasonal' | 'other'
  employment_period?: string
  compensation?: string
  deadline?: string
  tags?: string[]
  source_description?: string
  agent_note?: string
  revision: number
  created_at: number
  updated_at: number
  archived_at?: number
  trashed_at?: number
}

export interface OpportunitiesPage extends PageLinks {
  opportunities: Opportunity[]
}

export interface TodoTask {
  id: string
  title: string
  tags?: string[]
  estimate_minutes?: number
  revision: number
  completed_at?: number
}

export interface TodoList {
  tasks: TodoTask[]
  undo?: TodoTask
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
  section?: 'news' | 'jobs' | 'todo'
  cursor?: string
  before?: string
  page_size?: number
  detail_selected?: boolean
  opportunities_page?: OpportunitiesPage
  opportunity?: Opportunity
  todo?: TodoList
}

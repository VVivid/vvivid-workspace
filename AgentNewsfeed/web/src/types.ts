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
  section?: 'news' | 'todo' | 'settings'
  cursor?: string
  before?: string
  page_size?: number
  todo?: TodoList
  settings?: SettingsPage
}

export interface Interest {
  name: string
  preference: 'more' | 'occasionally' | 'avoid'
}

export interface SourcePreference {
  domain: string
  preference: 'prefer' | 'allow' | 'block'
}

export interface RecommendationPreferences {
  interests: Interest[]
  reading_brief: string
  avoid: string
  sources: SourcePreference[]
  allow_new_sources: boolean
  allow_suggestions: boolean
  discovery: {
    enabled: boolean
    max_per_run: number
    max_per_day: number
    exploration: 'familiar' | 'balanced' | 'adventurous'
    publication_days: number
    include_evergreen: boolean
  }
}
export interface PreferencesState {
  revision: number
  updated_at: number
  preferences: RecommendationPreferences
}

export interface RecommendationSuggestion {
  id: string
  revision: number
  status: 'pending' | 'accepted' | 'dismissed'
  kind: 'interest' | 'source'
  name: string
  preference: string
  reason: string
  evidence_urls: string[]
  preferences_revision: number
  created_at: number
  resolved_at: number
}

export interface DiscoveryRun {
  id: string
  preferences_revision: number
  status: 'active' | 'completed' | 'failed' | 'expired'
  started_at: number
  expires_at: number
  finished_at: number
  saved_count: number
  note: string
}

export interface DiscoveryRunDetail {
  run: DiscoveryRun
  articles: {
    article_id: string
    url: string
    title: string
    why_match: string
    published_at: number
    evergreen: boolean
    saved_at: number
  }[]
}

export interface SettingsPage {
  preferences: PreferencesState
  suggestions: RecommendationSuggestion[]
  discovery: {
    preferences: PreferencesState
    recent_urls: string[]
    runs: DiscoveryRun[]
    day: string
    day_timezone: string
    saved_today: number
    remaining_today: number
  }
  runs: {
    runs: DiscoveryRun[]
    next_before?: number
  }
  selected_run?: DiscoveryRunDetail
}

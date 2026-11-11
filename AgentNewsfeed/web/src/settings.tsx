import { useEffect, useRef, useState } from 'react'
import type { KeyboardEvent, ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import type { Bootstrap, Interest, RecommendationPreferences, RecommendationSuggestion, SourcePreference } from '@/types'
import './settings.css'

export interface SettingsDraft {
  revision: number
  preferences: RecommendationPreferences
  key: string
  csrf: string
}

export interface SettingsEditor {
  draft: SettingsDraft | null
  onDraft: (draft: SettingsDraft | null) => void
  onSaved: (navigationSequence: number) => void
  onRefresh: () => void
  write: (path: string, payload: object) => Promise<number>
}

const tabs = ['Interests', 'Sources', 'Discovery', 'Suggestions', 'Run history'] as const
const options = {
  more: 'More',
  occasionally: 'Occasionally',
  avoid: 'Avoid',
  prefer: 'Prefer',
  allow: 'Allow',
  block: 'Block',
}

function date(value: number) {
  return new Intl.DateTimeFormat('en', {
    dateStyle: 'medium',
    timeStyle: 'short',
    timeZone: 'America/New_York',
  }).format(value)
}

export function Settings({ data, editor, pending = false, saveNotice }: {
  data: Bootstrap
  editor: SettingsEditor
  pending?: boolean
  saveNotice?: ReactNode
}) {
  const settings = data.settings!
  const [tab, setTab] = useState<(typeof tabs)[number]>(() =>
    settings.selected_run || new URLSearchParams(location.search).has('before') ? 'Run history' : 'Interests')
  const [topic, setTopic] = useState('')
  const [source, setSource] = useState('')
  const [notice, setNotice] = useState('')
  const [problem, setProblem] = useState('')
  const [busy, setBusy] = useState(false)
  const [retry, setRetry] = useState<{ path: string; payload: object } | null>(null)
  const actionKeys = useRef(new Map<string, string>())
  const noticeRef = useRef<HTMLDivElement>(null)
  const draft = editor.draft?.csrf === data.csrf ? editor.draft : null
  const preferences = draft?.preferences ?? settings.preferences.preferences
  const dirty = draft !== null
  const readonly = !data.can_write || busy || pending

  useEffect(() => {
    if (problem) noticeRef.current?.focus()
  }, [problem])

  function change(next: RecommendationPreferences) {
    if (readonly) return
    // A changed draft is a new intent; an unchanged retry keeps this key.
    editor.onDraft({
      revision: draft?.revision ?? settings.preferences.revision,
      preferences: next,
      key: crypto.randomUUID(),
      csrf: data.csrf,
    })
    setNotice('Unsaved changes')
    setProblem('')
    setRetry(null)
  }

  function discovery(next: Partial<RecommendationPreferences['discovery']>) {
    change({ ...preferences, discovery: { ...preferences.discovery, ...next } })
  }

  function key(intent: string) {
    if (!actionKeys.current.has(intent)) actionKeys.current.set(intent, crypto.randomUUID())
    return actionKeys.current.get(intent)!
  }

  async function write(path: string, payload: object) {
    if (readonly) return
    setBusy(true)
    setProblem('')
    setNotice('Saving…')
    try {
      const navigationSequence = await editor.write(path, payload)
      editor.onDraft(null)
      setRetry(null)
      setNotice('Saved.')
      // Refresh is a separate read. A failed refresh must not repeat the write.
      editor.onSaved(navigationSequence)
    } catch (error) {
      setProblem(error instanceof Error ? error.message : 'The change could not be confirmed.')
      setRetry({ path, payload })
      setNotice('')
    } finally {
      setBusy(false)
    }
  }

  function save() {
    if (readonly || !draft || draft.revision !== settings.preferences.revision) return
    void write('/api/v1/settings/preferences', {
      idempotency_key: draft.key,
      expected_revision: draft.revision,
      preferences,
    })
  }

  function decide(suggestion: RecommendationSuggestion, accept: boolean) {
    if (readonly || dirty) return
    const action = accept ? 'accept' : 'dismiss'
    const intent = `${action}:${suggestion.id}:${suggestion.revision}:${settings.preferences.revision}`
    void write(`/api/v1/settings/suggestions/${action}`, {
      idempotency_key: key(intent),
      suggestion_id: suggestion.id,
      expected_revision: suggestion.revision,
      expected_preferences_revision: settings.preferences.revision,
    })
  }

  function chooseTab(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const offset = event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 1
    if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + offset + tabs.length) % tabs.length
    setTab(tabs[next])
    document.getElementById(`settings-tab-${next}`)?.focus()
  }

  function addTopic() {
    if (readonly || preferences.interests.length >= 50) return
    const name = topic.trim().replace(/\s+/g, ' ')
    if (!name || preferences.interests.some(item => item.name.toLowerCase() === name.toLowerCase())) {
      setProblem('Enter a new interest that is not already listed.')
      return
    }
    change({ ...preferences, interests: [...preferences.interests, { name, preference: 'more' }] })
    setTopic('')
  }

  function addSource() {
    if (readonly || preferences.sources.length >= 50) return
    const domain = source.trim().toLowerCase()
    if (!/^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z][a-z0-9-]*$/.test(domain) || preferences.sources.some(item => item.domain === domain)) {
      setProblem('Enter a new exact domain such as example.com, without a URL or path.')
      return
    }
    change({ ...preferences, sources: [...preferences.sources, { domain, preference: 'allow' }] })
    setSource('')
  }

  const selected = tabs.indexOf(tab)
  return <main id="main" tabIndex={-1} className="settings page-width" aria-busy={busy || pending}>
    {saveNotice}
    <div className="settings-title">
      <div>
        <h1>Reading preferences</h1>
        <p>Guide what your agent finds for you.</p>
      </div>
      <Button type="button" disabled={readonly || !dirty || draft?.revision !== settings.preferences.revision} onClick={save}>
        Save changes
      </Button>
    </div>
    {!data.can_write && <p role="status">Settings are read-only with this sign-in. Sign in again after your account has write permission.</p>}
    <div className="settings-layout">
      <nav className="settings-tabs" role="tablist" aria-label="Recommendation settings">
        {tabs.map((name, index) => <button type="button" role="tab" id={`settings-tab-${index}`} key={name}
          aria-controls="settings-panel" aria-selected={tab === name} tabIndex={tab === name ? 0 : -1}
          onClick={() => setTab(name)} onKeyDown={event => chooseTab(event, index)}>
          {name}{name === 'Suggestions' && ` (${settings.suggestions.length})`}
        </button>)}
      </nav>
      <div>
        <section id="settings-panel" role="tabpanel" aria-labelledby={`settings-tab-${selected}`}>
          {tab === 'Interests' && <>
            <h2>What you want to read</h2>
            <p className="settings-hint">Set a preference for each topic. Start with your own interests.</p>
            {preferences.interests.length === 0 && <p className="settings-empty">No interests yet.</p>}
            <div className="settings-rows">
              {preferences.interests.map((item, index) => <div className="settings-row" key={item.name}>
                <span>{item.name}</span>
                <div>
                  <select aria-label={`Preference for ${item.name}`} value={item.preference} disabled={readonly}
                    onChange={event => change({
                      ...preferences,
                      interests: preferences.interests.map((value, i) => i === index
                        ? { ...value, preference: event.target.value as Interest['preference'] } : value),
                    })}>
                    {(['more', 'occasionally', 'avoid'] as const).map(value => <option key={value} value={value}>{options[value]}</option>)}
                  </select>
                  <button type="button" disabled={readonly} aria-label={`Remove ${item.name}`}
                    onClick={() => change({ ...preferences, interests: preferences.interests.filter((_, i) => i !== index) })}>
                    Remove
                  </button>
                </div>
              </div>)}
            </div>
            <form className="settings-add" onSubmit={event => {
              event.preventDefault()
              addTopic()
            }}>
              <input aria-label="New interest" placeholder="Add a topic…" maxLength={80} value={topic}
                disabled={readonly || preferences.interests.length >= 50} onChange={event => setTopic(event.target.value)} required />
              <button disabled={readonly || preferences.interests.length >= 50}>Add interest</button>
            </form>
            <label className="settings-field">
              Reading brief
              <textarea maxLength={4000} value={preferences.reading_brief} disabled={readonly}
                onChange={event => change({ ...preferences, reading_brief: event.target.value })}
                placeholder="What makes a useful article for you?" />
            </label>
            <label className="settings-field">
              Avoid
              <textarea maxLength={4000} value={preferences.avoid} disabled={readonly}
                onChange={event => change({ ...preferences, avoid: event.target.value })}
                placeholder="Topics, formats, or coverage you want less of…" />
            </label>
          </>}

          {tab === 'Sources' && <>
            <h2>Sources</h2>
            <p className="settings-hint">Prefer trusted sources, allow others, or block an exact domain. Subdomains and www are separate hosts.</p>
            {preferences.sources.length === 0 && <p className="settings-empty">No sources yet.</p>}
            <div className="settings-rows">
              {preferences.sources.map((item, index) => <div className="settings-row" key={item.domain}>
                <span>{item.domain}</span>
                <div>
                  <select aria-label={`Preference for ${item.domain}`} value={item.preference} disabled={readonly}
                    onChange={event => change({
                      ...preferences,
                      sources: preferences.sources.map((value, i) => i === index
                        ? { ...value, preference: event.target.value as SourcePreference['preference'] } : value),
                    })}>
                    {(['prefer', 'allow', 'block'] as const).map(value => <option key={value} value={value}>{options[value]}</option>)}
                  </select>
                  <button type="button" disabled={readonly} aria-label={`Remove ${item.domain}`}
                    onClick={() => change({ ...preferences, sources: preferences.sources.filter((_, i) => i !== index) })}>
                    Remove
                  </button>
                </div>
              </div>)}
            </div>
            <form className="settings-add" onSubmit={event => {
              event.preventDefault()
              addSource()
            }}>
              <input aria-label="Source domain" placeholder="example.com" maxLength={253} value={source}
                disabled={readonly || preferences.sources.length >= 50} onChange={event => setSource(event.target.value)} required />
              <button disabled={readonly || preferences.sources.length >= 50}>Add source</button>
            </form>
            <label className="settings-check">
              <input type="checkbox" checked={preferences.allow_new_sources} disabled={readonly}
                onChange={event => change({ ...preferences, allow_new_sources: event.target.checked })} />
              Allow discovery beyond these sources
            </label>
            <p className="settings-hint">When off, only listed Prefer or Allow domains can be saved by discovery. Block always wins for that exact host.</p>
          </>}

          {tab === 'Discovery' && <>
            <h2>Discovery</h2>
            <p className="settings-hint">Control your hourly agent search and how much it adds.</p>
            <label className="settings-check">
              <input type="checkbox" checked={preferences.discovery.enabled} disabled={readonly}
                onChange={event => discovery({ enabled: event.target.checked })} />
              Enable hourly discovery
            </label>
            <p className="settings-hint">Discovery starts disabled. A connected external agent runs the search; saving this setting does not install a scheduler.</p>
            <div className="settings-two-fields">
              <label className="settings-field">
                Maximum articles per run
                <input type="number" min={1} max={10} value={preferences.discovery.max_per_run} disabled={readonly}
                  onChange={event => discovery({ max_per_run: Number(event.target.value) })} />
              </label>
              <label className="settings-field">
                Maximum articles per day
                <input type="number" min={1} max={50} value={preferences.discovery.max_per_day} disabled={readonly}
                  onChange={event => discovery({ max_per_day: Number(event.target.value) })} />
              </label>
            </div>
            <p className="settings-hint">Daily budget resets at midnight in America/New_York. {settings.discovery.saved_today} saved today, {settings.discovery.remaining_today} remaining under saved settings.</p>
            <div className="settings-two-fields">
              <label className="settings-field">
                Topic exploration
                <select value={preferences.discovery.exploration} disabled={readonly}
                  onChange={event => discovery({ exploration: event.target.value as RecommendationPreferences['discovery']['exploration'] })}>
                  <option value="familiar">Mostly my interests</option>
                  <option value="balanced">A mix of familiar and new</option>
                  <option value="adventurous">More new topics</option>
                </select>
              </label>
              <label className="settings-field">
                Publication age
                <select value={preferences.discovery.publication_days} disabled={readonly}
                  onChange={event => discovery({ publication_days: Number(event.target.value) })}>
                  <option value={1}>Past 24 hours</option>
                  <option value={7}>Past week</option>
                  <option value={30}>Past month</option>
                  <option value={0}>Any age</option>
                </select>
              </label>
            </div>
            <label className="settings-check">
              <input type="checkbox" checked={preferences.discovery.include_evergreen} disabled={readonly}
                onChange={event => discovery({ include_evergreen: event.target.checked })} />
              Include older articles with lasting value
            </label>
            <p className="settings-hint">Unknown dates stay unknown. With an age filter, the agent must identify lasting value for older or undated articles.</p>
            <p className="settings-check">✓ Always skip URLs already saved in Feed, Archive, or Trash.</p>
            <hr />
            <h2>Agent suggestions</h2>
            <label className="settings-check">
              <input type="checkbox" checked={preferences.allow_suggestions} disabled={readonly}
                onChange={event => change({ ...preferences, allow_suggestions: event.target.checked })} />
              Let agents propose new interests and sources
            </label>
            <p className="settings-hint">Proposals need your explicit Apply action. They never change preferences on their own.</p>
          </>}

          {tab === 'Suggestions' && <>
            <h2>Suggestions from your agent</h2>
            <p className="settings-hint">Review the proposed change, reason and source evidence before applying it.</p>
            {dirty && <p>Save or discard your draft before applying or dismissing a suggestion.</p>}
            {settings.suggestions.length === 0 && <p className="settings-empty">All caught up. No pending suggestions.</p>}
            {settings.suggestions.map(suggestion => <article className="settings-suggestion" key={suggestion.id} aria-labelledby={`suggestion-${suggestion.id}`}>
              <h3 id={`suggestion-${suggestion.id}`}>{suggestion.kind === 'interest' ? 'Interest' : 'Source'} · {suggestion.name} · {options[suggestion.preference as keyof typeof options]}</h3>
              <p>{suggestion.reason}</p>
              <ul>
                {suggestion.evidence_urls.map(url => <li key={url}>
                  <a href={url} target="_blank" rel="noopener noreferrer">{url}</a>
                </li>)}
              </ul>
              <div>
                <button type="button" aria-label={`Apply ${suggestion.name}`} disabled={readonly || dirty}
                  onClick={() => decide(suggestion, true)}>Apply</button>
                <button type="button" aria-label={`Dismiss ${suggestion.name}`} disabled={readonly || dirty}
                  onClick={() => decide(suggestion, false)}>Dismiss</button>
              </div>
            </article>)}
          </>}

          {tab === 'Run history' && <>
            <h2>Discovery run history</h2>
            <p className="settings-hint">Private search outcomes and saved matches. Dates use America/New_York.</p>
            {settings.runs.runs.length === 0 && <p className="settings-empty">No discovery runs yet.</p>}
            {settings.runs.runs.map(run => <article className="settings-run" key={run.id}>
              <a href={`/settings?run=${encodeURIComponent(run.id)}`}>{date(run.started_at)} · {run.status}</a>
              <span>{run.saved_count} saved</span>
              {run.note && <p>{run.note}</p>}
            </article>)}
            {settings.runs.next_before && <a href={`/settings?before=${settings.runs.next_before}`}>Older runs</a>}
            {new URLSearchParams(location.search).has('before') && <p><a href="/settings">Latest runs</a></p>}
            {settings.selected_run && <section className="settings-run-detail">
              <h3>{date(settings.selected_run.run.started_at)} · {settings.selected_run.run.status}</h3>
              <p>{settings.selected_run.run.saved_count} saved · preference revision {settings.selected_run.run.preferences_revision}</p>
              <p>{settings.selected_run.run.note}</p>
              {settings.selected_run.articles.map(item => <article className="settings-suggestion" key={item.article_id}>
                <a href={item.url} target="_blank" rel="noopener noreferrer">{item.title}</a>
                <p>{item.why_match}</p>
                <p className="settings-hint">Published: {item.published_at ? date(item.published_at) : 'Unknown'}{item.evergreen ? ' · Lasting value' : ''}</p>
              </article>)}
              {settings.selected_run.articles.length === 0 && <p>No articles saved in this run.</p>}
            </section>}
          </>}
        </section>

        <div className="settings-status" ref={noticeRef} tabIndex={-1}>
          {problem && <p role="alert">{problem}</p>}
          {retry && <button type="button" disabled={readonly} onClick={() => void write(retry.path, retry.payload)}>Retry same request</button>}
          {problem && <button type="button" disabled={busy || pending} onClick={() => editor.onRefresh()}>Reload current settings</button>}
          <p role="status" aria-live="polite">{notice || (dirty ? 'Unsaved changes' : `Saved settings · Revision ${settings.preferences.revision}`)}</p>
          {dirty && <button type="button" disabled={busy || pending} onClick={() => {
            editor.onDraft(null)
            setProblem('')
            setNotice('Draft discarded')
            setRetry(null)
          }}>Discard draft</button>}
          {dirty && draft.revision !== settings.preferences.revision && <p role="alert">Saved preferences changed. Your draft is kept for reference; discard it to edit the current revision.</p>}
        </div>
      </div>
    </div>
  </main>
}

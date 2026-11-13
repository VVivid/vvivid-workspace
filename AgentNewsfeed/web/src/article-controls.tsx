import { useLayoutEffect, useRef, useState } from 'react'
import type { ChangeEvent, FormEvent, KeyboardEvent, ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { validPageSize } from '@/pagination'

export type BrowseArticles = (query: string, pageSize: number, timing?: 'immediate' | 'debounced') => void
export type ArticleBrowseDraft = { query: string; pageSize: number }
const presets = [10, 20, 50, 100]

// Go 1.26.9 uses Unicode 15.0. These Unicode 16/17 lowercase additions must
// stay literal. Recheck this list against unicode/tables.go when upgrading Go.
const newerLowercaseMappings = /[\u1c89\ua7cb\ua7cc\ua7ce\ua7d2\ua7d4\ua7da\ua7dc\u{10d50}-\u{10d65}\u{16ea0}-\u{16eb8}]/u

function articleSearchText(text: string) {
  let value = ''
  const originalStarts: number[] = []
  const originalEnds: number[] = []
  let originalOffset = 0
  for (const character of text) {
    // Match Go's simple lowercase: no contextual final sigma or dotted-I expansion.
    const lower = character === 'İ' ? 'i' : newerLowercaseMappings.test(character) ? character : character.toLowerCase()
    value += lower
    for (let index = 0; index < lower.length; index++) {
      originalStarts.push(originalOffset)
      originalEnds.push(originalOffset + character.length)
    }
    originalOffset += character.length
  }
  return { value, originalStarts, originalEnds }
}

function articleMatches(text: string, query?: string) {
  // Bootstrap queries already have whitespace collapsed to ASCII spaces by Go.
  const terms = query ? articleSearchText(query).value.split(' ').filter(Boolean) : []
  const matches: { start: number; end: number }[] = []
  if (!terms.length) return matches
  // Literal index searches keep user punctuation out of regular expressions.
  const searchable = articleSearchText(text)
  let searchOffset = 0
  while (searchOffset < searchable.value.length) {
    let matchStart = searchable.value.length
    let matchLength = 0
    for (const term of terms) {
      const candidateStart = searchable.value.indexOf(term, searchOffset)
      if (candidateStart >= 0 && (candidateStart < matchStart || (candidateStart === matchStart && term.length > matchLength))) {
        matchStart = candidateStart
        matchLength = term.length
      }
    }
    if (!matchLength) break
    matches.push({
      start: searchable.originalStarts[matchStart],
      end: searchable.originalEnds[matchStart + matchLength - 1],
    })
    searchOffset = matchStart + matchLength
  }
  return matches
}

function characterBoundary(text: string, offset: number) {
  const codeUnit = text.charCodeAt(offset)
  return codeUnit >= 0xdc00 && codeUnit <= 0xdfff ? offset - 1 : offset
}

function articleExcerpt(text: string, query?: string) {
  if (!query) return text
  const firstMatch = articleMatches(text, query)[0]
  // Leading context can hide a match below the compact two-line summary.
  const excerptStart = firstMatch?.start ?? 0
  // Keep a long first match whole so Highlight can still recognize it.
  const excerptLimit = Math.max(excerptStart + 260, firstMatch?.end ?? 0)
  const excerptEnd = characterBoundary(text, Math.min(text.length, excerptLimit))
  return `${excerptStart ? '…' : ''}${text.slice(excerptStart, excerptEnd)}${text.length > excerptEnd ? '…' : ''}`
}

export function Highlight({ text: original, query, excerpt = false }: { text: string; query?: string; excerpt?: boolean }) {
  const text = excerpt ? articleExcerpt(original, query) : original
  const matches = articleMatches(text, query)
  if (!matches.length) return text
  const parts: ReactNode[] = []
  let position = 0
  for (const { start, end } of matches) {
    parts.push(text.slice(position, start))
    parts.push(<mark key={start}>{text.slice(start, end)}</mark>)
    position = end
  }
  parts.push(text.slice(position))
  return <>{parts}</>
}

export function ArticleControls({ path, query = '', pageSize: loadedPageSize, browseDraft, onBrowse }: {
  path: string; query?: string; pageSize: number; browseDraft?: ArticleBrowseDraft; onBrowse?: BrowseArticles
}) {
  const [localSearch, setLocalSearch] = useState({ loadedQuery: query, draft: query })
  // SPA navigation owns both live controls; standalone/native forms keep local drafts.
  const searchDraft = browseDraft?.query ?? localSearch.draft
  if (localSearch.loadedQuery !== query) setLocalSearch({ loadedQuery: query, draft: query })
  const [sizeSelection, setSizeSelection] = useState({ loadedQuery: query, loadedSize: loadedPageSize, selectedSize: loadedPageSize })
  const selectedPageSize = browseDraft?.pageSize ?? sizeSelection.selectedSize
  // Keep a pending choice usable while loading, then accept navigation/history's
  // completed query and size before committing its controls to the screen.
  if (sizeSelection.loadedQuery !== query || sizeSelection.loadedSize !== loadedPageSize) {
    setSizeSelection({ loadedQuery: query, loadedSize: loadedPageSize, selectedSize: loadedPageSize })
  }
  const [editingCustomSize, setEditingCustomSize] = useState(false)
  const [customSizeDraft, setCustomSizeDraft] = useState('25')
  const [customSizeError, setCustomSizeError] = useState('')
  const search = useRef<HTMLInputElement>(null)
  const sizeSelect = useRef<HTMLSelectElement>(null)
  const customInput = useRef<HTMLInputElement>(null)
  useLayoutEffect(() => {
    if (editingCustomSize) {
      customInput.current?.focus()
      customInput.current?.select()
    }
  }, [editingCustomSize])

  function applyBrowse(nextQuery: string, nextSize = selectedPageSize) {
    setSizeSelection({ loadedQuery: query, loadedSize: loadedPageSize, selectedSize: nextSize })
    onBrowse?.(nextQuery, nextSize)
  }
  function clearSearch() {
    setLocalSearch({ loadedQuery: query, draft: '' })
    applyBrowse('')
    search.current?.focus()
  }
  function submitSearch(event: FormEvent<HTMLFormElement>) {
    if (!onBrowse) return
    event.preventDefault()
    applyBrowse(searchDraft)
  }
  function changeSearch(event: ChangeEvent<HTMLInputElement>) {
    const nextQuery = event.target.value
    setLocalSearch({ loadedQuery: query, draft: nextQuery })
    onBrowse?.(nextQuery, selectedPageSize, 'debounced')
  }
  function searchKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key !== 'Escape') return
    event.preventDefault()
    clearSearch()
  }
  function changePageSize(event: ChangeEvent<HTMLSelectElement>) {
    if (event.target.value === 'custom') {
      setCustomSizeDraft(String(presets.includes(selectedPageSize) ? 25 : selectedPageSize))
      setCustomSizeError('')
      setEditingCustomSize(true)
    } else {
      setEditingCustomSize(false)
      applyBrowse(searchDraft, Number(event.target.value))
    }
  }
  function submitCustomSize(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const size = validPageSize(customSizeDraft)
    if (size === undefined) {
      setCustomSizeError('Enter a whole number from 1 to 500.')
      customInput.current?.focus()
      return
    }
    setEditingCustomSize(false)
    setCustomSizeError('')
    applyBrowse(searchDraft, size)
    sizeSelect.current?.focus()
  }
  function changeCustomSize(event: ChangeEvent<HTMLInputElement>) {
    setCustomSizeDraft(event.target.value)
    setCustomSizeError('')
  }
  function cancelCustomSize() {
    setEditingCustomSize(false)
    setCustomSizeError('')
    sizeSelect.current?.focus()
  }

  return <div className="article-controls">
    <form className="shelf-toolbar" method="get" action={path} role="search" onSubmit={submitSearch}>
      <label className="shelf-search" htmlFor="article-search">Search saved news
        <span className="shelf-search-box">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true"><circle cx="10" cy="10" r="6" /><path d="m15 15 5 5" /></svg>
          <input ref={search} id="article-search" type="search" name="q" autoComplete="off" value={searchDraft} maxLength={300} aria-label="Search saved news"
            placeholder="Search by title, topic, or source…" aria-describedby="article-search-hint" onChange={changeSearch} onKeyDown={searchKeyDown} />
          {searchDraft && <button type="button" className="search-clear" onClick={clearSearch}>Clear</button>}
        </span>
      </label>
      <label className="shelf-page-size" htmlFor="article-page-size">Articles per page
        <input type="hidden" name="page_size" value={selectedPageSize} />
        <select ref={sizeSelect} id="article-page-size" value={editingCustomSize || !presets.includes(selectedPageSize) ? 'custom' : selectedPageSize}
          onChange={changePageSize}>
          {presets.map(size => <option key={size} value={size}>{size}</option>)}
          <option value="custom">{presets.includes(selectedPageSize) ? 'Custom…' : `Custom (${selectedPageSize})`}</option>
        </select>
      </label>
    </form>
    <div className="shelf-search-help"><p id="article-search-hint">Titles, introductions &amp; sources · searches across pages</p><span>Remembered on this device</span></div>
    {editingCustomSize && <form className="custom-page-size" noValidate onSubmit={submitCustomSize}>
      <label htmlFor="custom-page-size">Custom page size <span>(1–500)</span>
        <input ref={customInput} id="custom-page-size" type="number" inputMode="numeric" min="1" max="500" step="1"
          value={customSizeDraft} aria-describedby="custom-page-size-error" aria-invalid={Boolean(customSizeError)}
          onChange={changeCustomSize} />
      </label>
      <Button type="submit" size="sm">Apply</Button>
      <Button type="button" variant="outline" size="sm" onClick={cancelCustomSize}>Cancel</Button>
      <span id="custom-page-size-error" role="alert">{customSizeError}</span>
    </form>}
  </div>
}

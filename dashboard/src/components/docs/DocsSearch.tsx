import { useEffect, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent } from 'react'
import { Search, X } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { docsHref, docsPages } from '../../docs/content'
import { customerOperations } from './select'
import { useI18n } from '../../lib/i18n'
import type { Language } from '../../lib/i18n'

type SearchKind = 'page' | 'section' | 'endpoint'

interface SearchItem {
  id: string
  kind: SearchKind
  title: string
  subtitle: string
  href: string
}

const KIND_RANK: Record<SearchKind, number> = { page: 0, section: 1, endpoint: 2 }

function buildIndex(lang: Language): SearchItem[] {
  const pages: SearchItem[] = []
  for (const page of docsPages) {
    pages.push({
      id: `page-${page.slug}`,
      kind: 'page',
      title: page.title[lang],
      subtitle: page.summary[lang],
      href: docsHref(page.slug),
    })
    for (const section of page.sections) {
      pages.push({
        id: `section-${page.slug}-${section.id}`,
        kind: 'section',
        title: section.title[lang],
        subtitle: page.title[lang],
        href: `${docsHref(page.slug)}#${section.id}`,
      })
    }
  }

  const endpoints: SearchItem[] = customerOperations.map((operation) => ({
    id: `endpoint-${operation.operationId}`,
    kind: 'endpoint',
    title: `${operation.method} ${operation.path}`,
    subtitle: operation.summary,
    href: `/docs/reference#${operation.operationId}`,
  }))

  return [...pages, ...endpoints]
}

/** Prefix beats a word prefix, which beats an includes match, which beats a body hit. */
function scoreItem(item: SearchItem, query: string): number {
  const title = item.title.toLowerCase()
  if (title.startsWith(query)) return 0
  if (title.split(/[\s/{}._-]+/).some((word) => word.startsWith(query))) return 1
  if (title.includes(query)) return 2
  if (`${item.subtitle} ${item.kind}`.toLowerCase().includes(query)) return 3
  return -1
}

export function DocsSearch({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t, lang } = useI18n()
  const navigate = useNavigate()
  const inputRef = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)

  const index = useMemo(() => buildIndex(lang), [lang])

  const results = useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (!needle) {
      return index.filter((item) => item.kind === 'page')
    }
    return index
      .map((item, order) => ({ item, order, score: scoreItem(item, needle) }))
      .filter((entry) => entry.score >= 0)
      .sort(
        (left, right) =>
          left.score - right.score ||
          KIND_RANK[left.item.kind] - KIND_RANK[right.item.kind] ||
          left.order - right.order,
      )
      .slice(0, 24)
      .map((entry) => entry.item)
  }, [index, query])

  useEffect(() => {
    if (!open) return
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    setQuery('')
    setActive(0)
    inputRef.current?.focus()
    return () => previous?.focus()
  }, [open])

  useEffect(() => {
    setActive(0)
  }, [query])

  if (!open) return null

  const kindLabel = (kind: SearchKind) =>
    kind === 'page'
      ? t('docs.searchPages')
      : kind === 'section'
        ? t('docs.searchSections')
        : t('docs.searchEndpoints')

  const choose = (item: SearchItem) => {
    navigate(item.href)
    onClose()
  }

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      if (results.length > 0) setActive((current) => (current + 1) % results.length)
      return
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      if (results.length > 0) setActive((current) => (current - 1 + results.length) % results.length)
      return
    }
    if (event.key === 'Enter') {
      const item = results[active] ?? results[0]
      if (item) {
        event.preventDefault()
        choose(item)
      }
      return
    }
    if (event.key === 'Escape') {
      event.preventDefault()
      onClose()
    }
  }

  return (
    <div className="docs-search">
      <div className="docs-search__scrim" aria-hidden="true" onClick={onClose} />
      <div
        className="docs-search__dialog"
        role="dialog"
        aria-modal="true"
        aria-label={t('docs.search')}
      >
        <div className="docs-search__head">
          <Search size={16} aria-hidden="true" />
          <input
            ref={inputRef}
            className="docs-search__input"
            type="text"
            role="combobox"
            aria-expanded="true"
            aria-controls="docs-search-results"
            aria-activedescendant={
              results.length > 0 ? `docs-search-option-${active}` : undefined
            }
            aria-label={t('docs.searchPlaceholder')}
            placeholder={t('docs.searchPlaceholder')}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={handleKeyDown}
            autoComplete="off"
            spellCheck={false}
          />
          <button
            type="button"
            className="docs-icon-btn docs-search__close"
            aria-label={t('docs.closeSearch')}
            onClick={onClose}
          >
            <X size={16} aria-hidden="true" />
          </button>
        </div>
        <div
          className="docs-search__list"
          id="docs-search-results"
          role="listbox"
          aria-label={t('docs.search')}
        >
          {results.length === 0 && (
            <p className="docs-search__empty">{t('docs.searchEmpty', { query: query.trim() })}</p>
          )}
          {results.map((item, index) => (
            <button
              key={item.id}
              id={`docs-search-option-${index}`}
              type="button"
              role="option"
              aria-selected={index === active}
              tabIndex={-1}
              className={`docs-search__option${index === active ? ' is-active' : ''}`}
              onMouseEnter={() => setActive(index)}
              onClick={() => choose(item)}
            >
              <span className="docs-search__title">{item.title}</span>
              <span className="docs-search__kind">{kindLabel(item.kind)}</span>
              {item.subtitle && <span className="docs-search__meta">{item.subtitle}</span>}
            </button>
          ))}
        </div>
        <p className="docs-search__hint">{t('docs.searchHint')}</p>
      </div>
    </div>
  )
}

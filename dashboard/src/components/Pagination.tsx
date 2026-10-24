import clsx from 'clsx'
import { ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight } from 'lucide-react'
import { useCallback, useEffect, useId, useState } from 'react'
import type { FormEvent } from 'react'
import { formatNumber } from '../lib/format'
import { useI18n } from '../lib/i18n'
import { useMediaQuery } from '../lib/useMediaQuery'
import { Button, IconButton, Input, Select } from './primitives'

export const PAGE_SIZE_OPTIONS = [25, 50, 100, 200] as const

export interface UsePaginationOptions {
  total: number
  /** One of PAGE_SIZE_OPTIONS; the gate ring keeps its full 200-row page. */
  initialPageSize?: number
  /**
   * Any change sends the operator back to page 1. Build it from filters,
   * search and sort only — never from live data, so a poll keeps the page.
   */
  resetKey?: string
}

export interface PaginationState {
  page: number
  pageSize: number
  pageCount: number
  /** First visible row, 1-based; 0 when the result set is empty. */
  start: number
  /** Last visible row, inclusive; 0 when the result set is empty. */
  end: number
  setPage: (page: number) => void
  setPageSize: (pageSize: number) => void
  /** The rows that belong on the current page. */
  slice: <T>(rows: T[]) => T[]
}

/**
 * Client-side pagination state shared by every list page.
 *
 * `resetKey` is the filter identity: when it changes the table returns to page
 * 1 immediately (during render, so the old page's slice never flashes). A
 * shrinking live result clamps to the last page instead of stranding the table
 * on an empty one, while growing data keeps the operator where they were.
 */
export function usePagination({
  total,
  initialPageSize = 25,
  resetKey = '',
}: UsePaginationOptions): PaginationState {
  const [page, setPageState] = useState(1)
  const [pageSize, setPageSizeState] = useState(initialPageSize)
  const [lastResetKey, setLastResetKey] = useState(resetKey)

  const pageCount = Math.max(1, Math.ceil(total / pageSize))

  // Adjusting state during render is the documented way to react to a prop
  // change without rendering the previous page once more.
  if (lastResetKey !== resetKey) {
    setLastResetKey(resetKey)
    setPageState(1)
  }

  const currentPage = Math.min(page, pageCount)
  if (currentPage !== page) {
    setPageState(currentPage)
  }

  const setPage = useCallback(
    (next: number) => {
      if (!Number.isFinite(next)) return
      setPageState(Math.min(Math.max(1, Math.trunc(next)), pageCount))
    },
    [pageCount],
  )

  const setPageSize = useCallback((next: number) => {
    if (!Number.isFinite(next) || next <= 0) return
    setPageSizeState(Math.trunc(next))
    setPageState(1)
  }, [])

  const slice = useCallback(
    <T,>(rows: T[]): T[] => rows.slice((currentPage - 1) * pageSize, currentPage * pageSize),
    [currentPage, pageSize],
  )

  const start = total === 0 ? 0 : (currentPage - 1) * pageSize + 1
  const end = total === 0 ? 0 : Math.min(currentPage * pageSize, total)

  return {
    page: currentPage,
    pageSize,
    pageCount,
    start,
    end,
    setPage,
    setPageSize,
    slice,
  }
}

export interface PaginationProps {
  page: number
  pageSize: number
  total: number
  onPageChange: (page: number) => void
  onPageSizeChange: (pageSize: number) => void
  /** Accessible name for this footer; pages pass a surface-specific label. */
  label?: string
  className?: string
}

/**
 * Table footer: `x–y of z`, first/prev/next/last, page jump and page size.
 * It owns no data — the page keeps filter state and only changes the slice.
 * Below 760 px the first/last buttons and the visible size label drop out so
 * the footer stays on one line beside the card-based mobile table.
 */
export function Pagination({
  page,
  pageSize,
  total,
  onPageChange,
  onPageSizeChange,
  label,
  className,
}: PaginationProps) {
  const { t } = useI18n()
  const compact = useMediaQuery('(max-width: 760px)')
  const inputId = useId()
  const errorId = useId()

  const pageCount = Math.max(1, Math.ceil(total / pageSize))
  const currentPage = Math.min(Math.max(1, page), pageCount)
  const start = total === 0 ? 0 : (currentPage - 1) * pageSize + 1
  const end = total === 0 ? 0 : Math.min(currentPage * pageSize, total)

  const [draft, setDraft] = useState(String(currentPage))

  useEffect(() => {
    setDraft(String(currentPage))
  }, [currentPage])

  const trimmed = draft.trim()
  const jumpValue = Number(trimmed)
  const validJump = /^\d+$/.test(trimmed) && jumpValue >= 1 && jumpValue <= pageCount
  // Explain a disabled Go button instead of letting the operator guess.
  const showInvalid = trimmed !== '' && !validJump

  const submitJump = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!validJump) return
    onPageChange(jumpValue)
    setDraft(String(jumpValue))
  }

  if (total === 0) return null

  return (
    <nav
      className={clsx('pagination', compact && 'pagination--compact', className)}
      aria-label={label ?? t('pagination.label')}
    >
      <span className="pagination__range" aria-live="polite">
        {t('pagination.range', {
          start: formatNumber(start),
          end: formatNumber(end),
          total: formatNumber(total),
        })}
      </span>

      <div className="pagination__controls">
        {compact ? null : (
          <IconButton
            size="sm"
            label={t('pagination.first')}
            icon={<ChevronsLeft size={14} aria-hidden="true" />}
            disabled={currentPage <= 1}
            onClick={() => onPageChange(1)}
          />
        )}
        <IconButton
          size="sm"
          label={t('pagination.previous')}
          icon={<ChevronLeft size={14} aria-hidden="true" />}
          disabled={currentPage <= 1}
          onClick={() => onPageChange(currentPage - 1)}
        />
        <span className="pagination__page">
          {t('pagination.pageOf', {
            page: formatNumber(currentPage),
            pages: formatNumber(pageCount),
          })}
        </span>
        <IconButton
          size="sm"
          label={t('pagination.next')}
          icon={<ChevronRight size={14} aria-hidden="true" />}
          disabled={currentPage >= pageCount}
          onClick={() => onPageChange(currentPage + 1)}
        />
        {compact ? null : (
          <IconButton
            size="sm"
            label={t('pagination.last')}
            icon={<ChevronsRight size={14} aria-hidden="true" />}
            disabled={currentPage >= pageCount}
            onClick={() => onPageChange(pageCount)}
          />
        )}
      </div>

      <form className="pagination__jump" onSubmit={submitJump}>
        <label className="sr-only" htmlFor={inputId}>
          {t('pagination.jump')}
        </label>
        <Input
          id={inputId}
          className="pagination__input"
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          autoComplete="off"
          value={draft}
          aria-invalid={showInvalid || undefined}
          aria-describedby={showInvalid ? errorId : undefined}
          onChange={(event) => setDraft(event.target.value)}
          onFocus={(event) => event.currentTarget.select()}
        />
        <Button type="submit" size="sm" variant="ghost" disabled={!validJump}>
          {t('pagination.go')}
        </Button>
      </form>

      <div className="pagination__size">
        {compact ? null : (
          <span className="pagination__size-label">{t('pagination.pageSize')}</span>
        )}
        <Select
          variant="log"
          size="sm"
          aria-label={t('pagination.pageSize')}
          value={String(pageSize)}
          onChange={(value) => onPageSizeChange(Number(value))}
          options={PAGE_SIZE_OPTIONS.map((size) => ({ value: String(size), label: String(size) }))}
        />
      </div>

      {showInvalid ? (
        <p className="pagination__error" id={errorId} aria-live="polite">
          {t('pagination.invalid', { pages: formatNumber(pageCount) })}
        </p>
      ) : null}
    </nav>
  )
}

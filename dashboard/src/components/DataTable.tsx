import clsx from 'clsx'
import type { ReactNode } from 'react'
import { useMediaQuery } from '../lib/useMediaQuery'

export interface Column<T> {
  key: string
  label: ReactNode
  align?: 'left' | 'right' | 'center'
  width?: string
  className?: string
  render: (row: T) => ReactNode
}

interface DataTableProps<T> {
  caption: string
  columns: Array<Column<T>>
  rows: T[]
  rowKey: (row: T) => string
  onRowClick?: (row: T) => void
  empty?: ReactNode
  /** Per-row class hook (live/selected state); rows stay a table concern. */
  rowClassName?: (row: T) => string | undefined
  /** `comfy` uses the 60px row from the reference console; default is 56px. */
  density?: 'standard' | 'comfy'
}

export function DataTable<T>({
  caption,
  columns,
  rows,
  rowKey,
  onRowClick,
  empty,
  rowClassName,
  density = 'standard',
}: DataTableProps<T>) {
  const compact = useMediaQuery('(max-width: 760px)')

  if (compact) {
    const selectColumn = columns.find((column) => column.key === 'select')
    const actionColumns = columns.filter((column) => column.label === '' || column.label === null)
    const contentColumns = columns.filter(
      (column) => column !== selectColumn && !actionColumns.includes(column),
    )
    const titleColumn = contentColumns[0]
    const detailColumns = contentColumns.slice(1)

    if (rows.length === 0) {
      return <div className="row-cards row-cards--empty">{empty}</div>
    }

    return (
      <div className="row-cards" role="list" aria-label={caption}>
        {rows.map((row) => (
          <div className={clsx('row-card', rowClassName?.(row))} key={rowKey(row)} role="listitem">
            <div
              className={clsx('row-card__body', onRowClick && 'is-clickable')}
              role={onRowClick ? 'button' : undefined}
              tabIndex={onRowClick ? 0 : undefined}
              onClick={onRowClick ? () => onRowClick(row) : undefined}
              onKeyDown={
                onRowClick
                  ? (event) => {
                      if (event.target !== event.currentTarget) return
                      if (event.key === 'Enter' || event.key === ' ') {
                        event.preventDefault()
                        onRowClick(row)
                      }
                    }
                  : undefined
              }
            >
              <div className="row-card__head">
                {selectColumn ? (
                  <span className="row-card__select" onClick={(event) => event.stopPropagation()}>
                    {selectColumn.render(row)}
                  </span>
                ) : null}
                <div className="row-card__title">{titleColumn?.render(row)}</div>
              </div>
              <dl className="row-card__facts">
                {detailColumns.map((column) => (
                  <div className="row-card__fact" key={column.key}>
                    <dt>{column.label}</dt>
                    <dd className={clsx(column.className, column.align === 'right' && 'is-right')}>
                      {column.render(row)}
                    </dd>
                  </div>
                ))}
              </dl>
            </div>
            {actionColumns.length > 0 ? (
              <div className="row-card__actions">
                {actionColumns.map((column) => (
                  <div className="row-card__action-group" key={column.key}>
                    {column.render(row)}
                  </div>
                ))}
              </div>
            ) : null}
          </div>
        ))}
      </div>
    )
  }

  return (
    <div className="table-wrap">
      <table className={clsx('data-table', density === 'comfy' && 'data-table--comfy')}>
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr>
            {columns.map((column) => (
              <th
                key={column.key}
                scope="col"
                className={clsx(column.align && `is-${column.align}`, column.className)}
                style={column.width ? { width: column.width } : undefined}
              >
                {column.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr className="data-table__empty-row">
              <td colSpan={columns.length}>{empty}</td>
            </tr>
          ) : (
            rows.map((row) => (
              <tr
                key={rowKey(row)}
                className={clsx(onRowClick && 'is-clickable', rowClassName?.(row))}
                tabIndex={onRowClick ? 0 : undefined}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
                onKeyDown={
                  onRowClick
                    ? (event) => {
                        if (event.key === 'Enter' || event.key === ' ') {
                          event.preventDefault()
                          onRowClick(row)
                        }
                      }
                    : undefined
                }
              >
                {columns.map((column) => (
                  <td key={column.key} className={clsx(column.align && `is-${column.align}`, column.className)}>
                    {column.render(row)}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  )
}

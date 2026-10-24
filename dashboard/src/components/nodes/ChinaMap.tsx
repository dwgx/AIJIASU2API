import clsx from 'clsx'
import { useId, useMemo, useState } from 'react'
import { formatDateTime, formatNumber } from '../../lib/format'
import { PROVINCES, PROVINCE_VIEWBOX, fillBin } from '../../lib/geo'
import type { CatalogGeoSummary, ProvinceDatum } from '../../lib/geo'
import { useI18n } from '../../lib/i18n'
import { useMediaQuery } from '../../lib/useMediaQuery'
import { ErrorState } from '../primitives'

interface ChinaMapProps {
  geo: CatalogGeoSummary
  /** Selected province code; '' means all, `00` means the Other bucket. */
  selected: string
  onSelect: (code: string) => void
  loading?: boolean
  error?: string | null
  onRetry?: () => void
  fetchedAt?: string | null
  stale?: boolean
}

const BINS = [0, 1, 2, 3, 4, 5] as const

interface Readout {
  code: string
  name: string
  nameEn: string
  count: number
  cities: number
  connected: number
}

function readoutOf(province: ProvinceDatum): Readout {
  return {
    code: province.code,
    name: province.name,
    nameEn: province.nameEn,
    count: province.count,
    cities: province.cities,
    connected: province.connected,
  }
}

/**
 * Offline province choropleth for the catalog.
 *
 * The SVG is decorative (`aria-hidden`): the province list below it is the
 * keyboard- and screen-reader-accessible control, and every count it shows is
 * also in the list. No tiles, no projection and no network at runtime — the
 * paths come from the committed `cn-provinces.json` asset.
 */
export function ChinaMap({
  geo,
  selected,
  onSelect,
  loading = false,
  error = null,
  onRetry,
  fetchedAt,
  stale = false,
}: ChinaMapProps) {
  const { t, lang } = useI18n()
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const titleId = useId()
  const [hovered, setHovered] = useState<string | null>(null)

  const byCode = useMemo(
    () => new Map(geo.provinces.map((province) => [province.code, province])),
    [geo.provinces],
  )

  // Count desc, then localized name: the busiest provinces stay at the top of
  // the fallback list, and the map is never the only way to reach a province.
  const ordered = useMemo(
    () =>
      [...geo.provinces].sort(
        (left, right) =>
          right.count - left.count ||
          (lang === 'zh' ? left.name.localeCompare(right.name) : left.nameEn.localeCompare(right.nameEn)),
      ),
    [geo.provinces, lang],
  )

  const hoveredProvince = hovered && hovered !== '00' ? byCode.get(hovered) : undefined
  const selectedProvince = selected && selected !== '00' ? byCode.get(selected) : undefined
  const currentProvince = geo.provinces.find((province) => province.current)
  const otherReadout: Readout = {
    code: '00',
    name: t('nodes.catalog.map.other'),
    nameEn: t('nodes.catalog.map.other'),
    count: geo.unknown,
    cities: 0,
    connected: 0,
  }

  const readout: Readout | null =
    hovered === '00'
      ? otherReadout
      : hoveredProvince
        ? readoutOf(hoveredProvince)
        : selected === '00'
          ? otherReadout
          : selectedProvince
            ? readoutOf(selectedProvince)
            : currentProvince
              ? readoutOf(currentProvince)
              : null

  return (
    <section
      className="catalog-map"
      data-motion={reducedMotion ? 'reduced' : 'full'}
      aria-labelledby={titleId}
    >
      <header className="catalog-map__head">
        <div className="catalog-map__heading">
          <h2 className="catalog-map__title" id={titleId}>
            {t('nodes.catalog.map.title')}
          </h2>
          <p className="catalog-map__sub">
            <span>{t('nodes.catalog.map.provinces', { count: PROVINCES.length })}</span>
            <span aria-hidden="true">·</span>
            <span>
              {t('nodes.catalog.map.servers', {
                count: loading ? '—' : formatNumber(geo.total),
              })}
            </span>
          </p>
        </div>
        {geo.current ? (
          <span className="catalog-map__current-chip">
            {t('nodes.catalog.map.current', { name: geo.current.title })}
          </span>
        ) : null}
      </header>

      {error ? (
        <div className="catalog-map__state">
          <ErrorState message={error} onRetry={onRetry} />
        </div>
      ) : loading ? (
        <p className="catalog-map__state catalog-map__loading">{t('nodes.catalog.map.loading')}</p>
      ) : (
        <>
          <div className="catalog-map__canvas">
            <svg
              className="catalog-map__svg"
              viewBox={PROVINCE_VIEWBOX}
              aria-hidden="true"
              focusable="false"
              data-testid="china-map-svg"
            >
              {geo.provinces.map((province) => (
                <path
                  key={province.code}
                  d={province.shape.path}
                  data-code={province.code}
                  className={clsx(
                    'catalog-map__province',
                    `catalog-map__province--bin-${fillBin(province.count)}`,
                    province.code === selected && 'is-selected',
                    province.current && 'is-current',
                  )}
                  onClick={() => onSelect(province.code)}
                  onPointerEnter={() => setHovered(province.code)}
                  onPointerLeave={() => setHovered(null)}
                >
                  <title>
                    {`${lang === 'zh' ? province.name : province.nameEn} · ${formatNumber(province.count)}`}
                  </title>
                </path>
              ))}
              {currentProvince ? (
                <path
                  className="catalog-map__province-ring"
                  d={currentProvince.shape.path}
                  data-testid="catalog-map-current-ring"
                />
              ) : null}
              {geo.currentCity ? (
                <circle
                  className="catalog-map__dot"
                  data-testid="catalog-map-current-dot"
                  cx={geo.currentCity.x}
                  cy={geo.currentCity.y}
                  r={4}
                />
              ) : null}
            </svg>
            {geo.total === 0 ? (
              <p className="catalog-map__empty">{t('nodes.catalog.map.empty')}</p>
            ) : null}
          </div>

          <p className="catalog-map__readout" data-testid="catalog-map-readout">
            {readout ? (
              <>
                <strong>{lang === 'zh' ? readout.name : readout.nameEn}</strong>
                <span>
                  {t('nodes.catalog.map.hover', {
                    count: formatNumber(readout.count),
                    cities: formatNumber(readout.cities),
                    connected: formatNumber(readout.connected),
                  })}
                </span>
                <span className="catalog-map__hint">
                  {selected === readout.code
                    ? t('nodes.catalog.map.hintClear')
                    : t('nodes.catalog.map.hintFilter')}
                </span>
              </>
            ) : (
              <span className="catalog-map__hint">{t('nodes.catalog.map.hintFilter')}</span>
            )}
          </p>

          <div className="catalog-map__legend">
            <span className="catalog-map__legend-title">{t('nodes.catalog.map.legendTitle')}</span>
            <ul className="catalog-map__legend-scale">
              {BINS.map((bin) => (
                <li key={bin}>
                  <span
                    className={clsx('catalog-map__swatch', `catalog-map__swatch--bin-${bin}`)}
                    aria-hidden="true"
                  />
                  <span>{t(`nodes.catalog.map.bin.${bin}`)}</span>
                </li>
              ))}
            </ul>
            <span className="catalog-map__legend-note">{t('nodes.catalog.map.legendNote')}</span>
          </div>

          <ul className="catalog-map__list" aria-label={t('nodes.catalog.map.listAria')}>
            {ordered.map((province) => (
              <li key={province.code}>
                <button
                  type="button"
                  className={clsx(
                    'catalog-map__item',
                    province.code === selected && 'is-selected',
                    province.current && 'is-current',
                  )}
                  aria-pressed={province.code === selected}
                  aria-label={t(
                    province.count === 1
                      ? 'nodes.catalog.map.provinceButtonOne'
                      : 'nodes.catalog.map.provinceButton',
                    {
                      name: lang === 'zh' ? province.name : province.nameEn,
                      count: formatNumber(province.count),
                    },
                  )}
                  onClick={() => onSelect(province.code)}
                  onMouseEnter={() => setHovered(province.code)}
                  onMouseLeave={() => setHovered(null)}
                  onFocus={() => setHovered(province.code)}
                  onBlur={() => setHovered(null)}
                >
                  <span className="catalog-map__item-name">
                    {lang === 'zh' ? province.name : province.nameEn}
                  </span>
                  <span className="catalog-map__item-count mono">
                    {formatNumber(province.count)}
                  </span>
                </button>
              </li>
            ))}
            {geo.unknown > 0 ? (
              <li>
                <button
                  type="button"
                  className={clsx('catalog-map__item', selected === '00' && 'is-selected')}
                  aria-pressed={selected === '00'}
                  aria-label={t('nodes.catalog.map.unknownAria', {
                    count: formatNumber(geo.unknown),
                  })}
                  title={t('nodes.catalog.map.otherHint', { count: formatNumber(geo.unknown) })}
                  onClick={() => onSelect('00')}
                  onMouseEnter={() => setHovered('00')}
                  onMouseLeave={() => setHovered(null)}
                  onFocus={() => setHovered('00')}
                  onBlur={() => setHovered(null)}
                >
                  <span className="catalog-map__item-name">{t('nodes.catalog.map.other')}</span>
                  <span className="catalog-map__item-count mono">{formatNumber(geo.unknown)}</span>
                </button>
              </li>
            ) : null}
          </ul>
        </>
      )}

      <p className="catalog-map__caption">
        <span>{t('nodes.catalog.map.source')}</span>
        {fetchedAt ? (
          <span>
            {' '}
            · {t('nodes.catalog.map.snapshot', { time: formatDateTime(fetchedAt) })}
          </span>
        ) : null}
        {stale ? <span> · {t('nodes.catalog.staleBadge')}</span> : null}
      </p>
    </section>
  )
}

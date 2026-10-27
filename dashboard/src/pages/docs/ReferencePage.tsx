import clsx from 'clsx'
import { Link, useParams } from 'react-router-dom'
import { CodeBlock } from '../../components/docs/CodeBlock'
import { reference } from '../../docs/reference.generated'
import { customerOperations, customerTags, safeDecode, tagAnchor, tagSlug } from '../../components/docs/select'
import type { DocsOperation } from '../../docs/types'
import { useI18n } from '../../lib/i18n'

const SAFE_METHODS: Record<string, true> = { GET: true, HEAD: true, OPTIONS: true }

/**
 * The appendix is a count sheet for operator routes, not a callable index.
 * Closed product lines must not be named on the public surface, so only the
 * open admin tags are listed; the omitted total is reported as a note.
 */
const openAdminTagCounts = reference.adminTagCounts.filter((row) => !row.closed)

function placeholderFor(name: string): string {
  return `$${name
    .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
    .replace(/[^A-Za-z0-9]+/g, '_')
    .toUpperCase()}`
}

/** A runnable-shaped example: placeholders are shell variables, never fake ids. */
function curlFor(operation: DocsOperation): string {
  let path = operation.path.replace(/\{([^}]+)\}/g, (_, name: string) => placeholderFor(name))
  const requiredQuery = operation.params.filter((param) => param.in === 'query' && param.required)
  if (requiredQuery.length > 0) {
    path += `?${requiredQuery
      .map((param) => `${param.name}=${placeholderFor(param.name)}`)
      .join('&')}`
  }

  const lines = [`curl -sS${SAFE_METHODS[operation.method] ? '' : ` -X ${operation.method}`} "$BASE${path}"`]
  lines.push('  -H "Authorization: Bearer $AJS_API_KEY"')
  if (!SAFE_METHODS[operation.method]) lines.push('  -H "X-AJS-CSRF: 1"')
  if (operation.body) {
    lines.push('  -H "Content-Type: application/json"')
    lines.push('  -d @body.json')
  }
  return lines.join(' \\\n')
}

function OperationModule({ operation }: { operation: DocsOperation }) {
  const { t } = useI18n()
  const scopeLabel =
    operation.access === 'public'
      ? t('docs.scopePublic')
      : `${t('docs.scopeScoped')} ${operation.scope ?? ''}`.trim()

  return (
    <article id={operation.operationId} className="docs-endpoint">
      <div className="docs-endpoint__head">
        <span className={clsx('docs-method', `docs-method--${operation.method.toLowerCase()}`)}>
          {operation.method}
        </span>
        <span className="docs-endpoint__path">{operation.path}</span>
        <span className={clsx('docs-scope', `docs-scope--${operation.access}`)}>{scopeLabel}</span>
      </div>

      <p className="docs-endpoint__summary">{operation.summary}</p>
      {operation.description && (
        <p className="docs-endpoint__description">{operation.description}</p>
      )}

      <details className="docs-curl">
        <summary>{t('docs.curlExample')}</summary>
        <CodeBlock code={curlFor(operation)} lang="bash" label="curl" lineNumbers={false} />
      </details>

      {operation.params.length > 0 && (
        <div className="docs-op-block">
          <h4>{t('docs.parameters')}</h4>
          <div
            className="docs-table-wrap"
            tabIndex={0}
            role="region"
            aria-label={`${operation.method} ${operation.path} ${t('docs.parameters')}`}
          >
            <table className="docs-table">
              <thead>
                <tr>
                  <th scope="col">{t('docs.paramName')}</th>
                  <th scope="col">{t('docs.paramIn')}</th>
                  <th scope="col">{t('docs.paramType')}</th>
                  <th scope="col">{t('docs.paramRequired')}</th>
                  <th scope="col">{t('docs.paramDescription')}</th>
                </tr>
              </thead>
              <tbody>
                {operation.params.map((param) => (
                  <tr key={`${param.in}:${param.name}`}>
                    <td className="docs-table__code">{param.name}</td>
                    <td className="docs-table__mono">{param.in}</td>
                    <td className="docs-table__mono">
                      {param.type}
                      {param.enum && param.enum.length > 0 && (
                        <span className="docs-enum">{param.enum.join(' | ')}</span>
                      )}
                    </td>
                    <td>{param.required ? t('common.required') : t('docs.optional')}</td>
                    <td>{param.description || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {operation.body && (
        <div className="docs-op-block">
          <h4>{t('docs.requestBody')}</h4>
          <p className="docs-body-summary">{operation.body.summary}</p>
          {operation.body.description && <p>{operation.body.description}</p>}
        </div>
      )}

      <div className="docs-op-block">
        <h4>{t('docs.responses')}</h4>
        <div
          className="docs-table-wrap"
          tabIndex={0}
          role="region"
          aria-label={`${operation.method} ${operation.path} ${t('docs.responses')}`}
        >
          <table className="docs-table">
            <thead>
              <tr>
                <th scope="col">{t('docs.responseStatus')}</th>
                <th scope="col">{t('docs.responseDescription')}</th>
              </tr>
            </thead>
            <tbody>
              {operation.responses.map((response) => (
                <tr key={response.status}>
                  <td className="docs-table__mono">{response.status}</td>
                  <td>{response.description}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </article>
  )
}

export default function ReferencePage() {
  const { tag } = useParams<{ tag: string }>()
  const { t } = useI18n()

  const requested = tag ? safeDecode(tag) : ''
  const activeTag = requested
    ? customerTags.find(
        (entry) =>
          tagSlug(entry.name) === tagSlug(requested) ||
          entry.name.toLowerCase() === requested.toLowerCase(),
      )
    : undefined
  const unknownTag = Boolean(requested) && !activeTag
  const visibleTags = activeTag ? [activeTag] : customerTags

  return (
    <article className="docs-article docs-reference">
      <nav className="docs-breadcrumb" aria-label={t('docs.breadcrumb')}>
        <Link to="/docs">{t('docs.breadcrumb')}</Link>
        <span aria-hidden="true">/</span>
        <span>{t('docs.referenceTitle')}</span>
      </nav>

      <header className="docs-page-head">
        <h1>{t('docs.referenceTitle')}</h1>
        <p className="docs-page-summary">{t('docs.referenceSummary')}</p>
        <div className="docs-stats">
          <span className="docs-stat">
            {t('docs.statCallable', { count: reference.counts.public + reference.counts.scoped })}
          </span>
          <span className="docs-stat">
            {t('docs.statOperator', { count: reference.counts.admin })}
          </span>
          <span className="docs-stat">
            {t('docs.statClosed', { count: reference.counts.closed })}
          </span>
        </div>
      </header>

      <nav className="docs-chips" aria-label={t('docs.filterLabel')}>
        <Link
          to="/docs/reference"
          className={clsx('docs-chip', !activeTag && !unknownTag && 'is-active')}
          aria-current={!activeTag && !unknownTag ? 'page' : undefined}
        >
          {t('common.all')}
        </Link>
        {customerTags.map((entry) => (
          <Link
            key={entry.name}
            to={`/docs/reference/${tagSlug(entry.name)}`}
            className={clsx('docs-chip', activeTag?.name === entry.name && 'is-active')}
            aria-current={activeTag?.name === entry.name ? 'page' : undefined}
          >
            {entry.name}
          </Link>
        ))}
      </nav>

      {unknownTag && (
        <div className="docs-callout docs-callout--warn" role="note">
          <p className="docs-callout__title">{t('docs.unknownTag', { tag: requested })}</p>
          <p>
            <Link to="/docs/reference">{t('docs.referenceAll')}</Link>
          </p>
        </div>
      )}

      {visibleTags.map((entry) => {
        const operations = customerOperations.filter((operation) => operation.tag === entry.name)
        return (
          <section key={entry.name} id={tagAnchor(entry.name)} className="docs-tag-group">
            <div className="docs-tag-group__head">
              <h2>{entry.name}</h2>
              {entry.description && <p>{entry.description}</p>}
            </div>
            {operations.map((operation) => (
              <OperationModule key={operation.operationId} operation={operation} />
            ))}
          </section>
        )
      })}

      <section id="operator-appendix" className="docs-admin">
        <h2>{t('docs.adminAppendix')}</h2>
        <p>{t('docs.adminAppendixNote')}</p>
        <div
          className="docs-table-wrap"
          tabIndex={0}
          role="region"
          aria-label={t('docs.adminAppendix')}
        >
          <table className="docs-table">
            <thead>
              <tr>
                <th scope="col">{t('docs.adminTag')}</th>
                <th scope="col" className="docs-num">
                  {t('docs.adminOperations')}
                </th>
                <th scope="col">{t('docs.adminState')}</th>
              </tr>
            </thead>
            <tbody>
              {openAdminTagCounts.map((row) => (
                <tr key={row.tag}>
                  <td>{row.tag}</td>
                  <td className="docs-num">{row.operations}</td>
                  <td>
                    <span className="docs-state docs-state--open">{t('docs.open')}</span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p>{t('docs.adminClosedNote', { count: reference.counts.closed })}</p>
      </section>
    </article>
  )
}

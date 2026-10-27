import { Link, useParams } from 'react-router-dom'
import { DocsBlocks } from '../../components/docs/DocsBlocks'
import { docsPageBySlug } from '../../docs/content'
import { useI18n } from '../../lib/i18n'

function DocsNotFound() {
  const { t } = useI18n()
  return (
    <article className="docs-article docs-notfound">
      <p className="docs-breadcrumb">{t('docs.breadcrumb')}</p>
      <h1>{t('docs.notFoundTitle')}</h1>
      <p>{t('docs.notFoundBody')}</p>
      <Link to="/docs">{t('docs.backToDocs')}</Link>
    </article>
  )
}

export default function DocsPageView() {
  const { slug = 'overview' } = useParams<{ slug: string }>()
  const { lang, t } = useI18n()
  const page = docsPageBySlug[slug]

  if (!page) return <DocsNotFound />

  return (
    <article className="docs-article">
      <nav className="docs-breadcrumb" aria-label={t('docs.breadcrumb')}>
        <Link to="/docs">{t('docs.breadcrumb')}</Link>
        <span aria-hidden="true">/</span>
        <span>{page.title[lang]}</span>
      </nav>

      <header className="docs-page-head">
        <h1>{page.title[lang]}</h1>
        <p className="docs-page-summary">{page.summary[lang]}</p>
      </header>

      {page.sections.map((section) => (
        <section key={section.id} id={section.id} className="docs-section">
          <h2 className="docs-section__title">{section.title[lang]}</h2>
          <DocsBlocks blocks={section.blocks} lang={lang} />
        </section>
      ))}
    </article>
  )
}

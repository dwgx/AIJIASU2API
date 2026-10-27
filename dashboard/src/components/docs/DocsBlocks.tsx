import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import type { DocsBlock } from '../../docs/types'
import type { Language } from '../../lib/i18n'
import { CodeBlock } from './CodeBlock'

/** Inline Markdown used by curated prose: code spans and links only. */
function renderInline(text: string, keyPrefix: string): ReactNode[] {
  const pattern = /(`[^`]+`|\[[^\]]+\]\([^)]+\))/g
  const nodes: ReactNode[] = []
  let lastIndex = 0
  let index = 0
  let match: RegExpExecArray | null

  while ((match = pattern.exec(text)) !== null) {
    if (match.index > lastIndex) nodes.push(text.slice(lastIndex, match.index))
    const token = match[0]
    if (token.startsWith('`')) {
      nodes.push(
        <code className="docs-inline-code" key={`${keyPrefix}-code-${index}`}>
          {token.slice(1, -1)}
        </code>,
      )
    } else {
      const link = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(token)
      if (link) {
        const [, label, href] = link
        nodes.push(
          href.startsWith('/') ? (
            <Link key={`${keyPrefix}-link-${index}`} to={href}>
              {label}
            </Link>
          ) : (
            <a key={`${keyPrefix}-link-${index}`} href={href} target="_blank" rel="noreferrer">
              {label}
            </a>
          ),
        )
      }
    }
    lastIndex = match.index + token.length
    index += 1
  }

  if (lastIndex < text.length) nodes.push(text.slice(lastIndex))
  return nodes
}

function DocsTable({ block, lang }: { block: Extract<DocsBlock, { kind: 'table' }>; lang: Language }) {
  const label = block.columns.map((column) => column[lang]).join(' · ')
  return (
    <div className="docs-table-wrap" tabIndex={0} role="region" aria-label={label}>
      <table className="docs-table">
        <thead>
          <tr>
            {block.columns.map((column, index) => (
              <th key={index} scope="col">
                {renderInline(column[lang], `head-${index}`)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {block.rows.map((row, rowIndex) => (
            <tr key={rowIndex}>
              {row.map((cell, cellIndex) => (
                <td key={cellIndex}>{renderInline(cell[lang], `${rowIndex}-${cellIndex}`)}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function DocsBlockView({ block, lang, index }: { block: DocsBlock; lang: Language; index: number }) {
  switch (block.kind) {
    case 'p':
      return <p className="docs-p">{renderInline(block.text[lang], `p-${index}`)}</p>
    case 'h3':
      return <h3 className="docs-h3">{renderInline(block.text[lang], `h3-${index}`)}</h3>
    case 'list': {
      const items = block.items.map((item, itemIndex) => (
        <li key={itemIndex}>{renderInline(item[lang], `li-${index}-${itemIndex}`)}</li>
      ))
      return block.ordered ? (
        <ol className="docs-list">{items}</ol>
      ) : (
        <ul className="docs-list">{items}</ul>
      )
    }
    case 'code':
      return (
        <CodeBlock
          code={block.code}
          lang={block.lang}
          label={block.label[lang]}
          lineNumbers
        />
      )
    case 'table':
      return <DocsTable block={block} lang={lang} />
    case 'callout':
      return (
        <div className={`docs-callout docs-callout--${block.tone}`} role="note">
          <p className="docs-callout__title">{renderInline(block.title[lang], `callout-${index}`)}</p>
          <p>{renderInline(block.text[lang], `callout-text-${index}`)}</p>
        </div>
      )
    default:
      return null
  }
}

/** Renders the curated page blocks with the site language. */
export function DocsBlocks({ blocks, lang }: { blocks: DocsBlock[]; lang: Language }) {
  return (
    <>
      {blocks.map((block, index) => (
        <DocsBlockView key={index} block={block} lang={lang} index={index} />
      ))}
    </>
  )
}

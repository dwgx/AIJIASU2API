import clsx from 'clsx'
import { useState } from 'react'
import { useI18n } from '../../lib/i18n'
import { CopyButton } from '../CopyButton'

export type CodeTabId = 'curl' | 'go' | 'ts'

export interface CodeTab {
  id: CodeTabId
  label: string
  code: string
  /** Display language when the tab is active; defaults to the block language. */
  lang?: string
}

const CODE_LANG_KEY = 'ajs.docs.codeLang'

function readCodeLang(tabs: CodeTab[]): CodeTabId {
  try {
    const stored = localStorage.getItem(CODE_LANG_KEY)
    if (tabs.some((tab) => tab.id === stored)) return stored as CodeTabId
  } catch {
    // Storage can be unavailable; the first tab still works for this session.
  }
  return tabs[0]?.id ?? 'curl'
}

/**
 * The docs code plate: 40px header, mono label, optional curl/Go/TS tabs, a
 * 26px square copy control, an optional line-number gutter and an 86ch cap.
 * One language choice is shared by every tabbed block on the page and survives
 * a reload through `ajs.docs.codeLang`.
 */
export function CodeBlock({
  code,
  lang = 'bash',
  label,
  tabs,
  lineNumbers = true,
  className,
}: {
  code: string
  lang?: string
  label?: string
  tabs?: CodeTab[]
  lineNumbers?: boolean
  className?: string
}) {
  const { t } = useI18n()
  const [tabId, setTabId] = useState<CodeTabId>(() => readCodeLang(tabs ?? []))
  const tabbed = Boolean(tabs && tabs.length > 1)
  const activeTab = tabbed ? (tabs?.find((tab) => tab.id === tabId) ?? tabs?.[0]) : undefined
  const activeCode = activeTab?.code ?? code
  const activeLang = activeTab?.lang ?? lang
  const heading = label ?? activeLang.toUpperCase()

  const selectTab = (id: CodeTabId) => {
    setTabId(id)
    try {
      localStorage.setItem(CODE_LANG_KEY, id)
    } catch {
      // The in-memory choice still applies for this session.
    }
  }

  const lines = activeCode.split('\n')

  return (
    <figure className={clsx('docs-code', className)} aria-label={heading}>
      <div className="docs-code__head">
        <span className="docs-code__label">{heading}</span>
        <div className="docs-code__tools">
          {tabbed && (
            <div className="docs-code__tabs" role="tablist" aria-label={t('docs.codeLang')}>
              {tabs?.map((tab) => (
                <button
                  key={tab.id}
                  type="button"
                  role="tab"
                  aria-selected={tab.id === activeTab?.id}
                  className={clsx('docs-code__tab', tab.id === activeTab?.id && 'is-active')}
                  onClick={() => selectTab(tab.id)}
                >
                  {tab.label}
                </button>
              ))}
            </div>
          )}
          <CopyButton
            value={activeCode}
            label={t('docs.copyCode')}
            className="docs-code__copy"
          />
        </div>
      </div>
      <div className="docs-code__body">
        <pre className="docs-code__pre" data-lang={activeLang}>
          <code>
            {lines.map((line, index) => (
              <span className="docs-code__line" key={index}>
                {lineNumbers && (
                  <span className="docs-code__num" aria-hidden="true">
                    {index + 1}
                  </span>
                )}
                <span className="docs-code__text">{line === '' ? '\u00a0' : line}</span>
              </span>
            ))}
          </code>
        </pre>
      </div>
    </figure>
  )
}

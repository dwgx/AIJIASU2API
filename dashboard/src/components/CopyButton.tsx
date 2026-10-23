import clsx from 'clsx'
import { Check, Copy } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import type { MouseEvent } from 'react'
import { useToast } from './Toast'
import { useI18n } from '../lib/i18n'
import { Button, IconButton } from './primitives'

async function writeClipboard(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value)
    return
  }
  const area = document.createElement('textarea')
  area.value = value
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  const copied = document.execCommand('copy')
  area.remove()
  if (!copied) throw new Error('Clipboard is unavailable')
}

export function CopyButton({
  value,
  label,
  toastMessage,
  className,
  withLabel = false,
}: {
  value: string
  label: string
  toastMessage?: string
  className?: string
  withLabel?: boolean
}) {
  const { push } = useToast()
  const { t } = useI18n()
  const [copied, setCopied] = useState(false)
  const timer = useRef<number | undefined>(undefined)

  useEffect(() => {
    return () => {
      if (timer.current) window.clearTimeout(timer.current)
    }
  }, [])

  const handleCopy = async () => {
    try {
      await writeClipboard(value)
      setCopied(true)
      push(toastMessage ?? t('common.copiedToClipboard'), 'success')
      if (timer.current) window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => setCopied(false), 1600)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('common.clipboardUnavailable'), 'danger')
    }
  }

  const icon = copied ? <Check size={14} /> : <Copy size={14} />
  const handleClick = (event: MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation()
    void handleCopy()
  }

  if (withLabel) {
    return (
      <Button
        size="sm"
        variant="ghost"
        aria-label={copied ? t('common.copied') : label}
        icon={icon}
        onClick={handleClick}
        className={clsx('copy-btn', copied && 'is-copied', className)}
      >
        {copied ? t('common.copied') : t('common.copy')}
      </Button>
    )
  }

  return (
    <IconButton
      label={copied ? t('common.copied') : label}
      icon={icon}
      onClick={handleClick}
      className={clsx('copy-btn', copied && 'is-copied', className)}
    />
  )
}

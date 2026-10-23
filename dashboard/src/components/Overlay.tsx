import clsx from 'clsx'
import type { CSSProperties, ReactNode } from 'react'
import { useI18n } from '../lib/i18n'
import { useFocusTrap } from '../lib/useFocusTrap'
import { ApcCloseIcon } from './icons/ApcIcons'
import { Button, IconButton } from './primitives'

interface DrawerProps {
  open: boolean
  title: string
  subtitle?: string
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  width?: number
}

export function Drawer({ open, title, subtitle, onClose, children, footer, width = 440 }: DrawerProps) {
  const { t } = useI18n()
  const containerRef = useFocusTrap<HTMLDivElement>(open, onClose)
  if (!open) return null
  const style = { '--drawer-w': `${width}px` } as CSSProperties
  return (
    <div
      className="overlay overlay--drawer"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div className="overlay__scrim" aria-hidden="true" />
      <aside
        ref={containerRef}
        className="drawer"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
        style={style}
      >
        <header className="drawer__head">
          <div>
            <h2 className="drawer__title">{title}</h2>
            {subtitle ? <p className="drawer__subtitle">{subtitle}</p> : null}
          </div>
          <IconButton label={t('common.closePanel')} icon={<ApcCloseIcon size={16} />} onClick={onClose} />
        </header>
        <div className="drawer__body">{children}</div>
        {footer ? <footer className="drawer__foot">{footer}</footer> : null}
      </aside>
    </div>
  )
}

interface ModalProps {
  open: boolean
  title: string
  description?: string
  onClose: () => void
  children?: ReactNode
  footer?: ReactNode
  size?: 'sm' | 'md'
}

export function Modal({ open, title, description, onClose, children, footer, size = 'sm' }: ModalProps) {
  const { t } = useI18n()
  const containerRef = useFocusTrap<HTMLDivElement>(open, onClose)
  if (!open) return null
  return (
    <div
      className="overlay overlay--modal"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div className="overlay__scrim" aria-hidden="true" />
      <div
        ref={containerRef}
        className={clsx('modal', size === 'md' && 'modal--md')}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
      >
        <header className="modal__head">
          <div>
            <h2 className="modal__title">{title}</h2>
            {description ? <p className="modal__desc">{description}</p> : null}
          </div>
          <IconButton label={t('common.closeDialog')} icon={<ApcCloseIcon size={16} />} onClick={onClose} />
        </header>
        {children ? <div className="modal__body">{children}</div> : null}
        {footer ? <footer className="modal__foot">{footer}</footer> : null}
      </div>
    </div>
  )
}

interface ConfirmDialogProps {
  open: boolean
  title: string
  description: string
  confirmLabel: string
  tone?: 'danger' | 'primary'
  busy?: boolean
  onConfirm: () => void
  onCancel: () => void
}

export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  tone = 'danger',
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const { t } = useI18n()
  return (
    <Modal
      open={open}
      title={title}
      description={description}
      onClose={onCancel}
      footer={
        <>
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant={tone} onClick={onConfirm} loading={busy}>
            {confirmLabel}
          </Button>
        </>
      }
    />
  )
}

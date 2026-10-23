import clsx from 'clsx'
import { X } from 'lucide-react'
import type { CSSProperties, ReactNode } from 'react'
import { useFocusTrap } from '../lib/useFocusTrap'
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
          <IconButton label="Close panel" icon={<X size={16} />} onClick={onClose} />
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
          <IconButton label="Close dialog" icon={<X size={16} />} onClick={onClose} />
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
  return (
    <Modal
      open={open}
      title={title}
      description={description}
      onClose={onCancel}
      footer={
        <>
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          <Button variant={tone} onClick={onConfirm} loading={busy}>
            {confirmLabel}
          </Button>
        </>
      }
    />
  )
}

import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { hasPanelToken, isMockMode, setPanelToken } from '../lib/api'
import { useI18n } from '../lib/i18n'
import { Modal } from './Overlay'
import { Button, Field, Input } from './primitives'

/**
 * Runtime token gate.
 *
 * The build-time `VITE_PANEL_TOKEN` is optional: when a live daemon answers
 * 401, `http()` dispatches `ajs:unauthorized` and this dialog asks for the
 * admin token. The value is kept in sessionStorage for this tab only and is
 * never rendered back.
 */
export function TokenGate() {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [token, setToken] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    const onUnauthorized = () => {
      if (!isMockMode()) setOpen(true)
    }
    const onRequest = () => {
      if (!isMockMode()) setOpen(true)
    }
    window.addEventListener('ajs:unauthorized', onUnauthorized)
    window.addEventListener('ajs:token-required', onRequest)
    return () => {
      window.removeEventListener('ajs:unauthorized', onUnauthorized)
      window.removeEventListener('ajs:token-required', onRequest)
    }
  }, [])

  const close = () => {
    setOpen(false)
    setToken('')
    setError('')
  }

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const value = token.trim()
    if (!value) {
      setError(t('gate.emptyError'))
      return
    }
    setPanelToken(value)
    setToken('')
    setError('')
    setOpen(false)
    // Retry every mounted loader with the new bearer header.
    window.dispatchEvent(new Event('ajs:refresh'))
  }

  return (
    <Modal
      open={open}
      title={t('gate.title')}
      description={hasPanelToken() ? t('gate.descriptionRejected') : t('gate.descriptionRequired')}
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form="token-gate-form" variant="primary">
            {t('gate.useToken')}
          </Button>
        </>
      }
    >
      <form id="token-gate-form" className="form" onSubmit={submit}>
        <Field
          label={t('gate.adminToken')}
          htmlFor="panel-token"
          error={error}
          hint={t('gate.hint')}
        >
          <Input
            id="panel-token"
            type="password"
            value={token}
            onChange={(event) => setToken(event.target.value)}
            autoComplete="off"
            autoFocus
            placeholder="AJS_ADMIN_TOKEN"
          />
        </Field>
        <p className="callout callout--muted">{t('gate.neverShown')}</p>
      </form>
    </Modal>
  )
}

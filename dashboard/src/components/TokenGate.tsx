import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { hasPanelToken, isMockMode, setPanelToken } from '../lib/api'
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
      setError('Enter the value of AJS_ADMIN_TOKEN')
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
      title="Panel token required"
      description={
        hasPanelToken()
          ? 'ajs-accountd rejected the stored token. Enter the current AJS_ADMIN_TOKEN to continue.'
          : 'ajs-accountd requires a bearer token. Enter AJS_ADMIN_TOKEN to load live data.'
      }
      onClose={close}
      footer={
        <>
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" form="token-gate-form" variant="primary">
            Use token
          </Button>
        </>
      }
    >
      <form id="token-gate-form" className="form" onSubmit={submit}>
        <Field
          label="Admin token"
          htmlFor="panel-token"
          error={error}
          hint="Kept in this tab's sessionStorage and sent only as the Authorization bearer header."
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
        <p className="callout callout--muted">
          The token is never displayed again, written to the URL, or included in a log line.
        </p>
      </form>
    </Modal>
  )
}

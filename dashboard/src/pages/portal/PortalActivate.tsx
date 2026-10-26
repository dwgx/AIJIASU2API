import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Button, Field, Input } from '../../components/primitives'
import { useI18n } from '../../lib/i18n'
import {
  PORTAL_INVITE_PASSWORD_MIN,
  PortalError,
  portalApi,
} from '../../lib/portal-api'
import type { PortalInviteView } from '../../lib/portal-api'
import { PortalFrame, portalErrorText } from './PortalFrame'

type PageState = 'checking' | 'ready' | 'invalid' | 'done'

/**
 * Public activation page for a one-time tenant invite. The code in `?code=`
 * is the credential: it is validated once on mount, then consumed by the
 * activate call. A used, expired or unknown code renders one stable refusal so
 * the page never reveals which of those states applies.
 */
export default function PortalActivate() {
  const { t } = useI18n()
  const [params] = useSearchParams()
  const code = (params.get('code') ?? '').trim()
  const [state, setState] = useState<PageState>(code ? 'checking' : 'invalid')
  const [invite, setInvite] = useState<PortalInviteView | null>(null)
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!code) return
    let cancelled = false
    setState('checking')
    setError('')
    portalApi
      .previewInvite(code)
      .then((view) => {
        if (cancelled) return
        setInvite(view)
        setDisplayName(view.displayName)
        setState('ready')
      })
      .catch((cause: unknown) => {
        if (cancelled) return
        setError(portalErrorText(cause, t, 'activate'))
        setState('invalid')
      })
    return () => {
      cancelled = true
    }
  }, [code, t])

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!displayName.trim()) {
      setError(t('portal.activate.nameRequired'))
      return
    }
    if (password.length < PORTAL_INVITE_PASSWORD_MIN) {
      setError(t('portal.activate.weak', { count: PORTAL_INVITE_PASSWORD_MIN }))
      return
    }
    if (password !== confirm) {
      setError(t('portal.activate.mismatch'))
      return
    }
    setBusy(true)
    setError('')
    try {
      await portalApi.activateInvite(code, {
        password,
        displayName: displayName.trim(),
      })
      setState('done')
    } catch (cause) {
      if (cause instanceof PortalError && (cause.status === 404 || cause.status === 409 || cause.status === 410)) {
        setError(t('portal.activate.invalid'))
      } else {
        setError(portalErrorText(cause, t, 'activate'))
      }
    } finally {
      setBusy(false)
    }
  }

  if (state === 'done') {
    return (
      <PortalFrame title={t('portal.activate.successTitle')} subtitle={t('portal.activate.successBody')}>
        <p className="portal-alt">
          <Link to="/portal/login">{t('portal.activate.back')}</Link>
        </p>
      </PortalFrame>
    )
  }

  if (state === 'checking') {
    return (
      <PortalFrame title={t('portal.activate.title')} subtitle={t('portal.activate.subtitle')}>
        <p className="portal-alt" role="status">
          {t('portal.activate.checking')}
        </p>
      </PortalFrame>
    )
  }

  if (state === 'invalid' || !invite) {
    return (
      <PortalFrame title={t('portal.activate.title')} subtitle={t('portal.activate.subtitle')}>
        <p className="portal-alert" role="alert">
          {error || t('portal.activate.invalid')}
        </p>
        <p className="portal-alt">
          <Link to="/portal/login">{t('portal.activate.back')}</Link>
        </p>
      </PortalFrame>
    )
  }

  return (
    <PortalFrame title={t('portal.activate.title')} subtitle={t('portal.activate.subtitle')}>
      <form className="form portal-form" aria-label={t('portal.activate.title')} onSubmit={submit}>
        <Field label={t('portal.activate.email')} htmlFor="portal-activate-email">
          <Input id="portal-activate-email" value={invite.email} readOnly autoComplete="username" />
        </Field>
        <Field label={t('portal.activate.name')} htmlFor="portal-activate-name">
          <Input
            id="portal-activate-name"
            value={displayName}
            onChange={(event) => setDisplayName(event.target.value)}
            autoComplete="name"
            autoFocus
            required
          />
        </Field>
        <Field
          label={t('portal.activate.password')}
          htmlFor="portal-activate-password"
          hint={t('portal.activate.passwordHint', { count: PORTAL_INVITE_PASSWORD_MIN })}
        >
          <Input
            id="portal-activate-password"
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>
        <Field label={t('portal.activate.confirm')} htmlFor="portal-activate-confirm">
          <Input
            id="portal-activate-confirm"
            type="password"
            value={confirm}
            onChange={(event) => setConfirm(event.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>
        {error ? (
          <p className="portal-alert" role="alert">
            {error}
          </p>
        ) : null}
        <Button type="submit" variant="primary" loading={busy}>
          {busy ? t('portal.activate.submitting') : t('portal.activate.submit')}
        </Button>
      </form>
    </PortalFrame>
  )
}

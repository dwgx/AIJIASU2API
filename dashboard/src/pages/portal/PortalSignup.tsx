import { useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, Field, Input } from '../../components/primitives'
import { useI18n } from '../../lib/i18n'
import { PORTAL_PASSWORD_MIN, portalApi } from '../../lib/portal-api'
import { PortalFrame, portalErrorText } from './PortalFrame'

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

export default function PortalSignup() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [displayName, setDisplayName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!displayName.trim()) {
      setError(t('portal.signup.nameRequired'))
      return
    }
    if (!EMAIL_PATTERN.test(email.trim())) {
      setError(t('portal.signup.invalidEmail'))
      return
    }
    if (password.length < PORTAL_PASSWORD_MIN) {
      setError(t('portal.signup.weak', { count: PORTAL_PASSWORD_MIN }))
      return
    }
    if (password !== confirm) {
      setError(t('portal.signup.mismatch'))
      return
    }
    setBusy(true)
    setError('')
    try {
      // New and duplicate emails both answer 202; the pending page is the
      // same, so signup never reveals whether the address already exists.
      await portalApi.signup({
        email: email.trim(),
        password,
        displayName: displayName.trim(),
      })
      navigate('/portal/pending', { replace: true })
    } catch (cause) {
      setError(portalErrorText(cause, t, 'signup'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <PortalFrame title={t('portal.signup.title')} subtitle={t('portal.signup.subtitle')}>
      <form className="form portal-form" aria-label={t('portal.signup.title')} onSubmit={submit}>
        <Field label={t('portal.signup.name')} htmlFor="portal-signup-name">
          <Input
            id="portal-signup-name"
            value={displayName}
            onChange={(event) => setDisplayName(event.target.value)}
            placeholder={t('portal.signup.namePlaceholder')}
            autoComplete="name"
            autoFocus
            required
          />
        </Field>
        <Field label={t('portal.signup.email')} htmlFor="portal-signup-email">
          <Input
            id="portal-signup-email"
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            autoComplete="username"
            required
          />
        </Field>
        <Field
          label={t('portal.signup.password')}
          htmlFor="portal-signup-password"
          hint={t('portal.signup.passwordHint', { count: PORTAL_PASSWORD_MIN })}
        >
          <Input
            id="portal-signup-password"
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>
        <Field label={t('portal.signup.confirm')} htmlFor="portal-signup-confirm">
          <Input
            id="portal-signup-confirm"
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
          {busy ? t('portal.signup.submitting') : t('portal.signup.submit')}
        </Button>
      </form>
      <p className="portal-alt">
        {t('portal.signup.haveAccount')}{' '}
        <Link to="/portal/login">{t('portal.signup.signIn')}</Link>
      </p>
    </PortalFrame>
  )
}

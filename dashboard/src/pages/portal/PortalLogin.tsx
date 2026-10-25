import { useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, Field, Input } from '../../components/primitives'
import { useI18n } from '../../lib/i18n'
import { portalApi } from '../../lib/portal-api'
import { PortalFrame, portalErrorText } from './PortalFrame'

export default function PortalLogin() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!email.trim() || !password) {
      setError(t('portal.login.invalid'))
      return
    }
    setBusy(true)
    setError('')
    try {
      await portalApi.login({ email: email.trim(), password })
      navigate('/portal', { replace: true })
    } catch (cause) {
      setError(portalErrorText(cause, t, 'login'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <PortalFrame title={t('portal.login.title')} subtitle={t('portal.login.subtitle')}>
      <form className="form portal-form" aria-label={t('portal.login.title')} onSubmit={submit}>
        <Field label={t('portal.login.email')} htmlFor="portal-email">
          <Input
            id="portal-email"
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            autoComplete="username"
            autoFocus
            required
          />
        </Field>
        <Field label={t('portal.login.password')} htmlFor="portal-password">
          <Input
            id="portal-password"
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            autoComplete="current-password"
            required
          />
        </Field>
        {error ? (
          <p className="portal-alert" role="alert">
            {error}
          </p>
        ) : null}
        <Button type="submit" variant="primary" loading={busy}>
          {busy ? t('portal.login.submitting') : t('portal.login.submit')}
        </Button>
      </form>
      <p className="portal-alt">
        {t('portal.login.noAccount')} <Link to="/portal/signup">{t('portal.login.createAccount')}</Link>
      </p>
    </PortalFrame>
  )
}

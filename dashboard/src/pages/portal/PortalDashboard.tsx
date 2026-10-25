import { LogOut } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Skeleton, Tabs } from '../../components/primitives'
import { useI18n } from '../../lib/i18n'
import { PortalError, portalApi } from '../../lib/portal-api'
import type { PortalSession } from '../../lib/portal-api'
import PortalKeys from './PortalKeys'
import PortalLeases from './PortalLeases'
import PortalOrders from './PortalOrders'
import PortalPlans from './PortalPlans'
import PortalUsage from './PortalUsage'
import { PortalFrame } from './PortalFrame'

type PortalTab = 'leases' | 'usage' | 'keys' | 'plans' | 'orders'

function failureMessage(error: unknown, t: (key: string) => string): string {
  const status = error instanceof PortalError ? error.status : 0
  if (status === 403) return t('portal.dashboard.suspended')
  if (status === 503) return t('portal.error.unavailable')
  return t('portal.dashboard.loadFailed')
}

/**
 * Guarded customer dashboard. The session is re-read on mount (the cookie is
 * HttpOnly, so the UI cannot inspect it); any 401 sends the visitor to sign
 * in, and a suspended tenant sees a refusal instead of data.
 */
export default function PortalDashboard() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [session, setSession] = useState<PortalSession | null>(null)
  const [failure, setFailure] = useState<unknown>(null)
  const [loading, setLoading] = useState(true)
  const [nonce, setNonce] = useState(0)
  const [tab, setTab] = useState<PortalTab>('leases')
  const [loggingOut, setLoggingOut] = useState(false)

  useEffect(() => {
    let active = true
    setLoading(true)
    setFailure(null)
    portalApi
      .session()
      .then((value) => {
        if (active) setSession(value)
      })
      .catch((cause: unknown) => {
        if (!active) return
        if (cause instanceof PortalError && cause.status === 401) {
          navigate('/portal/login', { replace: true })
          return
        }
        setFailure(cause)
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => {
      active = false
    }
  }, [navigate, nonce])

  const logout = async () => {
    setLoggingOut(true)
    try {
      await portalApi.logout()
    } catch {
      // The session may already be revoked; the cookie is cleared server-side
      // when it is not. The UI still leaves the authenticated surface.
    } finally {
      setLoggingOut(false)
      navigate('/portal/login', { replace: true })
    }
  }

  if (loading) {
    return (
      <PortalFrame title={t('portal.dashboard.title')} wide>
        <div className="portal-loading" aria-busy="true" aria-label={t('portal.dashboard.loading')}>
          <Skeleton width={180} height={16} />
          <Skeleton height={220} />
        </div>
      </PortalFrame>
    )
  }

  if (failure) {
    return (
      <PortalFrame title={t('portal.dashboard.title')}>
        <p className="portal-alert" role="alert">
          {failureMessage(failure, t)}
        </p>
        <div className="portal-actions">
          <Button onClick={() => setNonce((value) => value + 1)}>{t('common.retry')}</Button>
          <Button variant="primary" onClick={() => navigate('/portal/login', { replace: true })}>
            {t('portal.login.submit')}
          </Button>
        </div>
      </PortalFrame>
    )
  }

  if (!session) return null

  const who = session.user?.name ?? session.tenant.displayName
  return (
    <PortalFrame
      title={t('portal.dashboard.title')}
      subtitle={t('portal.dashboard.signedInAs', { name: who })}
      wide
      actions={
        <Button
          variant="ghost"
          icon={<LogOut size={14} />}
          loading={loggingOut}
          onClick={logout}
        >
          {loggingOut ? t('portal.dashboard.loggingOut') : t('portal.dashboard.logout')}
        </Button>
      }
    >
      <Tabs
        label={t('portal.dashboard.tabs')}
        options={[
          { value: 'leases', label: t('portal.dashboard.leasesTab') },
          { value: 'usage', label: t('portal.dashboard.usageTab') },
          { value: 'keys', label: t('portal.dashboard.keysTab') },
          { value: 'plans', label: t('portal.dashboard.plansTab') },
          { value: 'orders', label: t('portal.dashboard.ordersTab') },
        ]}
        value={tab}
        onChange={setTab}
      />
      <div
        className="portal-tabpanel"
        role="tabpanel"
        aria-label={t(
          tab === 'leases'
            ? 'portal.dashboard.leasesTab'
            : tab === 'usage'
              ? 'portal.dashboard.usageTab'
              : tab === 'keys'
                ? 'portal.dashboard.keysTab'
                : tab === 'plans'
                  ? 'portal.dashboard.plansTab'
                  : 'portal.dashboard.ordersTab',
        )}
      >
        {tab === 'leases' ? <PortalLeases /> : null}
        {tab === 'usage' ? <PortalUsage /> : null}
        {tab === 'keys' ? <PortalKeys /> : null}
        {tab === 'plans' ? <PortalPlans onCreated={() => setTab('orders')} /> : null}
        {tab === 'orders' ? <PortalOrders /> : null}
      </div>
    </PortalFrame>
  )
}

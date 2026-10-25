import { useNavigate } from 'react-router-dom'
import { Button } from '../../components/primitives'
import { useI18n } from '../../lib/i18n'
import { PortalFrame } from './PortalFrame'

/**
 * Signup is an application, not an account: no user, lease or key exists yet.
 * The page intentionally shows nothing about the email's prior state.
 */
export default function PortalPending() {
  const { t } = useI18n()
  const navigate = useNavigate()
  return (
    <PortalFrame title={t('portal.pending.title')}>
      <div className="portal-note">
        <p>{t('portal.pending.body')}</p>
        <p className="portal-note__fine">{t('portal.pending.note')}</p>
      </div>
      <Button variant="primary" onClick={() => navigate('/portal/login')}>
        {t('portal.pending.back')}
      </Button>
    </PortalFrame>
  )
}

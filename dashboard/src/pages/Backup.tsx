import { useI18n } from '../lib/i18n'
import { BackupPanel } from '../components/backup/BackupPanel'
import { PageHeader } from '../components/primitives'

export default function Backup() {
  const { t } = useI18n()
  return (
    <div className="stack-lg">
      <PageHeader title={t('backup.title')} subtitle={t('backup.subtitle')} />
      <BackupPanel />
    </div>
  )
}

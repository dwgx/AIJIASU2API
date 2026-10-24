import clsx from 'clsx'
import { Database, ExternalLink, Moon, RefreshCw, Save, Sun } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, apiEndpointLabel, hasPanelToken, isMockMode, setPanelToken } from '../lib/api'
import { CopyButton } from '../components/CopyButton'
import { useI18n } from '../lib/i18n'
import { useTheme } from '../lib/theme'
import type { AccentName } from '../lib/theme'
import type { PanelSettings, UpdateInfo } from '../lib/types'
import { useAsync } from '../lib/useAsync'
import { formatDateTime } from '../lib/format'
import { ConfirmDialog } from '../components/Overlay'
import { useToast } from '../components/Toast'
import {
  Badge,
  Button,
  Card,
  ErrorState,
  Input,
  PageHeader,
  SegmentedControl,
  Select,
  Skeleton,
} from '../components/primitives'
import { EffectiveBadge } from '../components/settings/EffectiveBadge'
import { SettingsField } from '../components/settings/SettingsField'
import { SettingsNav } from '../components/settings/SettingsNav'
import { SettingsSection } from '../components/settings/SettingsSection'
import { SettingsSegmented } from '../components/settings/SettingsSegmented'
import { WebhooksSection } from '../components/settings/WebhooksSection'
import { AccessSection } from '../components/settings/AccessSection'
import {
  SETTINGS_FIELD_KEYS,
  SETTINGS_SECTIONS,
  sectionForField,
  settingsSectionFromHash,
} from '../lib/settingsSections'
import type { SettingsFieldKey, SettingsSectionId } from '../lib/settingsSections'
import '../components/settings/settings.css'

const ACCENTS: ReadonlyArray<{ value: AccentName; labelKey: string }> = [
  { value: 'mint', labelKey: 'settings.appearance.mint' },
  { value: 'indigo', labelKey: 'settings.appearance.indigo' },
  { value: 'violet', labelKey: 'settings.appearance.violet' },
  { value: 'orange', labelKey: 'settings.appearance.orange' },
]

const EMPTY_SECTIONS: ReadonlySet<SettingsSectionId> = new Set()

export default function Settings() {
  const mock = isMockMode()
  const settings = useAsync(() => api.getSettings(), [])
  // Facts only: General has no polling. The dashboard refresh action still
  // reloads mounted loaders via `ajs:refresh`.
  const overview = useAsync(() => api.getLabOverview(), [])
  const { push } = useToast()
  const { t, lang, setLang } = useI18n()
  const { theme, setTheme, accent, setAccent } = useTheme()
  const [updateInfo, setUpdateInfo] = useState<UpdateInfo | null>(null)
  const [updateChecking, setUpdateChecking] = useState(false)
  const [updateError, setUpdateError] = useState<string | null>(null)
  const [draft, setDraft] = useState<PanelSettings | null>(null)
  const [saving, setSaving] = useState(false)
  const [resetOpen, setResetOpen] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [tokenSet, setTokenSet] = useState(hasPanelToken())
  const [activeSection, setActiveSection] = useState<SettingsSectionId>(() =>
    typeof window === 'undefined' ? 'general' : settingsSectionFromHash(window.location.hash),
  )
  const themeRef = useRef(theme)
  themeRef.current = theme
  const initialScrollDone = useRef(false)

  useEffect(() => {
    if (settings.data) setDraft({ ...settings.data, theme: themeRef.current })
  }, [settings.data])

  useEffect(() => {
    setDraft((current) => (current && current.theme !== theme ? { ...current, theme } : current))
  }, [theme])

  const dirtyFields = useMemo(() => {
    const fields = new Set<SettingsFieldKey>()
    if (!draft || !settings.data) return fields
    for (const key of SETTINGS_FIELD_KEYS) {
      if (draft[key] !== settings.data[key]) fields.add(key)
    }
    return fields
  }, [draft, settings.data])

  const dirty = dirtyFields.size > 0

  const dirtySections = useMemo(() => {
    const sections = new Set<SettingsSectionId>()
    for (const field of dirtyFields) sections.add(sectionForField(field))
    return sections
  }, [dirtyFields])

  const update = <K extends keyof PanelSettings>(key: K, value: PanelSettings[K]) => {
    setDraft((current) => (current ? { ...current, [key]: value } : current))
  }

  const resetFields = (fields: readonly SettingsFieldKey[]) => {
    const baseline = settings.data
    if (!baseline) return
    setDraft((current) => {
      if (!current) return current
      const next = { ...current }
      for (const key of fields) Object.assign(next, { [key]: baseline[key] })
      return next
    })
    if (fields.includes('theme')) setTheme(baseline.theme)
  }

  const resetSection = (id: SettingsSectionId) => {
    const section = SETTINGS_SECTIONS.find((candidate) => candidate.id === id)
    if (section) resetFields(section.fields)
  }

  const selectSection = (id: SettingsSectionId) => {
    setActiveSection(id)
    if (window.location.hash !== `#${id}`) {
      window.history.pushState(
        window.history.state,
        '',
        `${window.location.pathname}${window.location.search}#${id}`,
      )
    }
    document.getElementById(id)?.scrollIntoView?.({ block: 'start' })
  }

  useEffect(() => {
    const onHashChange = () => setActiveSection(settingsSectionFromHash(window.location.hash))
    window.addEventListener('hashchange', onHashChange)
    return () => window.removeEventListener('hashchange', onHashChange)
  }, [])

  // The sections mount after the settings request resolves, so the browser's
  // own fragment jump can land before the target exists. Run it once.
  useEffect(() => {
    if (!draft || initialScrollDone.current) return
    initialScrollDone.current = true
    if (!window.location.hash) return
    document
      .getElementById(settingsSectionFromHash(window.location.hash))
      ?.scrollIntoView?.({ block: 'start' })
  }, [draft])

  useEffect(() => {
    if (!dirty) return
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [dirty])

  const save = async () => {
    if (!draft) return
    setSaving(true)
    try {
      const saved = await api.saveSettings({ ...draft, theme })
      setDraft({ ...saved, theme })
      setTheme(saved.theme)
      settings.reload()
      push(t('settings.toast.saved'), 'success')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('settings.toast.saveFailed'), 'danger')
    } finally {
      setSaving(false)
    }
  }

  const resetMock = async () => {
    setResetting(true)
    try {
      await api.resetMock()
      settings.reload()
      window.dispatchEvent(new Event('ajs:refresh'))
      push(t('settings.toast.resetDone'), 'success')
      setResetOpen(false)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : t('settings.toast.resetFailed'), 'danger')
    } finally {
      setResetting(false)
    }
  }

  const checkUpdate = async () => {
    setUpdateChecking(true)
    setUpdateError(null)
    try {
      setUpdateInfo(await api.getUpdate())
    } catch (cause) {
      setUpdateError(cause instanceof Error ? cause.message : t('settings.toast.updateFailed'))
    } finally {
      setUpdateChecking(false)
    }
  }

  useEffect(() => {
    const syncToken = () => setTokenSet(hasPanelToken())
    window.addEventListener('ajs:refresh', syncToken)
    return () => window.removeEventListener('ajs:refresh', syncToken)
  }, [])

  const clearToken = () => {
    setPanelToken(null)
    setTokenSet(false)
    push(t('settings.toast.tokenCleared'), 'default')
  }

  const daemonVersion = overview.data?.version || t('common.unknown')
  const transportReason = t('settings.transport.storedReason')
  const healthReason = t('settings.health.storedReason')
  const subscriptionReason = t('settings.subscription.storedReason')
  const deployCommand = (tag: string) => `cd deploy && ./update.sh --tag ${tag}`

  return (
    <div className="stack-lg">
      <PageHeader
        title={t('settings.title')}
        subtitle={mock ? t('settings.subtitleMock') : t('settings.subtitleLive')}
        actions={
          <>
            {dirty ? (
              <span className="settings-unsaved" role="status">
                {t('settings.unsaved', { count: dirtyFields.size })}
              </span>
            ) : null}
            <Button variant="ghost" onClick={() => resetFields(SETTINGS_FIELD_KEYS)} disabled={!dirty}>
              {t('settings.resetChanges')}
            </Button>
            <Button
              variant="primary"
              icon={<Save size={14} />}
              onClick={() => void save()}
              loading={saving}
              disabled={!dirty}
            >
              {t('settings.save')}
            </Button>
          </>
        }
      />

      {settings.status === 'error' ? (
        <div className="settings-shell">
          <SettingsNav activeId={activeSection} dirtySections={EMPTY_SECTIONS} onSelect={selectSection} />
          <div className="settings-content">
            <Card>
              <ErrorState
                message={settings.error ?? t('common.unknownError')}
                onRetry={settings.reload}
              />
            </Card>
          </div>
        </div>
      ) : !draft ? (
        <div className="settings-shell">
          <SettingsNav activeId={activeSection} dirtySections={EMPTY_SECTIONS} onSelect={selectSection} />
          <div className="settings-content">
            <Card title={t('settings.loading')}>
              <Skeleton height={16} />
              <Skeleton height={32} />
              <Skeleton height={12} width="60%" />
            </Card>
            <Card title={t('settings.loading')}>
              <Skeleton height={16} />
              <Skeleton height={32} />
            </Card>
          </div>
        </div>
      ) : (
        <div className="settings-shell">
          <SettingsNav
            activeId={activeSection}
            dirtySections={dirtySections}
            onSelect={selectSection}
          />

          <div className="settings-content">
            <SettingsSection
              id="general"
              title={t('settings.nav.general')}
              description={t('settings.section.general.desc')}
            >
              <Card
                title={t('settings.daemon.title')}
                subtitle={t('settings.daemon.subtitle')}
                actions={
                  !mock ? (
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => window.dispatchEvent(new Event('ajs:token-required'))}
                    >
                      {tokenSet ? t('settings.daemon.replaceToken') : t('settings.daemon.setToken')}
                    </Button>
                  ) : null
                }
              >
                <dl className="facts facts--compact">
                  <div>
                    <dt>{t('settings.daemon.endpoint')}</dt>
                    <dd className="mono">{apiEndpointLabel()}</dd>
                  </div>
                  <div>
                    <dt>{t('settings.daemon.mode')}</dt>
                    <dd>
                      <Badge tone={mock ? 'mint' : 'info'}>
                        {mock ? t('shell.envMock') : t('shell.envLive')}
                      </Badge>
                    </dd>
                  </div>
                  <div>
                    <dt>{t('settings.daemon.version')}</dt>
                    <dd className="mono">{daemonVersion}</dd>
                  </div>
                  <div>
                    <dt>{t('settings.daemon.commit')}</dt>
                    <dd className="mono">{overview.data?.commit || t('common.notEmbedded')}</dd>
                  </div>
                  <div>
                    <dt>{t('settings.daemon.transport')}</dt>
                    <dd className="mono">{draft.transport}</dd>
                  </div>
                  <div>
                    <dt>{t('settings.daemon.token')}</dt>
                    <dd>
                      <Badge tone={mock ? 'neutral' : tokenSet ? 'success' : 'warning'}>
                        {mock
                          ? t('settings.daemon.tokenNotRequired')
                          : tokenSet
                            ? t('settings.daemon.tokenSet')
                            : t('settings.daemon.tokenMissing')}
                      </Badge>
                    </dd>
                  </div>
                </dl>
                <p className="setting-note">
                  <Database size={12} aria-hidden="true" />{' '}
                  {mock ? (
                    <>
                      {t('settings.daemon.mockNotePrefix')}{' '}
                      <code className="mono">VITE_USE_MOCK=false</code>{' '}
                      {t('settings.daemon.mockNoteMiddle')} <code className="mono">VITE_API_BASE</code>{' '}
                      {t('settings.daemon.mockNoteSuffix')}
                    </>
                  ) : (
                    <>
                      {t('settings.daemon.liveNote')}
                      {tokenSet ? (
                        <>
                          {' '}
                          <button type="button" className="link-button" onClick={clearToken}>
                            {t('settings.daemon.clearToken')}
                          </button>
                        </>
                      ) : null}
                    </>
                  )}
                </p>
              </Card>

              {mock ? (
                <Card
                  className="danger-card"
                  title={t('settings.reset.title')}
                  subtitle={t('settings.reset.subtitle')}
                >
                  <div className="danger-card__row">
                    <p className="setting-note">{t('settings.reset.note')}</p>
                    <Button
                      variant="danger"
                      icon={<RefreshCw size={14} />}
                      onClick={() => setResetOpen(true)}
                    >
                      {t('settings.reset.button')}
                    </Button>
                  </div>
                </Card>
              ) : null}
            </SettingsSection>

            <SettingsSection
              id="appearance"
              title={t('settings.nav.appearance')}
              description={t('settings.section.appearance.desc')}
              onReset={() => resetSection('appearance')}
              dirty={dirtySections.has('appearance')}
            >
              <Card
                title={t('settings.appearance.cardTitle')}
                subtitle={t('settings.appearance.subtitle')}
              >
                <SegmentedControl
                  label={t('settings.appearance.theme')}
                  value={theme}
                  onChange={(value) => {
                    setTheme(value)
                    update('theme', value)
                  }}
                  options={[
                    { value: 'dark', label: t('settings.appearance.dark'), icon: <Moon size={13} /> },
                    { value: 'light', label: t('settings.appearance.light'), icon: <Sun size={13} /> },
                  ]}
                />
                <div className="accent-field">
                  <span className="accent-field__label" id="accent-group-label">
                    {t('settings.appearance.accent')}
                  </span>
                  <div className="accent-picker" role="group" aria-labelledby="accent-group-label">
                    {ACCENTS.map((option) => (
                      <button
                        key={option.value}
                        type="button"
                        className={clsx(
                          'accent-picker__item',
                          accent === option.value && 'is-active',
                        )}
                        aria-pressed={accent === option.value}
                        onClick={() => setAccent(option.value)}
                      >
                        <span
                          className={clsx(
                            'accent-picker__dot',
                            `accent-picker__dot--${option.value}`,
                          )}
                          aria-hidden="true"
                        />
                        {t(option.labelKey)}
                      </button>
                    ))}
                  </div>
                </div>
                <p className="setting-note">{t('settings.appearance.note')}</p>
              </Card>
            </SettingsSection>

            <SettingsSection
              id="language"
              title={t('settings.nav.language')}
              description={t('settings.section.language.desc')}
            >
              <Card title={t('settings.language.cardTitle')} subtitle={t('settings.language.subtitle')}>
                <SegmentedControl
                  label={t('settings.language.label')}
                  value={lang}
                  onChange={setLang}
                  options={[
                    { value: 'zh', label: t('settings.language.zh') },
                    { value: 'en', label: t('settings.language.en') },
                  ]}
                />
                <p className="setting-note">{t('settings.language.note')}</p>
              </Card>
            </SettingsSection>

            <SettingsSection
              id="connection"
              title={t('settings.nav.connection')}
              description={t('settings.section.connection.desc')}
              onReset={() => resetSection('connection')}
              dirty={dirtySections.has('connection')}
            >
              <div className="settings-grid">
                <Card title={t('settings.transport.title')} subtitle={t('settings.transport.subtitle')}>
                  <SettingsField
                    label={t('settings.transport.title')}
                    badge={<EffectiveBadge kind="stored" reason={transportReason} />}
                    reason={transportReason}
                    reasonId="transport-reason"
                  >
                    <SettingsSegmented
                      label={t('settings.transport.title')}
                      value={draft.transport}
                      onChange={(value) => update('transport', value)}
                      disabled
                      describedBy="transport-reason"
                      options={[
                        { value: 'httpupgrade', label: t('settings.transport.httpupgrade') },
                        { value: 'rawtcp', label: t('settings.transport.rawtcp') },
                      ]}
                    />
                  </SettingsField>
                  <p className="setting-note">
                    <code className="mono">httpupgrade</code> {t('settings.transport.notePrefix')}{' '}
                    <code className="mono">rawtcp</code> {t('settings.transport.noteMiddle')}
                  </p>
                </Card>

                <Card title={t('settings.health.title')} subtitle={t('settings.health.subtitle')}>
                  <SettingsField
                    label={t('settings.health.interval')}
                    htmlFor="health-interval"
                    hint={t('settings.health.hint')}
                    badge={<EffectiveBadge kind="stored" reason={healthReason} />}
                    reason={healthReason}
                  >
                    <div className="input-affix">
                      <Input
                        id="health-interval"
                        type="number"
                        min={10}
                        max={600}
                        value={draft.healthIntervalSec}
                        disabled
                        aria-describedby="health-interval-reason"
                        onChange={(event) =>
                          update('healthIntervalSec', Number(event.target.value))
                        }
                      />
                      <span className="input-affix__unit">{t('common.sec')}</span>
                    </div>
                  </SettingsField>
                </Card>
              </div>
              <p className="page-note">{t('settings.section.connection.note')}</p>
            </SettingsSection>

            <SettingsSection
              id="leases"
              title={t('settings.nav.leases')}
              description={t('settings.section.leases.desc')}
              onReset={() => resetSection('leases')}
              dirty={dirtySections.has('leases')}
            >
              <div className="settings-grid">
                <Card title={t('settings.leases.cardTitle')} subtitle={t('settings.leases.subtitle')}>
                  <SettingsField
                    label={t('settings.leases.ttl')}
                    htmlFor="lease-ttl"
                    hint={t('settings.leases.hint')}
                    badge={<EffectiveBadge kind="applied" />}
                  >
                    <Select
                      variant="settings"
                      id="lease-ttl"
                      value={draft.leaseTtl}
                      onChange={(value) => update('leaseTtl', value)}
                      options={[
                        {
                          value: '1h',
                          label: t('settings.leases.1h'),
                          description: t('settings.leases.1hDesc'),
                        },
                        {
                          value: '4h',
                          label: t('settings.leases.4h'),
                          description: t('settings.leases.4hDesc'),
                        },
                        {
                          value: '8h',
                          label: t('settings.leases.8h'),
                          description: t('settings.leases.8hDesc'),
                        },
                        {
                          value: '24h',
                          label: t('settings.leases.24h'),
                          description: t('settings.leases.24hDesc'),
                        },
                      ]}
                    />
                  </SettingsField>
                </Card>

                <Card
                  title={t('settings.subscription.title')}
                  subtitle={t('settings.subscription.subtitle')}
                >
                  <SettingsField
                    label={t('settings.subscription.interval')}
                    htmlFor="subscription-interval"
                    hint={t('settings.subscription.hint')}
                    badge={<EffectiveBadge kind="stored" reason={subscriptionReason} />}
                    reason={subscriptionReason}
                  >
                    <div className="input-affix">
                      <Input
                        id="subscription-interval"
                        type="number"
                        min={5}
                        max={300}
                        value={draft.subscriptionRefreshSec}
                        disabled
                        aria-describedby="subscription-interval-reason"
                        onChange={(event) =>
                          update('subscriptionRefreshSec', Number(event.target.value))
                        }
                      />
                      <span className="input-affix__unit">{t('common.sec')}</span>
                    </div>
                  </SettingsField>
                </Card>
              </div>
            </SettingsSection>

            <SettingsSection
              id="updates"
              title={t('settings.nav.updates')}
              description={t('settings.section.updates.desc')}
            >
              <Card title={t('settings.updates.cardTitle')} subtitle={t('settings.updates.subtitle')}>
                <dl className="facts facts--compact">
                  <div>
                    <dt>{t('settings.updates.current')}</dt>
                    <dd className="mono">{daemonVersion}</dd>
                  </div>
                </dl>
                <div className="update-card__row">
                  <Button
                    variant="secondary"
                    icon={<RefreshCw size={14} />}
                    onClick={() => void checkUpdate()}
                    loading={updateChecking}
                  >
                    {t('settings.updates.check')}
                  </Button>
                  {updateInfo && updateInfo.enabled && updateInfo.latest ? (
                    <Badge tone={updateInfo.updateAvailable ? 'warning' : 'success'}>
                      {updateInfo.updateAvailable
                        ? t('settings.updates.available')
                        : t('settings.updates.upToDate')}
                    </Badge>
                  ) : null}
                </div>
                {updateError ? (
                  <p className="setting-note" role="alert">
                    {t('settings.updates.failed', { error: updateError })}
                  </p>
                ) : null}
                {!updateChecking && !updateError && updateInfo && !updateInfo.enabled ? (
                  <p className="setting-note">
                    {t('settings.updates.disabledPrefix')}{' '}
                    <code className="mono">AJS_UPDATE_REPO</code>{' '}
                    {t('settings.updates.disabledSuffix')}
                  </p>
                ) : null}
                {updateInfo && updateInfo.enabled && updateInfo.latest ? (
                  <dl className="facts facts--compact">
                    <div>
                      <dt>{t('settings.updates.latest')}</dt>
                      <dd className="mono">{updateInfo.latest}</dd>
                    </div>
                    {updateInfo.publishedAt ? (
                      <div>
                        <dt>{t('settings.updates.published')}</dt>
                        <dd>{formatDateTime(updateInfo.publishedAt)}</dd>
                      </div>
                    ) : null}
                  </dl>
                ) : null}
                {updateInfo && updateInfo.enabled && updateInfo.latest ? (
                  <div className="update-card__command">
                    <code className="mono">{deployCommand(updateInfo.latest)}</code>
                    <CopyButton
                      value={deployCommand(updateInfo.latest)}
                      label={t('settings.updates.copyCommand')}
                      toastMessage={t('settings.updates.copied')}
                      withLabel
                    />
                    {updateInfo.url ? (
                      <a
                        className="update-card__link"
                        href={updateInfo.url}
                        target="_blank"
                        rel="noreferrer noopener"
                      >
                        <ExternalLink size={12} aria-hidden="true" />{' '}
                        {t('settings.updates.releaseNotes')}
                      </a>
                    ) : null}
                  </div>
                ) : null}
                {updateInfo?.checkedAt ? (
                  <p className="setting-note">
                    {t('settings.updates.checked', { time: formatDateTime(updateInfo.checkedAt) })}
                  </p>
                ) : null}
                <p className="setting-note">
                  <EffectiveBadge
                    kind="later"
                    reason={t('settings.updates.laterReason')}
                  />{' '}
                  {t('settings.updates.later')}
                </p>
              </Card>
            </SettingsSection>

            <SettingsSection
              id="integrations"
              title={t('settings.nav.integrations')}
              description={t('settings.section.integrations.desc')}
            >
              <Card
                title={t('settings.integrations.cardTitle')}
                subtitle={t('settings.integrations.subtitle')}
              >
                <p className="setting-note">{t('settings.integrations.readOnly')}</p>
                <div className="settings-facts">
                  <div className="settings-facts__row">
                    <span className="settings-facts__label">
                      {t('settings.integrations.resin')}
                    </span>
                    <span className="settings-facts__value">
                      {t('settings.integrations.resinDesc')}
                    </span>
                    <Badge className="settings-facts__badge" tone="neutral">
                      {t('settings.integrations.notConfigured')}
                    </Badge>
                  </div>
                  <div className="settings-facts__row">
                    <span className="settings-facts__label">
                      {t('settings.integrations.prometheus')}
                    </span>
                    <span className="settings-facts__value">
                      {t('settings.integrations.prometheusDesc')}
                    </span>
                    <Badge className="settings-facts__badge" tone="neutral">
                      {t('settings.integrations.notConfigured')}
                    </Badge>
                  </div>
                </div>
                <p className="setting-note">
                  {t('settings.integrations.docs')} <code>docs/PANEL.md</code> ·{' '}
                  <code>daemon/README.md</code> · <code>dashboard/README.md</code>
                </p>
              </Card>
              <WebhooksSection />
            </SettingsSection>

            <SettingsSection
              id="access"
              title={t('settings.nav.access')}
              description={t('settings.section.access.desc')}
            >
              <AccessSection />
            </SettingsSection>

            <SettingsSection
              id="audit"
              title={t('settings.nav.audit')}
              description={t('settings.section.audit.desc')}
            >
              <Card title={t('settings.audit.cardTitle')} subtitle={t('settings.audit.subtitle')}>
                <div className="settings-facts">
                  <div className="settings-facts__row">
                    <span className="settings-facts__label">{t('settings.audit.retention')}</span>
                    <span className="settings-facts__value">
                      {t('settings.audit.retentionValue')}
                    </span>
                    <span className="settings-facts__badge">
                      <EffectiveBadge kind="locked" reason={t('settings.audit.retentionReason')} />
                    </span>
                  </div>
                  <div className="settings-facts__row">
                    <span className="settings-facts__label">{t('settings.audit.requests')}</span>
                    <span className="settings-facts__value">{t('settings.audit.requestsValue')}</span>
                  </div>
                  <div className="settings-facts__row">
                    <span className="settings-facts__label">{t('settings.audit.source')}</span>
                    <span className="settings-facts__value">
                      <code className="mono">GET /v1/audit</code>
                    </span>
                  </div>
                </div>
                <p className="setting-note">
                  <EffectiveBadge kind="later" reason={t('settings.audit.exportReason')} />{' '}
                  {t('settings.audit.exportLater')}
                </p>
                <p className="setting-note">
                  <Link className="settings-inline-link" to="/logs">
                    {t('settings.audit.openLogs')}
                  </Link>{' '}
                  — {t('settings.audit.openLogsHint')}
                </p>
              </Card>
            </SettingsSection>
          </div>
        </div>
      )}

      {mock ? (
        <ConfirmDialog
          open={resetOpen}
          title={t('settings.reset.confirmTitle')}
          description={t('settings.reset.confirmDescription')}
          confirmLabel={t('settings.reset.button')}
          busy={resetting}
          onConfirm={() => void resetMock()}
          onCancel={() => setResetOpen(false)}
        />
      ) : null}
    </div>
  )
}

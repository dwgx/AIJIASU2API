import clsx from 'clsx'
import { Database, ExternalLink, Moon, RefreshCw, Save, Sun } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api, isMockMode } from '../lib/api'
import { CopyButton } from '../components/CopyButton'
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
  Field,
  Input,
  PageHeader,
  SegmentedControl,
  Select,
  Skeleton,
} from '../components/primitives'

const ACCENTS: ReadonlyArray<{ value: AccentName; label: string }> = [
  { value: 'mint', label: 'Mint' },
  { value: 'indigo', label: 'Indigo' },
  { value: 'violet', label: 'Violet' },
  { value: 'orange', label: 'Orange' },
]

export default function Settings() {
  const mock = isMockMode()
  const settings = useAsync(() => api.getSettings(), [])
  const { push } = useToast()
  const { theme, setTheme, accent, setAccent } = useTheme()
  const [updateInfo, setUpdateInfo] = useState<UpdateInfo | null>(null)
  const [updateChecking, setUpdateChecking] = useState(false)
  const [updateError, setUpdateError] = useState<string | null>(null)
  const [draft, setDraft] = useState<PanelSettings | null>(null)
  const [saving, setSaving] = useState(false)
  const [resetOpen, setResetOpen] = useState(false)
  const [resetting, setResetting] = useState(false)
  const themeRef = useRef(theme)
  themeRef.current = theme

  useEffect(() => {
    if (settings.data) setDraft({ ...settings.data, theme: themeRef.current })
  }, [settings.data])

  useEffect(() => {
    setDraft((current) => (current && current.theme !== theme ? { ...current, theme } : current))
  }, [theme])

  const dirty = draft !== null && settings.data !== undefined && JSON.stringify(draft) !== JSON.stringify(settings.data)

  const update = <K extends keyof PanelSettings>(key: K, value: PanelSettings[K]) => {
    setDraft((current) => (current ? { ...current, [key]: value } : current))
  }

  const save = async () => {
    if (!draft) return
    if (draft.healthIntervalSec < 10 || draft.healthIntervalSec > 600) {
      push('Health interval must be between 10 and 600 seconds', 'danger')
      return
    }
    if (draft.subscriptionRefreshSec < 5 || draft.subscriptionRefreshSec > 300) {
      push('Subscription refresh must be between 5 and 300 seconds', 'danger')
      return
    }
    setSaving(true)
    try {
      const saved = await api.saveSettings({ ...draft, theme })
      setDraft({ ...saved, theme })
      setTheme(saved.theme)
      settings.reload()
      push('Settings saved', 'success')
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Could not save settings', 'danger')
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
      push('Mock dataset reset to its initial state', 'success')
      setResetOpen(false)
    } catch (cause) {
      push(cause instanceof Error ? cause.message : 'Reset failed', 'danger')
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
      setUpdateError(cause instanceof Error ? cause.message : 'Update check failed')
    } finally {
      setUpdateChecking(false)
    }
  }

  const deployCommand = (tag: string) => `cd deploy && ./update.sh --tag ${tag}`


  return (
    <div className="stack-lg">
      <PageHeader
        title="Settings"
        subtitle={
          mock
            ? 'Panel defaults. Mock mode keeps changes in this browser only.'
            : 'Panel defaults. Changes are saved to ajs-accountd.'
        }
        actions={
          <Button
            variant="primary"
            icon={<Save size={14} />}
            onClick={() => void save()}
            loading={saving}
            disabled={!dirty}
          >
            Save changes
          </Button>
        }
      />

      {settings.status === 'error' ? (
        <Card>
          <ErrorState message={settings.error ?? 'Unknown error'} onRetry={settings.reload} />
        </Card>
      ) : !draft ? (
        <div className="settings-grid">
          {Array.from({ length: 4 }, (_, index) => (
            <Card key={index} title="Loading">
              <Skeleton height={16} />
              <Skeleton height={32} />
              <Skeleton height={12} width="60%" />
            </Card>
          ))}
        </div>
      ) : (
        <div className="settings-grid">
          <Card title="Transport" subtitle="How ajsproxy reaches the vendor FV endpoint.">
            <SegmentedControl
              label="Transport"
              value={draft.transport}
              onChange={(value) => update('transport', value)}
              options={[
                { value: 'httpupgrade', label: 'HTTP upgrade' },
                { value: 'rawtcp', label: 'Raw TCP' },
              ]}
            />
            <p className="setting-note">
              <code className="mono">httpupgrade</code> matches the 4.x client default. Switch to{' '}
              <code className="mono">rawtcp</code> only for a version profile that requires it; the
              choice applies to new sessions, not running ones.
            </p>
          </Card>

          <Card title="Health checks" subtitle="Canary cadence for LIVE nodes.">
            <Field
              label="Interval"
              htmlFor="health-interval"
              hint="10–600 seconds. Each probe exits through the gate and verifies the egress IP."
            >
              <div className="input-affix">
                <Input
                  id="health-interval"
                  type="number"
                  min={10}
                  max={600}
                  value={draft.healthIntervalSec}
                  onChange={(event) => update('healthIntervalSec', Number(event.target.value))}
                />
                <span className="input-affix__unit">sec</span>
              </div>
            </Field>
          </Card>

          <Card title="Leases" subtitle="Default TTL for new user assignments.">
            <Field label="Lease TTL" htmlFor="lease-ttl" hint="Existing leases keep their original TTL.">
              <Select
                id="lease-ttl"
                value={draft.leaseTtl}
                onChange={(event) => update('leaseTtl', event.target.value)}
              >
                <option value="1h">1 hour</option>
                <option value="4h">4 hours</option>
                <option value="8h">8 hours</option>
                <option value="24h">24 hours</option>
              </Select>
            </Field>
          </Card>

          <Card title="Subscription" subtitle="How often ajs-accountd republishes ajs-live.">
            <Field
              label="Refresh interval"
              htmlFor="subscription-interval"
              hint="5–300 seconds. Publishes are coalesced to one PATCH per window."
            >
              <div className="input-affix">
                <Input
                  id="subscription-interval"
                  type="number"
                  min={5}
                  max={300}
                  value={draft.subscriptionRefreshSec}
                  onChange={(event) => update('subscriptionRefreshSec', Number(event.target.value))}
                />
                <span className="input-affix__unit">sec</span>
              </div>
            </Field>
          </Card>

          <Card title="Appearance" subtitle="Theme and accent, stored in this browser.">
            <SegmentedControl
              label="Theme"
              value={theme}
              onChange={(value) => {
                setTheme(value)
                update('theme', value)
              }}
              options={[
                { value: 'dark', label: 'Dark', icon: <Moon size={13} /> },
                { value: 'light', label: 'Light', icon: <Sun size={13} /> },
              ]}
            />
            <div className="accent-field">
              <span className="accent-field__label" id="accent-group-label">
                Accent
              </span>
              <div className="accent-picker" role="group" aria-labelledby="accent-group-label">
                {ACCENTS.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    className={clsx('accent-picker__item', accent === option.value && 'is-active')}
                    aria-pressed={accent === option.value}
                    onClick={() => setAccent(option.value)}
                  >
                    <span
                      className={clsx('accent-picker__dot', `accent-picker__dot--${option.value}`)}
                      aria-hidden="true"
                    />
                    {option.label}
                  </button>
                ))}
              </div>
            </div>
            <p className="setting-note">
              The theme and accent are stored per browser (<code className="mono">ajs.theme</code>,{' '}
              <code className="mono">ajs.accent</code>) and are not sent to the daemon. Dark is the
              reference palette; light is a derived variant for bright rooms.
            </p>
          </Card>

          <Card title="Daemon" subtitle="Read-only connection facts.">
            <dl className="facts facts--compact">
              <div>
                <dt>Endpoint</dt>
                <dd className="mono">127.0.0.1:2261</dd>
              </div>
              <div>
                <dt>Mode</dt>
                <dd>
                  <Badge tone={mock ? 'mint' : 'info'}>
                    {mock ? 'mock data' : 'live API'}
                  </Badge>
                </dd>
              </div>
              <div>
                <dt>Version</dt>
                <dd className="mono">0.1.0</dd>
              </div>
              <div>
                <dt>Transport</dt>
                <dd className="mono">{draft.transport}</dd>
              </div>
            </dl>
            <p className="setting-note">
              <Database size={12} aria-hidden="true" />{' '}
              {mock ? (
                <>
                  Set <code className="mono">VITE_USE_MOCK=false</code> and{' '}
                  <code className="mono">VITE_API_BASE</code> to target a real daemon. No credentials are
                  bundled in this build.
                </>
              ) : (
                <>
                  The panel talks to ajs-accountd at this endpoint; the panel token is supplied at build
                  time and never rendered here.
                </>
              )}
            </p>
          </Card>

          <Card title="Updates" subtitle="Read-only GitHub release check.">
            <dl className="facts facts--compact">
              <div>
                <dt>Current</dt>
                <dd className="mono">0.1.0</dd>
              </div>
            </dl>
            <div className="update-card__row">
              <Button
                variant="secondary"
                icon={<RefreshCw size={14} />}
                onClick={() => void checkUpdate()}
                loading={updateChecking}
              >
                Check
              </Button>
              {updateInfo && updateInfo.enabled && updateInfo.latest ? (
                <Badge tone={updateInfo.updateAvailable ? 'warning' : 'success'}>
                  {updateInfo.updateAvailable ? 'Update available' : 'Up to date'}
                </Badge>
              ) : null}
            </div>
            {updateError ? (
              <p className="setting-note" role="alert">
                Update check failed: {updateError}
              </p>
            ) : null}
            {!updateChecking && !updateError && updateInfo && !updateInfo.enabled ? (
              <p className="setting-note">
                Update checks are disabled: <code className="mono">AJS_UPDATE_REPO</code> is not
                configured on the daemon. The daemon never updates itself; the host runs{' '}
                <code className="mono">deploy/update.sh</code>.
              </p>
            ) : null}
            {updateInfo && updateInfo.enabled && updateInfo.latest ? (
              <dl className="facts facts--compact">
                <div>
                  <dt>Latest</dt>
                  <dd className="mono">{updateInfo.latest}</dd>
                </div>
                {updateInfo.publishedAt ? (
                  <div>
                    <dt>Published</dt>
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
                  label="Copy deploy command"
                  toastMessage="Deploy command copied"
                  withLabel
                />
                {updateInfo.url ? (
                  <a
                    className="update-card__link"
                    href={updateInfo.url}
                    target="_blank"
                    rel="noreferrer noopener"
                  >
                    <ExternalLink size={12} aria-hidden="true" /> Release notes
                  </a>
                ) : null}
              </div>
            ) : null}
            {updateInfo?.checkedAt ? (
              <p className="setting-note">Checked {formatDateTime(updateInfo.checkedAt)}</p>
            ) : null}
          </Card>
        </div>
      )}

      {mock ? (
        <Card className="danger-card" title="Reset mock data" subtitle="Restore accounts, nodes, leases, keys and logs.">
          <div className="danger-card__row">
            <p className="setting-note">
              This only affects the in-browser mock store. It does not touch a daemon, database or
              vendor account.
            </p>
            <Button variant="danger" icon={<RefreshCw size={14} />} onClick={() => setResetOpen(true)}>
              Reset dataset
            </Button>
          </div>
        </Card>
      ) : null}

      {mock ? (
        <ConfirmDialog
          open={resetOpen}
          title="Reset mock data?"
          description="Every change made in this session — created accounts, rotated leases, revoked keys — is discarded. Settings are preserved."
          confirmLabel="Reset dataset"
          busy={resetting}
          onConfirm={() => void resetMock()}
          onCancel={() => setResetOpen(false)}
        />
      ) : null}
    </div>
  )
}

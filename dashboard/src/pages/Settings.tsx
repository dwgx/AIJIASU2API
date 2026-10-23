import { Database, Moon, RefreshCw, Save, Sun } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api, isMockMode } from '../lib/api'
import { useTheme } from '../lib/theme'
import type { PanelSettings } from '../lib/types'
import { useAsync } from '../lib/useAsync'
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

export default function Settings() {
  const mock = isMockMode()
  const settings = useAsync(() => api.getSettings(), [])
  const { push } = useToast()
  const { theme, setTheme } = useTheme()
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

          <Card title="Appearance" subtitle="Obsidian Mint, dark-first.">
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
            <p className="setting-note">
              The theme is stored per browser. Dark is the reference palette; light is a derived
              variant for bright rooms.
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

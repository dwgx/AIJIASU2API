import { useId, useMemo, useState } from 'react'
import { Pencil, Plus, RefreshCw, ScrollText, Trash2 } from 'lucide-react'
import { api } from '../../lib/api'
import { ApiError } from '../../lib/errors'
import { formatDateTimeSeconds, formatRelative } from '../../lib/format'
import { useI18n } from '../../lib/i18n'
import type {
  Delivery,
  WebhookCreated,
  WebhookEndpoint,
  WebhookEvent,
} from '../../lib/types'
import { WEBHOOK_EVENTS } from '../../lib/types'
import { useAsync } from '../../lib/useAsync'
import { CopyButton } from '../CopyButton'
import { DataTable } from '../DataTable'
import { ConfirmDialog, Drawer, Modal } from '../Overlay'
import { useToast } from '../Toast'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  Input,
  Select,
  Skeleton,
  Switch,
  TableSkeleton,
} from '../primitives'
import './WebhooksSection.css'

type Translate = (key: string, params?: Record<string, string | number>) => string

interface WebhookRow {
  endpoint: WebhookEndpoint
  lastDelivery: Delivery | null
  /** False when the deliveries request failed; the list still renders. */
  deliveriesKnown: boolean
}

interface WebhookFormState {
  url: string
  label: string
  events: WebhookEvent[]
  enabled: boolean
}

/** The label when present, otherwise the URL host — never the full URL. */
function endpointName(endpoint: WebhookEndpoint): string {
  const label = endpoint.description?.trim()
  if (label) return label
  try {
    return new URL(endpoint.url).host
  } catch {
    return endpoint.url
  }
}

function isLoopbackHost(host: string): boolean {
  const value = host.toLowerCase()
  return (
    value === 'localhost' ||
    value === '::1' ||
    value === '[::1]' ||
    /^127(?:\.\d{1,3}){3}$/.test(value)
  )
}

/** Mirrors the daemon's receiver validation so the dialog can explain it first. */
function validateUrl(raw: string, t: Translate): string | null {
  const value = raw.trim()
  if (!value) return t('settings.webhooks.urlRequired')
  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return t('settings.webhooks.urlInvalid')
  }
  if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') {
    return t('settings.webhooks.urlInvalid')
  }
  if (parsed.username || parsed.password) return t('settings.webhooks.urlInvalid')
  if (parsed.protocol === 'http:' && !isLoopbackHost(parsed.hostname)) {
    return t('settings.webhooks.urlLoopback')
  }
  return null
}

function sameEvents(left: readonly string[], right: readonly string[]): boolean {
  if (left.length !== right.length) return false
  const a = [...left].sort()
  const b = [...right].sort()
  return a.every((value, index) => value === b[index])
}

function actionError(t: Translate, cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 409) return t('settings.webhooks.conflict')
    if (cause.status === 404) return t('settings.webhooks.notFound')
    if (cause.status === 503) return t('settings.webhooks.serviceUnavailable')
    return cause.message
  }
  return cause instanceof Error ? cause.message : String(cause)
}

function deliveryTone(status: Delivery['status']): 'success' | 'warning' | 'danger' {
  if (status === 'success') return 'success'
  if (status === 'dead_letter') return 'danger'
  return 'warning'
}

/** The delivery record stores the daemon's error text; reduce it to a class. */
function deliveryErrorClass(delivery: Delivery, t: Translate): string | null {
  if (!delivery.error) return null
  if (delivery.httpStatus === undefined) return t('settings.webhooks.delivery.transport')
  if (delivery.httpStatus === 429) return t('settings.webhooks.delivery.rateLimited')
  if (delivery.httpStatus >= 500) return t('settings.webhooks.delivery.serverError')
  if (delivery.httpStatus >= 400) return t('settings.webhooks.delivery.clientError')
  return t('settings.webhooks.delivery.httpError')
}

function deliveryErrorTone(delivery: Delivery): 'danger' | 'warning' | 'neutral' {
  if (!delivery.error) return 'neutral'
  if (delivery.httpStatus === undefined) return 'danger'
  if (delivery.httpStatus >= 500 || delivery.httpStatus === 429) return 'warning'
  return 'neutral'
}

/** One page shared by the row header and the drawer; the daemon pages newest first. */
const DELIVERY_PAGE_LIMIT = 50

async function loadRows(): Promise<WebhookRow[]> {
  const endpoints = await api.listWebhooks()
  return Promise.all(
    endpoints.map(async (endpoint) => {
      try {
        // The daemon answers newest first, so index 0 is the last attempt.
        const deliveries = await api.listWebhookDeliveries(endpoint.id, {
          limit: DELIVERY_PAGE_LIMIT,
        })
        return {
          endpoint,
          lastDelivery: deliveries[0] ?? null,
          deliveriesKnown: true,
        }
      } catch {
        return { endpoint, lastDelivery: null, deliveriesKnown: false }
      }
    }),
  )
}

/**
 * Webhook receivers live in Settings → Integrations: one row per receiver with
 * its event subscription, last delivery, enabled switch and actions. The
 * signing secret exists only in the create/replace response, so it is rendered
 * once in a masked panel and never written to storage or the list.
 */
export function WebhooksSection() {
  const { t, lang } = useI18n()
  const { push } = useToast()
  const rows = useAsync(loadRows, [])
  const [createOpen, setCreateOpen] = useState(false)
  const [editTarget, setEditTarget] = useState<WebhookEndpoint | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<WebhookEndpoint | null>(null)
  const [deliveriesTarget, setDeliveriesTarget] = useState<WebhookEndpoint | null>(null)
  const [secret, setSecret] = useState<WebhookCreated | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [busyId, setBusyId] = useState<string | null>(null)

  const endpoints = rows.data ?? []

  const toggle = async (endpoint: WebhookEndpoint, enabled: boolean) => {
    setBusyId(endpoint.id)
    try {
      await api.updateWebhook(endpoint.id, { enabled })
      rows.reload()
      push(
        t(
          enabled ? 'settings.webhooks.enabledToast' : 'settings.webhooks.disabledToast',
          { name: endpointName(endpoint) },
        ),
        'success',
      )
    } catch (cause) {
      push(actionError(t, cause), 'danger')
    } finally {
      setBusyId(null)
    }
  }

  const handleSaved = (created: WebhookCreated | null) => {
    setCreateOpen(false)
    setEditTarget(null)
    rows.reload()
    if (created) setSecret(created)
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      await api.deleteWebhook(deleteTarget.id)
      push(t('settings.webhooks.deleted', { name: endpointName(deleteTarget) }), 'success')
      setDeleteTarget(null)
      rows.reload()
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 404) {
        push(t('settings.webhooks.notFound'), 'danger')
        setDeleteTarget(null)
        rows.reload()
      } else {
        push(actionError(t, cause), 'danger')
      }
    } finally {
      setDeleting(false)
    }
  }

  return (
    <Card
      title={t('platform.webhooks.title')}
      subtitle={t('settings.webhooks.subtitle')}
      actions={
        <div className="webhooks-head-actions">
          {rows.data ? (
            <span className="webhooks-count">
              {t(
                endpoints.length === 1
                  ? 'settings.webhooks.countOne'
                  : 'settings.webhooks.count',
                { count: endpoints.length },
              )}
            </span>
          ) : null}
          <IconButton
            label={t('settings.webhooks.refresh')}
            icon={<RefreshCw size={15} />}
            onClick={rows.reload}
          />
          <Button
            size="sm"
            variant="primary"
            icon={<Plus size={14} />}
            onClick={() => setCreateOpen(true)}
          >
            {t('settings.webhooks.new')}
          </Button>
        </div>
      }
    >
      {rows.status === 'loading' && !rows.data ? (
        <div className="webhooks-skeleton" aria-busy="true">
          <Skeleton height={64} />
          <Skeleton height={64} />
        </div>
      ) : rows.status === 'error' && !rows.data ? (
        <ErrorState
          message={rows.error ?? t('settings.webhooks.loadFailed')}
          onRetry={rows.reload}
        />
      ) : endpoints.length === 0 ? (
        <EmptyState
          title={t('platform.webhooks.empty')}
          description={t('settings.webhooks.emptyDesc')}
          action={
            <Button
              variant="primary"
              icon={<Plus size={14} />}
              onClick={() => setCreateOpen(true)}
            >
              {t('settings.webhooks.new')}
            </Button>
          }
        />
      ) : (
        <>
          {rows.error ? (
            <p className="form-error" role="alert">
              {rows.error}
            </p>
          ) : null}
          <ul className="webhooks-list" aria-label={t('platform.webhooks.title')}>
            {endpoints.map((row) => {
              const { endpoint, lastDelivery, deliveriesKnown } = row
              const name = endpointName(endpoint)
              const events = endpoint.events ?? []
              return (
                <li className="webhooks-row" key={endpoint.id}>
                  <div className="webhooks-row__main">
                    <div className="webhooks-row__head">
                      <span className="webhooks-row__name">{name}</span>
                      {!endpoint.secretAvailable ? (
                        <Badge tone="warning">
                          {t('platform.webhooks.secretUnavailable')}
                        </Badge>
                      ) : null}
                      {!endpoint.enabled ? (
                        <Badge tone="neutral">{t('platform.webhooks.disabled')}</Badge>
                      ) : null}
                    </div>
                    <p className="webhooks-row__url mono" title={endpoint.url}>
                      {endpoint.url}
                    </p>
                    <div className="webhooks-row__chips">
                      {events.length === 0 ? (
                        <span className="webhooks-chip webhooks-chip--all">
                          {t('platform.webhooks.allEvents')}
                        </span>
                      ) : (
                        events.map((event) => (
                          <span className="webhooks-chip" key={event}>
                            {event}
                          </span>
                        ))
                      )}
                    </div>
                    <div className="webhooks-row__delivery">
                      <span className="webhooks-row__delivery-label">
                        {t('settings.webhooks.lastDelivery')}
                      </span>
                      {lastDelivery ? (
                        <>
                          <Badge tone={deliveryTone(lastDelivery.status)}>
                            {t(`platform.webhooks.delivery.status.${lastDelivery.status}`)}
                          </Badge>
                          <span className="webhooks-row__delivery-time">
                            {formatRelative(lastDelivery.at, Date.now(), lang)}
                          </span>
                        </>
                      ) : (
                        <span className="webhooks-row__muted">
                          {deliveriesKnown
                            ? t('settings.webhooks.noDeliveries')
                            : t('settings.webhooks.deliveryUnknown')}
                        </span>
                      )}
                    </div>
                    {!endpoint.secretAvailable ? (
                      <p className="webhooks-row__warning">
                        {t('settings.webhooks.secretUnavailableHint')}
                      </p>
                    ) : null}
                  </div>
                  <div className="webhooks-row__actions">
                    <Switch
                      checked={endpoint.enabled}
                      onChange={(next) => void toggle(endpoint, next)}
                      label={
                        endpoint.enabled
                          ? t('settings.webhooks.disableFor', { name })
                          : t('settings.webhooks.enableFor', { name })
                      }
                      disabled={busyId === endpoint.id}
                    />
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<ScrollText size={14} />}
                      onClick={() => setDeliveriesTarget(endpoint)}
                      aria-label={t('settings.webhooks.deliveriesFor', { name })}
                    >
                      {t('platform.webhooks.deliveries')}
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<Pencil size={14} />}
                      onClick={() => setEditTarget(endpoint)}
                      aria-label={t('settings.webhooks.editFor', { name })}
                    >
                      {t('settings.webhooks.edit')}
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<Trash2 size={14} />}
                      onClick={() => setDeleteTarget(endpoint)}
                      aria-label={t('settings.webhooks.deleteFor', { name })}
                    >
                      {t('settings.webhooks.delete')}
                    </Button>
                  </div>
                </li>
              )
            })}
          </ul>
        </>
      )}

      {createOpen ? (
        <WebhookDialog
          mode="create"
          onClose={() => setCreateOpen(false)}
          onSaved={handleSaved}
        />
      ) : null}
      {editTarget ? (
        <WebhookDialog
          mode="edit"
          endpoint={editTarget}
          onClose={() => setEditTarget(null)}
          onSaved={handleSaved}
        />
      ) : null}
      <ConfirmDialog
        open={deleteTarget !== null}
        title={t('settings.webhooks.deleteTitle')}
        description={
          deleteTarget
            ? t('settings.webhooks.deleteDesc', { name: endpointName(deleteTarget) })
            : ''
        }
        confirmLabel={t('settings.webhooks.deleteConfirm')}
        requireText={deleteTarget ? endpointName(deleteTarget) : undefined}
        busy={deleting}
        onConfirm={() => void confirmDelete()}
        onCancel={() => setDeleteTarget(null)}
      />
      {deliveriesTarget ? (
        <DeliveriesDrawer
          endpoint={deliveriesTarget}
          onClose={() => setDeliveriesTarget(null)}
        />
      ) : null}
      {secret ? <SecretDialog created={secret} onClose={() => setSecret(null)} /> : null}
    </Card>
  )
}

function WebhookDialog({
  mode,
  endpoint,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit'
  endpoint?: WebhookEndpoint
  onClose: () => void
  onSaved: (created: WebhookCreated | null) => void
}) {
  const { t } = useI18n()
  const { push } = useToast()
  const baseId = useId()
  const [form, setForm] = useState<WebhookFormState>(() => ({
    url: endpoint?.url ?? '',
    label: endpoint?.description ?? '',
    events: endpoint ? [...(endpoint.events ?? [])] : [],
    enabled: endpoint?.enabled ?? true,
  }))
  const [urlError, setUrlError] = useState<string | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const eventOptions = useMemo(
    () => WEBHOOK_EVENTS.map((event) => ({ value: event, label: event })),
    [],
  )

  const replacing =
    mode === 'edit' &&
    endpoint !== undefined &&
    (form.url.trim() !== endpoint.url ||
      form.label.trim() !== (endpoint.description ?? '') ||
      !sameEvents(form.events, endpoint.events ?? []))
  const dirty =
    mode === 'create' ||
    replacing ||
    (endpoint !== undefined && form.enabled !== endpoint.enabled)

  const save = async () => {
    const invalid = validateUrl(form.url, t)
    if (invalid) {
      setUrlError(invalid)
      return
    }
    setUrlError(null)
    setFormError(null)
    setSaving(true)
    try {
      if (mode === 'create' || !endpoint) {
        const created = await api.createWebhook({
          url: form.url.trim(),
          events: form.events,
          description: form.label.trim(),
          enabled: form.enabled,
        })
        push(t('settings.webhooks.created', { name: endpointName(created) }), 'success')
        onSaved(created)
      } else if (replacing) {
        // The daemon only mutates `enabled`; a changed URL, label or event list
        // is a new receiver. Create first so a failed create leaves the old one
        // intact, then drop the old row (its sealed secret goes with it).
        const created = await api.createWebhook({
          url: form.url.trim(),
          events: form.events,
          description: form.label.trim(),
          enabled: form.enabled,
        })
        let deleteError: string | null = null
        try {
          await api.deleteWebhook(endpoint.id)
        } catch (cause) {
          deleteError = actionError(t, cause)
        }
        push(t('settings.webhooks.replaced', { name: endpointName(created) }), 'success')
        if (deleteError) push(deleteError, 'danger')
        onSaved(created)
      } else {
        await api.updateWebhook(endpoint.id, { enabled: form.enabled })
        push(t('settings.webhooks.updated', { name: endpointName(endpoint) }), 'success')
        onSaved(null)
      }
    } catch (cause) {
      setFormError(actionError(t, cause))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open
      title={mode === 'create' ? t('settings.webhooks.createTitle') : t('settings.webhooks.editTitle')}
      description={
        mode === 'create' ? t('settings.webhooks.createDesc') : t('settings.webhooks.editDesc')
      }
      onClose={saving ? () => {} : onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="primary"
            onClick={() => void save()}
            loading={saving}
            disabled={!dirty}
          >
            {mode === 'create'
              ? t('settings.webhooks.createAction')
              : t('settings.webhooks.saveAction')}
          </Button>
        </>
      }
    >
      <div className="form webhooks-form">
        {formError ? (
          <p className="form-error" role="alert">
            {formError}
          </p>
        ) : null}
        <Field
          label={t('platform.webhooks.url')}
          htmlFor={`${baseId}-url`}
          hint={t('settings.webhooks.urlHint')}
          error={urlError ?? undefined}
        >
          <Input
            id={`${baseId}-url`}
            type="url"
            value={form.url}
            placeholder="https://hooks.example.com/ajs"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => {
              setForm((current) => ({ ...current, url: event.target.value }))
              if (urlError) setUrlError(null)
            }}
          />
        </Field>
        <Field
          label={t('settings.webhooks.label')}
          htmlFor={`${baseId}-label`}
          hint={t('settings.webhooks.labelHint')}
        >
          <Input
            id={`${baseId}-label`}
            value={form.label}
            maxLength={80}
            autoComplete="off"
            onChange={(event) =>
              setForm((current) => ({ ...current, label: event.target.value }))
            }
          />
        </Field>
        <Field
          label={t('platform.webhooks.events')}
          htmlFor={`${baseId}-events`}
          hint={t('settings.webhooks.eventsHint')}
        >
          <Select
            multiple
            searchable
            id={`${baseId}-events`}
            aria-label={t('platform.webhooks.events')}
            value={form.events}
            options={eventOptions}
            placeholder={t('platform.webhooks.allEvents')}
            onChange={(events) =>
              setForm((current) => ({ ...current, events: events as WebhookEvent[] }))
            }
          />
        </Field>
        <div className="webhooks-toggle">
          <div>
            <p className="webhooks-toggle__label">{t('platform.webhooks.enabled')}</p>
            <p className="webhooks-toggle__hint">{t('settings.webhooks.enabledHint')}</p>
          </div>
          <Switch
            checked={form.enabled}
            onChange={(enabled) => setForm((current) => ({ ...current, enabled }))}
            label={t('platform.webhooks.enabled')}
          />
        </div>
        {replacing ? <p className="form-note">{t('settings.webhooks.replaceNote')}</p> : null}
      </div>
    </Modal>
  )
}

function SecretDialog({ created, onClose }: { created: WebhookCreated; onClose: () => void }) {
  const { t } = useI18n()
  const masked = `${created.secret.slice(0, 6)}${'•'.repeat(12)}${created.secret.slice(-4)}`
  return (
    <Modal
      open
      title={t('settings.webhooks.secretTitle')}
      description={t('settings.webhooks.secretDesc')}
      onClose={onClose}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t('settings.webhooks.secretDone')}
        </Button>
      }
    >
      <div className="webhooks-secret">
        <p className="webhooks-secret__warning" role="alert">
          {t('settings.webhooks.secretWarning')}
        </p>
        <div className="webhooks-secret__value">
          <code className="mono" aria-label={t('settings.webhooks.secretTitle')}>
            {masked}
          </code>
          <CopyButton
            value={created.secret}
            label={t('settings.webhooks.secretCopy')}
            toastMessage={t('settings.webhooks.secretCopied')}
            withLabel
          />
        </div>
        <p className="webhooks-secret__url mono">{created.url}</p>
      </div>
    </Modal>
  )
}

function DeliveriesDrawer({
  endpoint,
  onClose,
}: {
  endpoint: WebhookEndpoint
  onClose: () => void
}) {
  const { t } = useI18n()
  const deliveries = useAsync(
    () => api.listWebhookDeliveries(endpoint.id, { limit: DELIVERY_PAGE_LIMIT }),
    [endpoint.id],
  )
  const rows = deliveries.data ?? []
  const name = endpointName(endpoint)

  return (
    <Drawer
      open
      title={t('platform.webhooks.deliveries')}
      subtitle={
        deliveries.data
          ? t('settings.webhooks.deliveriesDesc', { name, count: rows.length })
          : undefined
      }
      onClose={onClose}
      width={780}
    >
      {deliveries.status === 'loading' && !deliveries.data ? (
        <TableSkeleton rows={4} columns={6} />
      ) : deliveries.status === 'error' && !deliveries.data ? (
        <ErrorState
          message={deliveries.error ?? t('settings.webhooks.deliveriesFailed')}
          onRetry={deliveries.reload}
        />
      ) : deliveries.data ? (
        <DataTable<Delivery>
          caption={t('settings.webhooks.deliveriesTitle')}
          rows={rows}
          rowKey={(delivery) => delivery.id}
          empty={
            <p className="webhooks-deliveries__empty">
              {t('platform.webhooks.emptyDeliveries')}
            </p>
          }
          columns={[
            {
              key: 'time',
              label: t('settings.webhooks.delivery.columnTime'),
              render: (delivery) => (
                <div className="webhooks-deliveries__when">
                  <time dateTime={delivery.at}>{formatDateTimeSeconds(delivery.at)}</time>
                  <Badge tone={deliveryTone(delivery.status)}>
                    {t(`platform.webhooks.delivery.status.${delivery.status}`)}
                  </Badge>
                </div>
              ),
            },
            {
              key: 'event',
              label: t('settings.webhooks.delivery.columnEvent'),
              render: (delivery) => <code className="mono">{delivery.event}</code>,
            },
            {
              key: 'attempt',
              label: t('settings.webhooks.delivery.columnAttempt'),
              render: (delivery) => (
                <span className="mono">
                  {t('platform.webhooks.delivery.attempt', { attempt: delivery.attempt })}
                </span>
              ),
            },
            {
              key: 'http',
              label: t('settings.webhooks.delivery.columnHttp'),
              render: (delivery) =>
                delivery.httpStatus === undefined ? (
                  <span className="muted">—</span>
                ) : (
                  <span className="mono">
                    {t('platform.webhooks.delivery.httpStatus', {
                      status: delivery.httpStatus,
                    })}
                  </span>
                ),
            },
            {
              key: 'error',
              label: t('settings.webhooks.delivery.columnError'),
              render: (delivery) => {
                const errorClass = deliveryErrorClass(delivery, t)
                return errorClass ? (
                  <Badge tone={deliveryErrorTone(delivery)} title={delivery.error}>
                    {errorClass}
                  </Badge>
                ) : (
                  <span className="muted">—</span>
                )
              },
            },
            {
              key: 'duration',
              label: t('settings.webhooks.delivery.columnDuration'),
              align: 'right',
              render: (delivery) => (
                <span className="mono">
                  {t('platform.webhooks.delivery.duration', { ms: delivery.durationMs })}
                </span>
              ),
            },
          ]}
        />
      ) : null}
    </Drawer>
  )
}

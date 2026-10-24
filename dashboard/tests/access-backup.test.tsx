import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import { ApiError } from '../src/lib/errors'
import type { BackupEnvelope, ScopedKeyCreated } from '../src/lib/types'

// jsdom's Blob predates Blob.text(); the backup panel reads the envelope it
// just downloaded, so give the test environment the browser API.
if (typeof Blob.prototype.text !== 'function') {
  Object.defineProperty(Blob.prototype, 'text', {
    configurable: true,
    value(this: Blob): Promise<string> {
      const { promise, resolve, reject } = Promise.withResolvers<string>()
      const reader = new FileReader()
      reader.onload = () => resolve(String(reader.result))
      reader.onerror = () => reject(reader.error)
      reader.readAsText(this)
      return promise
    },
  })
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

function stubDownload() {
  const createObjectURL = vi.fn((_blob: Blob) => 'blob:download')
  const revokeObjectURL = vi.fn()
  Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL })
  Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeObjectURL })
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  return { createObjectURL, revokeObjectURL, click }
}

async function openAccess(): Promise<HTMLElement> {
  window.history.pushState({}, '', '/settings')
  render(<App />)
  await screen.findByRole('heading', { name: 'Settings', level: 1 })
  await screen.findByRole('heading', { name: 'Access', level: 2 })
  const section = document.getElementById('access')
  if (!section) throw new Error('access section is not mounted')
  await within(section).findByText('Grafana read-only')
  return section
}

function accessRow(label: string): HTMLElement {
  const row = screen.getByText(label).closest('li')
  if (!row) throw new Error(`row for ${label} not found`)
  return row as HTMLElement
}

async function openLogs(): Promise<void> {
  window.history.pushState({}, '', '/logs')
  render(<App />)
  await screen.findByRole('heading', { name: 'Logs', level: 1 })
  await screen.findByText('account.login')
}

async function openBackup(): Promise<void> {
  window.history.pushState({}, '', '/backup')
  render(<App />)
  await screen.findByRole('heading', { name: 'Backup', level: 1 })
  await screen.findByRole('heading', { name: 'Create backup', level: 2 })
}

describe('scoped key settings', () => {
  it('lists keys with scopes, limits, expiry, last use and revoked state', async () => {
    const section = await openAccess()

    const grafana = accessRow('Grafana read-only')
    expect(within(grafana).getByText('ajs_sk_mock_01…')).toBeInTheDocument()
    expect(within(grafana).getByText('ada.ops · All accounts')).toBeInTheDocument()
    expect(within(grafana).getByText('metrics:read')).toBeInTheDocument()
    expect(within(grafana).getByText('health:read')).toBeInTheDocument()
    expect(within(grafana).getByText('60/min')).toBeInTheDocument()
    expect(within(grafana).getByText('2h ago')).toBeInTheDocument()
    expect(within(grafana).getByText('Active')).toBeInTheDocument()

    const catalog = accessRow('CI catalog sync')
    expect(within(catalog).getByText('120/min')).toBeInTheDocument()
    expect(within(catalog).getByText(/2 accounts/)).toBeInTheDocument()
    expect(within(catalog).getByText('Never')).toBeInTheDocument()
    expect(within(catalog).getByText('never')).toBeInTheDocument()

    const legacy = accessRow('Legacy usage feed')
    expect(within(legacy).getByText('Revoked')).toBeInTheDocument()
    expect(within(legacy).getByText('Unlimited')).toBeInTheDocument()
    expect(within(legacy).queryByRole('button', { name: 'Revoke Legacy usage feed' })).toBeNull()

    expect(within(section).getByText('3 keys')).toBeInTheDocument()
  })

  it('creates a key, shows the secret once, copies it, then revokes with a typed confirm', async () => {
    const user = userEvent.setup()
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    const create = vi.spyOn(api, 'createScopedKey')
    const revoke = vi.spyOn(api, 'revokeScopedKey')
    await openAccess()

    await user.click(screen.getByRole('button', { name: 'New scoped key' }))
    const dialog = await screen.findByRole('dialog', { name: 'New scoped key' })
    await user.type(within(dialog).getByLabelText('Label'), 'CI monitor')

    await user.click(within(dialog).getByRole('button', { name: 'Scopes' }))
    const scopeList = await screen.findByRole('listbox', { name: 'Scopes' })
    await user.click(within(scopeList).getByRole('option', { name: 'accounts:read' }))
    await user.click(within(scopeList).getByRole('option', { name: 'usage:read' }))
    await user.click(screen.getByRole('button', { name: 'Done' }))

    await user.click(within(dialog).getByRole('button', { name: 'Accounts' }))
    const accountList = await screen.findByRole('listbox', { name: 'Accounts' })
    await user.click(within(accountList).getByRole('option', { name: /Northwind Ops/ }))
    await user.click(screen.getByRole('button', { name: 'Done' }))

    const rate = within(dialog).getByLabelText('Rate limit')
    await user.clear(rate)
    await user.type(rate, '0')

    const future = new Date(Date.now() + 30 * 86_400_000)
    const local = new Date(future.getTime() - future.getTimezoneOffset() * 60_000)
      .toISOString()
      .slice(0, 16)
    fireEvent.change(within(dialog).getByLabelText('Expires at'), {
      target: { value: local },
    })

    await user.click(within(dialog).getByRole('button', { name: 'Create key' }))

    const created = (await create.mock.results[0]?.value) as ScopedKeyCreated
    expect(create).toHaveBeenCalledWith(
      expect.objectContaining({
        userId: 'u-01',
        label: 'CI monitor',
        scopes: ['accounts:read', 'usage:read'],
        accountIds: ['acc-01'],
        rateLimitPerMin: 0,
      }),
    )
    const submitted = create.mock.calls[0][0]
    expect(submitted.expiresAt).toBeDefined()
    expect(Date.parse(submitted.expiresAt ?? '')).toBeGreaterThan(Date.now())

    const secretDialog = await screen.findByRole('dialog', { name: 'Scoped key created' })
    expect(within(secretDialog).getByText(created.secret)).toBeInTheDocument()
    expect(within(secretDialog).getByRole('alert')).toHaveTextContent(
      'keeps only its SHA-256',
    )
    await user.click(within(secretDialog).getByRole('button', { name: 'Copy secret' }))
    expect(writeText).toHaveBeenCalledWith(created.secret)
    await user.click(within(secretDialog).getByRole('button', { name: 'Done' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Scoped key created' })).toBeNull(),
    )

    const row = accessRow('CI monitor')
    expect(within(row).getByText('Unlimited')).toBeInTheDocument()
    expect(within(row).getByText('accounts:read')).toBeInTheDocument()
    expect(within(row).getByText('Northwind Ops')).toBeInTheDocument()

    await user.click(within(row).getByRole('button', { name: 'Revoke CI monitor' }))
    const confirm = await screen.findByRole('dialog', { name: 'Revoke scoped key?' })
    const confirmButton = within(confirm).getByRole('button', { name: 'Revoke key' })
    expect(confirmButton).toBeDisabled()
    await user.type(within(confirm).getByLabelText('Prefix'), created.key.prefix)
    await user.click(confirmButton)

    await waitFor(() => expect(revoke).toHaveBeenCalledWith(created.key.id))
    await waitFor(() => expect(within(accessRow('CI monitor')).getByText('Revoked')).toBeInTheDocument())
    expect(await screen.findByText('Scoped key “CI monitor” revoked')).toBeInTheDocument()
  })

  it('offers events:read in the scope picker and sends it on create', async () => {
    const user = userEvent.setup()
    const create = vi.spyOn(api, 'createScopedKey')
    await openAccess()

    await user.click(screen.getByRole('button', { name: 'New scoped key' }))
    const dialog = await screen.findByRole('dialog', { name: 'New scoped key' })
    await user.type(within(dialog).getByLabelText('Label'), 'Event reader')

    await user.click(within(dialog).getByRole('button', { name: 'Scopes' }))
    const scopeList = await screen.findByRole('listbox', { name: 'Scopes' })
    // The picker mirrors the daemon's Scope enum order, events:read included.
    expect(within(scopeList).getAllByRole('option').map((option) => option.textContent)).toEqual([
      'accounts:read',
      'accounts:write',
      'catalog:read',
      'catalog:write',
      'leases:read',
      'leases:write',
      'requests:read',
      'audit:read',
      'metrics:read',
      'lab:read',
      'settings:read',
      'settings:write',
      'usage:read',
      'renewal:read',
      'health:read',
      'events:read',
      'admin',
    ])
    await user.click(within(scopeList).getByRole('option', { name: 'events:read' }))
    await user.click(screen.getByRole('button', { name: 'Done' }))

    await user.click(within(dialog).getByRole('button', { name: 'Create key' }))

    expect(create).toHaveBeenCalledWith(
      expect.objectContaining({ label: 'Event reader', scopes: ['events:read'] }),
    )

    const secretDialog = await screen.findByRole('dialog', { name: 'Scoped key created' })
    await user.click(within(secretDialog).getByRole('button', { name: 'Done' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Scoped key created' })).toBeNull(),
    )
    expect(within(accessRow('Event reader')).getByText('events:read')).toBeInTheDocument()
  })

  it('refreshes the list and explains a 409 when a revoke races the daemon', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'revokeScopedKey').mockRejectedValue(
      new ApiError(409, 'conflict', 'CONFLICT'),
    )
    await openAccess()

    await user.click(
      within(accessRow('Grafana read-only')).getByRole('button', {
        name: 'Revoke Grafana read-only',
      }),
    )
    const confirm = await screen.findByRole('dialog', { name: 'Revoke scoped key?' })
    await user.type(within(confirm).getByLabelText('Prefix'), 'ajs_sk_mock_01')
    await user.click(within(confirm).getByRole('button', { name: 'Revoke key' }))

    expect(
      await screen.findByText(/The key changed on the daemon/),
    ).toBeInTheDocument()
    expect(screen.queryByRole('dialog', { name: 'Revoke scoped key?' })).toBeNull()
  })

  it('renders the Access section in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    window.history.pushState({}, '', '/settings')
    render(<App />)

    await screen.findByRole('heading', { name: '设置', level: 1 })
    await screen.findByRole('heading', { name: '访问密钥', level: 2 })
    const section = document.getElementById('access')
    if (!section) throw new Error('access section is not mounted')
    expect(within(section).getByRole('button', { name: '新建限定密钥' })).toBeInTheDocument()
    expect(within(section).getAllByText('速率限制')).toHaveLength(3)
    expect(within(section).getByText('已撤销')).toBeInTheDocument()
    expect(within(section).getByText('不限制')).toBeInTheDocument()
  })
})

describe('audit CSV export', () => {
  it('downloads the CSV route with the active severity filter', async () => {
    const user = userEvent.setup()
    const { createObjectURL, click } = stubDownload()
    await openLogs()

    await user.click(screen.getByRole('combobox', { name: 'Filter by severity' }))
    await user.click(await screen.findByRole('option', { name: 'Warn' }))
    await user.click(screen.getByRole('button', { name: 'Export CSV' }))

    await waitFor(() => expect(click).toHaveBeenCalledTimes(1))
    const blob = createObjectURL.mock.calls[0][0] as Blob
    const csv = await blob.text()
    const lines = csv.trim().split(/\r\n/)
    expect(lines[0]).toBe('ts,severity,actor,action,target,detail,meta')
    expect(lines.length).toBeGreaterThan(1)
    for (const line of lines.slice(1)) expect(line.split(',')[1]).toBe('warn')
    expect(csv).toContain('node.drain')
    expect(csv).not.toContain('account.login')
    expect(await screen.findByText('Audit CSV downloaded')).toBeInTheDocument()
    expect(screen.queryByText(/X-Truncated/)).toBeNull()
  })

  it('shows the X-Truncated warning when the daemon clipped the export', async () => {
    const { createObjectURL } = stubDownload()
    vi.spyOn(api, 'exportAuditCsv').mockResolvedValue({
      blob: new Blob(['ts\r\n']),
      filename: 'apc-audit-test.csv',
      truncated: true,
    })
    await openLogs()

    await userEvent.setup().click(screen.getByRole('button', { name: 'Export CSV' }))

    expect(screen.getByRole('alert')).toHaveTextContent('X-Truncated')
    expect(createObjectURL).toHaveBeenCalledTimes(1)
  })
})

describe('backup console', () => {
  const passphrase = 'correct horse battery'

  it('creates an envelope, downloads it, and lists the sealed blobs', async () => {
    const user = userEvent.setup()
    const { createObjectURL } = stubDownload()
    await openBackup()

    await user.type(screen.getByLabelText('Passphrase'), passphrase)
    await user.type(screen.getByLabelText('Confirm passphrase'), passphrase)
    await user.click(screen.getByRole('button', { name: 'Create and download' }))

    await waitFor(() => expect(createObjectURL).toHaveBeenCalledTimes(1))
    const envelope = JSON.parse(
      await (createObjectURL.mock.calls[0][0] as Blob).text(),
    ) as BackupEnvelope
    expect(envelope.app).toBe('apc')
    expect(envelope.version).toBe(1)
    expect(envelope.payload.length).toBeGreaterThan(0)

    expect(await screen.findByText('state.json')).toBeInTheDocument()
    expect(screen.getByText('secrets.json')).toBeInTheDocument()
    expect(screen.getByText('device-secrets.json')).toBeInTheDocument()
    expect(screen.getAllByText('required').length).toBe(1)
    expect(screen.getAllByText('optional').length).toBe(2)
    expect(screen.getByLabelText('Passphrase')).toHaveValue('')
    expect(screen.getByLabelText('Confirm passphrase')).toHaveValue('')
    expect(await screen.findByText('Backup downloaded')).toBeInTheDocument()
  })

  it('inspects a pasted envelope, dry-runs the diff, and keeps apply disabled', async () => {
    const user = userEvent.setup()
    const created = await api.createBackup(passphrase)
    const envelopeText = await created.blob.text()
    await openBackup()

    fireEvent.change(screen.getByLabelText('Envelope'), { target: { value: envelopeText } })
    await user.type(screen.getByLabelText('Passphrase (optional)'), passphrase)
    await user.click(screen.getByRole('button', { name: 'Inspect' }))

    expect(await screen.findByText('apc')).toBeInTheDocument()
    expect(screen.getByText('Supported')).toBeInTheDocument()
    const manifest = screen.getByRole('table', { name: 'Manifest' })
    expect(within(manifest).getByText('state.json')).toBeInTheDocument()

    // Change the panel after the backup: the state blob must show as changed.
    await api.createAccount({
      label: 'Backup drift',
      username: 'demo-drift',
      password: 'placeholder-password',
      platform: 'android',
      concurrency: 10,
      regions: ['hk'],
    })

    await user.click(screen.getByRole('button', { name: 'Run dry run' }))
    expect(await screen.findByText(/Dry run complete: 0 added, 0 removed, 2 changed, 1 unchanged/)).toBeInTheDocument()
    const diff = screen.getByRole('table', {
      name: 'Dry-run diff between the envelope and the panel files',
    })
    const changedRow = within(diff).getByText('Changed').closest('tr')
    if (!changedRow) throw new Error('changed row not found')
    expect(within(changedRow).getByText(/state\.json/)).toBeInTheDocument()

    const apply = screen.getByRole('button', { name: 'Apply restore' })
    expect(apply).toBeDisabled()
    expect(apply).toHaveAccessibleDescription(/AJS_BACKUP_ALLOW_RESTORE=1/)
    expect(screen.queryByText(/no files were changed/)).toBeNull()
  })

  it('rejects a wrong passphrase on inspect', async () => {
    const user = userEvent.setup()
    const created = await api.createBackup(passphrase)
    await openBackup()

    fireEvent.change(screen.getByLabelText('Envelope'), {
      target: { value: await created.blob.text() },
    })
    await user.type(screen.getByLabelText('Passphrase (optional)'), 'wrong horse battery')
    await user.click(screen.getByRole('button', { name: 'Inspect' }))

    expect(await screen.findByText(/wrong passphrase or corrupted envelope/)).toBeInTheDocument()
    expect(screen.queryByText('Supported')).toBeNull()
  })

  it('renders the backup console in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    window.history.pushState({}, '', '/backup')
    render(<App />)

    await screen.findByRole('heading', { name: '备份', level: 1 })
    expect(await screen.findByRole('heading', { name: '创建备份', level: 2 })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '创建并下载' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '检查信封', level: 2 })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '恢复试运行', level: 2 })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '应用恢复' })).toBeDisabled()
    expect(screen.getByText(/AJS_BACKUP_ALLOW_RESTORE=1/)).toBeInTheDocument()
  })
})

describe('live audit export', () => {
  it('requests the CSV route with the bearer header and reads X-Truncated', async () => {
    vi.resetModules()
    vi.stubEnv('VITE_USE_MOCK', 'false')
    const calls: Array<{ url: string; init: RequestInit }> = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push({ url: String(input), init: init ?? {} })
        return new Response('ts\r\n', {
          status: 200,
          headers: { 'Content-Type': 'text/csv', 'X-Truncated': 'true' },
        })
      }),
    )

    const live = await import('../src/lib/api') // Dynamic on purpose: VITE_USE_MOCK is baked in at module load, so this exercises the live-mode boundary.
    live.setPanelToken('panel-secret')
    const result = await live.api.exportAuditCsv({ severity: 'warn' })

    expect(calls[0].url).toContain('/v1/audit/export.csv?severity=warn')
    expect(new Headers(calls[0].init.headers).get('Authorization')).toBe(
      'Bearer panel-secret',
    )
    expect(new Headers(calls[0].init.headers).get('Accept')).toBe('text/csv')
    expect(result.truncated).toBe(true)
    live.setPanelToken(null)
    vi.unstubAllEnvs()
    vi.unstubAllGlobals()
  })
})

import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { UserEvent } from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import type { AccountExportDocument } from '../src/lib/types'

function exportDocument(
  rows: Array<Record<string, unknown> & { label: string; username: string }>,
): string {
  const doc: AccountExportDocument = {
    format: 'ajs-accounts',
    version: 1,
    exportedAt: '2026-10-24T09:00:00Z',
    accounts: rows.map((row) => ({
      password: 'fake-pass-123',
      platform: 'android',
      concurrency: 1,
      regions: [],
      state: 'active',
      ...row,
    })) as AccountExportDocument['accounts'],
  }
  return JSON.stringify(doc)
}

async function openAccounts(): Promise<void> {
  render(<App />)
  await screen.findByRole('heading', { name: 'Accounts', level: 1 })
  await screen.findByRole('table', { name: 'Vendor accounts' })
}

async function openExport(user: UserEvent): Promise<HTMLElement> {
  await user.click(screen.getByRole('button', { name: 'Export' }))
  return screen.findByRole('dialog', { name: 'Export accounts' })
}

async function openImport(user: UserEvent): Promise<HTMLElement> {
  await user.click(screen.getByRole('button', { name: 'Import' }))
  return screen.findByRole('dialog', { name: 'Import accounts' })
}

function stubDownload() {
  const createObjectURL = vi.fn((_blob: Blob) => 'blob:account-export')
  const revokeObjectURL = vi.fn()
  Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL })
  Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeObjectURL })
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  return { createObjectURL, revokeObjectURL, click }
}

function storedValues(): string {
  const values: string[] = []
  for (let index = 0; index < localStorage.length; index += 1) {
    const key = localStorage.key(index)
    if (key) values.push(localStorage.getItem(key) ?? '')
  }
  return values.join('\n')
}

/** jsdom's Blob has no `.text()` in this environment. */
function blobText(blob: Blob): Promise<string> {
  const { promise, resolve, reject } = Promise.withResolvers<string>()
  const reader = new FileReader()
  reader.onload = () => resolve(String(reader.result ?? ''))
  reader.onerror = () => reject(reader.error)
  reader.readAsText(blob)
  return promise
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/accounts')
  await api.resetMock()
})

describe('account export dialog', () => {
  it('defaults to all accounts, disables encrypted export in mock and gates plaintext', async () => {
    const user = userEvent.setup()
    const { createObjectURL, click } = stubDownload()
    await openAccounts()

    const dialog = await openExport(user)
    const allScope = within(dialog).getByRole('radio', { name: 'All (6)' })
    expect(allScope).toBeChecked()
    expect(within(dialog).getByRole('radio', { name: 'Selected (0)' })).toBeDisabled()

    const encrypted = within(dialog).getByRole('radio', { name: 'Encrypted .ajsx' })
    await waitFor(() => expect(encrypted).toBeDisabled())
    expect(within(dialog).getByText('Encrypted export requires the live daemon')).toBeInTheDocument()
    expect(within(dialog).getByRole('radio', { name: 'JSON' })).toBeChecked()

    const warning = within(dialog).getByText(/plaintext vendor passwords/i)
    expect(warning).toBeInTheDocument()

    await user.type(
      within(dialog).getByLabelText('Type EXPORT to confirm'),
      'EXPORT',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Export' }))

    await waitFor(() => expect(click).toHaveBeenCalledTimes(1))
    expect(createObjectURL).toHaveBeenCalledTimes(1)
    const blob = createObjectURL.mock.calls[0][0] as Blob
    const parsed = JSON.parse(await blobText(blob)) as AccountExportDocument
    expect(parsed.format).toBe('ajs-accounts')
    expect(parsed.version).toBe(1)
    expect(parsed.accounts).toHaveLength(6)
    for (const row of parsed.accounts) {
      expect(row.password).toBeNull()
      expect(row.warnings).toContain('password_unavailable')
    }
    expect(await screen.findByText('Exported 6 accounts')).toBeInTheDocument()
    expect(screen.queryByRole('dialog', { name: 'Export accounts' })).not.toBeInTheDocument()
    expect(storedValues()).not.toContain('fake-pass-123')
  })

  it('preselects the bulk selection and exports only those ids', async () => {
    const user = userEvent.setup()
    stubDownload()
    await openAccounts()

    await user.click(screen.getByRole('checkbox', { name: 'Select Northwind Ops' }))
    await user.click(screen.getByRole('checkbox', { name: 'Select Field Lab' }))
    await user.click(screen.getByRole('button', { name: 'Export selected' }))

    const dialog = await screen.findByRole('dialog', { name: 'Export accounts' })
    expect(within(dialog).getByRole('radio', { name: 'Selected (2)' })).toBeChecked()
    expect(within(dialog).getByText('2 accounts selected for export')).toBeInTheDocument()

    const exportAccounts = vi.spyOn(api, 'exportAccounts')
    await user.type(within(dialog).getByLabelText('Type EXPORT to confirm'), 'EXPORT')
    await user.click(within(dialog).getByRole('button', { name: 'Export' }))
    await waitFor(() => expect(exportAccounts).toHaveBeenCalledTimes(1))
    expect(exportAccounts.mock.calls[0][0].ids).toEqual(['acc-01', 'acc-02'])
  })

  it('validates the passphrase, downloads the encrypted blob and never stores it', async () => {
    const user = userEvent.setup()
    const { click } = stubDownload()
    vi.spyOn(api, 'accountIOCapabilities').mockResolvedValue({
      encryptedExport: true,
      plaintextExport: true,
      maxImportRows: 1000,
      maxExportAccounts: 1000,
      maxDeleteIDs: 100,
      maxBodyBytes: 1_048_576,
      maxImportBodyBytes: 4_194_304,
    })
    const exportAccounts = vi.spyOn(api, 'exportAccounts').mockResolvedValue({
      blob: new Blob(['encrypted']),
      filename: 'ajs-accounts-20261024-120000.ajsx',
      counts: { accounts: 6, missingSecrets: 0, devices: 6 },
    })
    const passphrase = 'correct horse battery'
    await openAccounts()

    const dialog = await openExport(user)
    const encrypted = within(dialog).getByRole('radio', { name: 'Encrypted .ajsx' })
    await waitFor(() => expect(encrypted).toBeEnabled())
    await user.click(encrypted)

    await user.click(within(dialog).getByRole('button', { name: 'Export' }))
    expect(
      within(dialog).getByText('Passphrase must be at least 12 characters'),
    ).toBeInTheDocument()

    await user.type(within(dialog).getByLabelText('Passphrase'), passphrase)
    await user.type(within(dialog).getByLabelText('Confirm passphrase'), 'different passphrase')
    await user.click(within(dialog).getByRole('button', { name: 'Export' }))
    expect(within(dialog).getByText('Passphrases do not match')).toBeInTheDocument()

    await user.clear(within(dialog).getByLabelText('Confirm passphrase'))
    await user.type(within(dialog).getByLabelText('Confirm passphrase'), passphrase)
    await user.click(within(dialog).getByRole('button', { name: 'Export' }))

    await waitFor(() => expect(exportAccounts).toHaveBeenCalledTimes(1))
    expect(exportAccounts.mock.calls[0][0]).toMatchObject({
      format: 'encrypted',
      passphrase,
      includeDevice: true,
    })
    await waitFor(() => expect(click).toHaveBeenCalled())
    expect(await screen.findByText('Exported 6 accounts')).toBeInTheDocument()
    expect(screen.queryByRole('dialog', { name: 'Export accounts' })).not.toBeInTheDocument()
    expect(storedValues()).not.toContain(passphrase)
  })
})

describe('account import dialog', () => {
  it('previews a JSON file, applies creates and never echoes or stores the password', async () => {
    const user = userEvent.setup()
    await openAccounts()
    const dialog = await openImport(user)

    const previewButton = within(dialog).getByRole('button', { name: 'Preview' })
    expect(previewButton).toBeDisabled()

    const content = exportDocument([
      { label: 'Imported Alpha', username: 'alpha-user' },
      { label: 'Imported Beta', username: 'beta-user', platform: 'ios', regions: ['hk'] },
    ])
    await user.upload(
      within(dialog).getByLabelText('File'),
      new File([content], 'accounts.json', { type: 'application/json' }),
    )
    await waitFor(() => expect(previewButton).toBeEnabled())
    await user.click(previewButton)

    expect(await within(dialog).findByText(/2 rows · 2 create/)).toBeInTheDocument()
    const table = within(dialog).getByRole('table', { name: 'Import preview' })
    expect(within(table).getByText('Imported Alpha')).toBeInTheDocument()
    expect(within(table).getByText('Imported Beta')).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Import 2 accounts' }))
    expect(await within(dialog).findByText(/2 created · 0 updated/)).toBeInTheDocument()
    expect(within(dialog).getByRole('table', { name: 'Import result' })).toBeInTheDocument()

    const accounts = await api.listAccounts()
    expect(accounts).toHaveLength(8)
    expect(accounts.find((account) => account.label === 'Imported Alpha')).toMatchObject({
      state: 'paused',
      username: 'alpha-user',
    })
    expect(JSON.stringify(accounts)).not.toContain('fake-pass-123')
    expect(document.body.textContent).not.toContain('fake-pass-123')
  })

  it('disables atomic apply on a failed row, then applies valid rows in partial mode', async () => {
    const user = userEvent.setup()
    await openAccounts()
    const dialog = await openImport(user)

    const content = exportDocument([
      { label: 'Atomic One', username: 'atomic-one' },
      { label: 'Atomic One', username: 'atomic-two' },
    ])
    await user.upload(
      within(dialog).getByLabelText('File'),
      new File([content], 'duplicate.json', { type: 'application/json' }),
    )
    await user.click(within(dialog).getByRole('button', { name: 'Preview' }))

    expect(await within(dialog).findByText(/1 fail/)).toBeInTheDocument()
    const apply = within(dialog).getByRole('button', { name: 'Import 1 accounts' })
    expect(apply).toBeDisabled()
    expect(
      within(dialog).getByText('Atomic mode: no accounts will change while a row fails.'),
    ).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Back' }))
    await user.click(within(dialog).getByText('Advanced options'))
    await user.click(within(dialog).getByRole('combobox', { name: 'Mode' }))
    await user.click(await screen.findByRole('option', { name: 'Partial' }))

    // Changing an option invalidates the preview: we are back on the source step.
    const previewAgain = within(dialog).getByRole('button', { name: 'Preview' })
    await user.click(previewAgain)
    const partialApply = await within(dialog).findByRole('button', { name: 'Import 1 accounts' })
    expect(partialApply).toBeEnabled()
    await user.click(partialApply)

    const confirm = await screen.findByRole('dialog', { name: 'Apply valid rows?' })
    await user.click(within(confirm).getByRole('button', { name: 'Apply valid rows' }))
    expect(await within(dialog).findByText(/1 created · 0 updated · 0 skipped · 1 failed/)).toBeInTheDocument()
    expect(within(dialog).getByText('Some rows failed; they are listed above.')).toBeInTheDocument()
  })

  it('rejects oversized, encrypted and malformed files with visible errors', async () => {
    const user = userEvent.setup()
    await openAccounts()
    const dialog = await openImport(user)
    const fileInput = within(dialog).getByLabelText('File')

    const oversized = new File([new Uint8Array(4 * 1024 * 1024 + 1)], 'big.json')
    await user.upload(fileInput, oversized)
    expect(await within(dialog).findByText('File is larger than 4 MiB')).toBeInTheDocument()

    await user.upload(fileInput, new File(['x'], 'backup.ajsx'))
    expect(
      await within(dialog).findByText('Encrypted import requires the live daemon'),
    ).toBeInTheDocument()

    await user.upload(fileInput, new File(['{not json'], 'broken.json'))
    await user.click(within(dialog).getByRole('button', { name: 'Preview' }))
    expect(await within(dialog).findByText('Could not read this file as JSON')).toBeInTheDocument()
  })
})

describe('account bulk delete', () => {
  it('uses the bulk endpoint once for the selected accounts', async () => {
    const user = userEvent.setup()
    const bulkDelete = vi.spyOn(api, 'bulkDeleteAccounts')
    await openAccounts()

    await user.click(screen.getByRole('checkbox', { name: 'Select Northwind Ops' }))
    await user.click(screen.getByRole('checkbox', { name: 'Select Field Lab' }))
    await user.click(screen.getByRole('button', { name: 'Delete' }))

    const dialog = await screen.findByRole('dialog', { name: 'Delete 2 accounts?' })
    await user.click(within(dialog).getByRole('button', { name: 'Delete 2 accounts' }))

    await waitFor(() => expect(bulkDelete).toHaveBeenCalledTimes(1))
    expect(bulkDelete.mock.calls[0][0]).toEqual({ ids: ['acc-01', 'acc-02'] })
    expect(await screen.findByText('2 accounts deleted')).toBeInTheDocument()
    const accounts = await api.listAccounts()
    expect(accounts.some((account) => account.id === 'acc-01')).toBe(false)
    expect(accounts.some((account) => account.id === 'acc-02')).toBe(false)
  })

  it('requires the typed DELETE confirmation above ten accounts', async () => {
    for (let index = 0; index < 5; index += 1) {
      await api.createAccount({
        label: `Extra ${index}`,
        username: `extra-${index}`,
        password: 'fake-pass-123',
        platform: 'android',
        concurrency: 1,
        regions: [],
      })
    }
    const user = userEvent.setup()
    await openAccounts()

    await user.click(screen.getByRole('checkbox', { name: 'Select all visible accounts' }))
    expect(screen.getByText('11 selected')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Delete' }))

    const dialog = await screen.findByRole('dialog', { name: 'Delete 11 accounts?' })
    const confirm = within(dialog).getByRole('button', { name: 'Delete 11 accounts' })
    expect(confirm).toBeDisabled()

    const typed = within(dialog).getByLabelText('Confirmation')
    await user.type(typed, 'delete')
    expect(confirm).toBeDisabled()
    await user.clear(typed)
    await user.type(typed, 'DELETE')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    expect(await screen.findByText('11 accounts deleted')).toBeInTheDocument()
    expect(await api.listAccounts()).toHaveLength(0)
  })

  it('keeps not-found ids selected and lists them in the result panel', async () => {
    const user = userEvent.setup()
    vi.spyOn(api, 'bulkDeleteAccounts').mockResolvedValueOnce({
      format: 'ajs-accounts-bulk-delete-result',
      version: 1,
      dryRun: false,
      mode: 'continue',
      counts: { total: 2, deleted: 1, wouldDelete: 0, notFound: 1, failed: 0, skipped: 0 },
      results: [
        {
          id: 'acc-01',
          label: 'Northwind Ops',
          status: 'deleted',
          nodes: 3,
          leases: 2,
          code: null,
          message: null,
        },
        {
          id: 'acc-02',
          label: 'Field Lab',
          status: 'not_found',
          nodes: 0,
          leases: 0,
          code: 'NOT_FOUND',
          message: 'account not found',
        },
      ],
    })
    await openAccounts()

    await user.click(screen.getByRole('checkbox', { name: 'Select Northwind Ops' }))
    await user.click(screen.getByRole('checkbox', { name: 'Select Field Lab' }))
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('dialog', { name: 'Delete 2 accounts?' })
    await user.click(within(dialog).getByRole('button', { name: 'Delete 2 accounts' }))

    expect(await screen.findByText('Some accounts were not deleted')).toBeInTheDocument()
    expect(screen.getByText('1 deleted · 1 not found · 0 failed · 0 skipped')).toBeInTheDocument()
    const panel = screen.getByText('Delete result').closest('.callout')
    expect(panel?.textContent).toContain('acc-02 Field Lab — not found: account not found')
    expect(screen.getByText('1 selected')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Select Field Lab' })).toBeChecked()
  })
})

describe('mock account IO contract', () => {
  it('exports JSON and CSV without passwords and refuses encrypted files', async () => {
    const json = await api.exportAccounts({ format: 'json' })
    const parsed = JSON.parse(await blobText(json.blob)) as AccountExportDocument
    expect(parsed.counts).toEqual({ accounts: 6, passwords: 0, devices: 0 })
    expect(parsed.accounts).toHaveLength(6)
    expect(parsed.accounts.every((row) => row.password === null)).toBe(true)
    expect(json.filename).toMatch(/^ajs-accounts-\d{8}-\d{6}\.json$/)
    expect(json.counts).toEqual({ accounts: 6, passwords: 0, devices: 0, missingSecrets: 6 })

    const csv = await api.exportAccounts({ format: 'csv' })
    const lines = (await blobText(csv.blob)).trim().split('\n')
    expect(lines[0]).toBe('label,username,password,platform,concurrency,regions,state,expiresAt')
    expect(lines).toHaveLength(7)
    expect(lines[1]).toContain('Northwind Ops,demo-northwind,,android')

    await expect(
      api.exportAccounts({ format: 'encrypted', passphrase: 'fake-passphrase-123' }),
    ).rejects.toThrow('Encrypted export requires the live daemon')
  })

  it('plans a dry run without mutating, then applies creates and updates', async () => {
    const content = exportDocument([
      { label: 'Imported Gamma', username: 'gamma-user' },
      { label: 'Northwind Ops', username: 'renamed-user' },
    ])
    const dryRun = await api.importAccounts({
      source: { kind: 'json', content: JSON.parse(content) },
      options: { dryRun: true },
    })
    expect(dryRun).toMatchObject({ dryRun: true, applied: false })
    expect(dryRun.counts).toMatchObject({ total: 2, created: 1, skipped: 1, failed: 0 })
    expect(await api.listAccounts()).toHaveLength(6)

    const applied = await api.importAccounts({
      source: { kind: 'json', content: JSON.parse(content) },
      options: { dryRun: false, onConflict: 'update', initialState: 'from_file' },
    })
    expect(applied.applied).toBe(true)
    expect(applied.counts).toMatchObject({ created: 1, updated: 1, failed: 0 })
    const accounts = await api.listAccounts()
    expect(accounts).toHaveLength(7)
    expect(accounts.find((account) => account.label === 'Imported Gamma')?.state).toBe('active')
    expect(accounts.find((account) => account.id === 'acc-01')?.username).toBe('renamed-user')
    expect(JSON.stringify(accounts)).not.toContain('fake-pass-123')
  })

  it('parses CSV import rows without retaining the password', async () => {
    const csv =
      'label,username,password,platform,concurrency,regions,state,expiresAt\n' +
      'CSV Account,csv-user,fake-pass-123,linux,2,hk|jp-tyo,paused,\n'
    const preview = await api.importAccounts({
      source: { kind: 'csv', content: csv },
      options: { dryRun: true },
    })
    expect(preview.counts).toMatchObject({ total: 1, created: 1, failed: 0 })
    expect(preview.results[0]).toMatchObject({ action: 'create', id: null })

    const applied = await api.importAccounts({
      source: { kind: 'csv', content: csv },
      options: { dryRun: false },
    })
    expect(applied.applied).toBe(true)
    const account = (await api.listAccounts()).find((row) => row.label === 'CSV Account')
    expect(account).toMatchObject({
      username: 'csv-user',
      platform: 'linux',
      concurrency: 2,
      regions: ['hk', 'jp-tyo'],
      state: 'paused',
    })
    expect(JSON.stringify(account)).not.toContain('fake-pass-123')
  })

  it('preserves an explicit id, rejects duplicate rows atomically and deletes sequentially', async () => {
    const preserve = exportDocument([
      { id: 'acc-99', label: 'Restored Account', username: 'restored-user' },
    ])
    const restored = await api.importAccounts({
      source: { kind: 'json', content: JSON.parse(preserve) },
      options: { idPolicy: 'preserve' },
    })
    expect(restored.results[0]).toMatchObject({ action: 'create', id: 'acc-99' })
    expect((await api.listAccounts()).some((account) => account.id === 'acc-99')).toBe(true)

    const duplicate = exportDocument([
      { label: 'Duplicate Row', username: 'dup-one' },
      { label: 'Duplicate Row', username: 'dup-two' },
    ])
    const before = (await api.listAccounts()).length
    const atomic = await api.importAccounts({
      source: { kind: 'json', content: JSON.parse(duplicate) },
      options: { mode: 'atomic' },
    })
    expect(atomic.applied).toBe(false)
    expect(atomic.counts.failed).toBe(1)
    expect(await api.listAccounts()).toHaveLength(before)

    const deleted = await api.bulkDeleteAccounts({ ids: ['acc-01', 'acc-01', 'missing'] })
    expect(deleted.counts).toEqual({
      total: 2,
      deleted: 1,
      wouldDelete: 0,
      notFound: 1,
      failed: 0,
      skipped: 0,
    })
    expect((await api.listNodes()).some((node) => node.accountId === 'acc-01')).toBe(false)
  })
})

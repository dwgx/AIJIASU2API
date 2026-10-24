import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { UserEvent } from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import {
  mockOpenDeviceEnvelope,
  mockSealDevicePayload,
} from '../src/lib/mock'

const PASSPHRASE = 'correct horse battery'

beforeEach(async () => {
  localStorage.clear()
  sessionStorage.clear()
  window.history.pushState({}, '', '/device-pool')
  await api.resetMock()
})

async function openDevicePool(): Promise<HTMLElement> {
  render(<App />)
  await screen.findByRole('heading', { name: 'Device pool', level: 1 })
  return screen.findByRole('table', { name: 'Device pool records' })
}

function rowFor(table: HTMLElement, label: string): HTMLElement {
  const cell = within(table).getByText(label)
  const row = cell.closest('tr')
  if (!row) throw new Error(`row for ${label} not found`)
  return row
}

async function assignDevice(
  user: UserEvent,
  table: HTMLElement,
  label: string,
  account: string,
) {
  await user.click(within(rowFor(table, label)).getByRole('button', { name: /^Assign device/ }))
  const dialog = await screen.findByRole('dialog', { name: 'Assign device' })
  await user.click(within(dialog).getByRole('button', { name: 'Account' }))
  await user.click(await screen.findByRole('option', { name: new RegExp(account) }))
  await user.click(within(dialog).getByRole('button', { name: 'Assign' }))
  await screen.findByText(`Device assigned to ${account}`)
  await waitFor(() =>
    expect(within(rowFor(table, label)).getByText('Assigned')).toBeInTheDocument(),
  )
}

describe('device pool page', () => {
  it('lists hash-only records with counts and never renders raw identity material', async () => {
    const table = await openDevicePool()

    // Header row plus the seven seeded records.
    expect(within(table).getAllByRole('row')).toHaveLength(8)
    expect(within(table).getByText('pool-linux-01')).toBeInTheDocument()
    expect(within(table).getAllByText(/sha256:[0-9a-f]{8}…/).length).toBeGreaterThan(0)

    expect(screen.getByRole('button', { name: /^7 Total$/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(screen.getByRole('button', { name: /^3 Available$/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^2 Assigned$/ })).toBeInTheDocument()

    const body = document.body.textContent ?? ''
    expect(body).not.toContain('machineCode')
    expect(body).not.toContain('mc_')
    expect(body).not.toContain('osdid:')
  })

  it('filters the table by state from the count chips', async () => {
    const user = userEvent.setup()
    const table = await openDevicePool()

    await user.click(screen.getByRole('button', { name: /^3 Available$/ }))
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(4))
    expect(screen.getByRole('button', { name: /^3 Available$/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(within(table).queryByText('northwind-android')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /^7 Total$/ }))
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(8))
  })

  it('generates a batch and reports the result summary', async () => {
    const user = userEvent.setup()
    const table = await openDevicePool()

    await user.click(screen.getByRole('button', { name: 'Generate' }))
    const dialog = await screen.findByRole('dialog', { name: 'Generate devices' })
    const count = within(dialog).getByLabelText('Count')
    await user.clear(count)
    await user.type(count, '2')
    await user.click(within(dialog).getByRole('button', { name: 'Generate' }))

    expect(await screen.findByText('Generated 2 devices')).toBeInTheDocument()
    expect(
      within(dialog).getByText('2 records created and available for assignment.'),
    ).toBeInTheDocument()
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(10))
  })

  it('assigns an available device to an account', async () => {
    const user = userEvent.setup()
    const table = await openDevicePool()

    await assignDevice(user, table, 'pool-linux-01', 'QA Sandbox')
    const row = rowFor(table, 'pool-linux-01')
    expect(within(row).getByText('QA Sandbox')).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: /^Release device/ })).toBeInTheDocument()
  })

  it('releases only after the exact RELEASE token and returns an unpresented identity to available', async () => {
    const user = userEvent.setup()
    const table = await openDevicePool()
    await assignDevice(user, table, 'pool-linux-02', 'QA Sandbox')

    await user.click(
      within(rowFor(table, 'pool-linux-02')).getByRole('button', { name: /^Release device/ }),
    )
    const dialog = await screen.findByRole('dialog', { name: 'Release device' })
    const confirm = within(dialog).getByRole('button', { name: 'Release' })
    expect(confirm).toBeDisabled()

    const token = within(dialog).getByLabelText('Confirm token')
    await user.type(token, 'release')
    expect(confirm).toBeDisabled()
    await user.clear(token)
    await user.type(token, 'RELEASE')
    expect(confirm).toBeEnabled()

    await user.click(confirm)
    expect(await screen.findByText('Device released')).toBeInTheDocument()
    await waitFor(() =>
      expect(within(rowFor(table, 'pool-linux-02')).getByText('Available')).toBeInTheDocument(),
    )
  })

  it('retires with RETIRE and surfaces the daemon 409 reason for a live session', async () => {
    const user = userEvent.setup()
    const table = await openDevicePool()

    await user.click(
      within(rowFor(table, 'pool-android-01')).getByRole('button', { name: /^Retire device/ }),
    )
    const dialog = await screen.findByRole('dialog', { name: 'Retire device' })
    await user.type(within(dialog).getByLabelText('Reason (optional)'), 'vendor challenge')
    await user.type(within(dialog).getByLabelText('Confirm token'), 'RETIRE')
    await user.click(within(dialog).getByRole('button', { name: 'Retire' }))

    expect(await screen.findByText('Device retired')).toBeInTheDocument()
    await waitFor(() =>
      expect(within(rowFor(table, 'pool-android-01')).getByText('Retired')).toBeInTheDocument(),
    )

    // acc-01 has LIVE nodes, so the store refuses the release with a reason.
    await user.click(
      within(rowFor(table, 'northwind-android')).getByRole('button', { name: /^Release device/ }),
    )
    const blocked = await screen.findByRole('dialog', { name: 'Release device' })
    await user.type(within(blocked).getByLabelText('Confirm token'), 'RELEASE')
    await user.click(within(blocked).getByRole('button', { name: 'Release' }))
    expect(await within(blocked).findByRole('alert')).toHaveTextContent(
      'The account has a live node; log out first.',
    )
  })

  it('quarantines and purges with typed confirms', async () => {
    const user = userEvent.setup()
    const table = await openDevicePool()

    await user.click(
      within(rowFor(table, 'pool-linux-01')).getByRole('button', { name: /^Quarantine device/ }),
    )
    const quarantine = await screen.findByRole('dialog', { name: 'Quarantine device' })
    await user.type(within(quarantine).getByLabelText('Confirm token'), 'QUARANTINE')
    await user.click(within(quarantine).getByRole('button', { name: 'Quarantine' }))
    expect(await screen.findByText('Device quarantined')).toBeInTheDocument()
    await waitFor(() =>
      expect(within(rowFor(table, 'pool-linux-01')).getByText('Quarantined')).toBeInTheDocument(),
    )

    await user.click(
      within(rowFor(table, 'pool-linux-01')).getByRole('button', { name: /^Purge device/ }),
    )
    const purge = await screen.findByRole('dialog', { name: 'Purge device' })
    const confirm = within(purge).getByRole('button', { name: 'Purge' })
    expect(confirm).toBeDisabled()
    await user.type(within(purge).getByLabelText('Confirm token'), 'PURGE')
    await user.click(confirm)
    expect(await screen.findByText('Device purged')).toBeInTheDocument()
    await waitFor(() =>
      expect(within(table).queryByText('pool-linux-01')).not.toBeInTheDocument(),
    )
  })

  it('exports a passphrase envelope only after the EXPORT token and stores no secret', async () => {
    const user = userEvent.setup()
    const exportSpy = vi.spyOn(api, 'exportDevicePool')
    await openDevicePool()

    await user.click(screen.getByRole('button', { name: 'Export' }))
    const dialog = await screen.findByRole('dialog', { name: 'Export devices' })
    await user.type(within(dialog).getByLabelText('Passphrase'), PASSPHRASE)
    await user.type(within(dialog).getByLabelText('Confirm passphrase'), PASSPHRASE)
    await user.type(within(dialog).getByLabelText('Confirm token'), 'EXPORT')
    await user.click(within(dialog).getByRole('button', { name: 'Export and download' }))

    expect(await screen.findByText('Exported 2 devices')).toBeInTheDocument()
    expect(exportSpy).toHaveBeenCalledWith(
      expect.objectContaining({ passphrase: PASSPHRASE, scope: 'assigned', confirm: 'EXPORT' }),
    )

    const stored: string[] = []
    for (let index = 0; index < localStorage.length; index += 1) {
      const key = localStorage.key(index)
      if (key) stored.push(key, localStorage.getItem(key) ?? '')
    }
    for (let index = 0; index < sessionStorage.length; index += 1) {
      const key = sessionStorage.key(index)
      if (key) stored.push(key, sessionStorage.getItem(key) ?? '')
    }
    expect(stored.join('\n')).not.toContain(PASSPHRASE)
  })

  it('imports with a dry run by default and shows conflict details', async () => {
    const user = userEvent.setup()
    await openDevicePool()
    const envelope = await api.exportDevicePool({ passphrase: PASSPHRASE, scope: 'all' })

    await user.click(screen.getByRole('button', { name: 'Import' }))
    const dialog = await screen.findByRole('dialog', { name: 'Import devices' })
    const textarea = within(dialog).getByLabelText('Envelope')
    fireEvent.change(textarea, { target: { value: JSON.stringify(envelope) } })
    await user.type(within(dialog).getByLabelText('Passphrase'), PASSPHRASE)
    expect(within(dialog).getByRole('checkbox', { name: 'Dry run (no writes)' })).toBeChecked()

    await user.click(within(dialog).getByRole('button', { name: 'Dry run' }))
    expect(
      await within(dialog).findByText('Dry run finished: 0 to add, 6 skipped.'),
    ).toBeInTheDocument()
    expect(within(dialog).getByText(/Added 0 · skipped 6/)).toBeInTheDocument()

    // A forged payload reuses a record id with a different machine code.
    const payload = mockOpenDeviceEnvelope(envelope, PASSPHRASE)
    payload.devices[0] = {
      ...payload.devices[0],
      machineCode: `${payload.devices[0].machineCode}-forged`,
    }
    const forged = mockSealDevicePayload(payload, PASSPHRASE)
    fireEvent.change(textarea, { target: { value: JSON.stringify(forged) } })
    await user.click(within(dialog).getByRole('button', { name: 'Dry run' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'The envelope conflicts with the current pool.',
    )
    expect(
      within(dialog).getByRole('heading', { name: 'Import conflicts (1)' }),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText('The same id has a different identity.'),
    ).toBeInTheDocument()
  })

  it('renders an honest disabled state when the pool is off', async () => {
    vi.spyOn(api, 'listDevicePool').mockResolvedValue({
      enabled: false,
      reason: 'DEVICE_POOL_DISABLED',
      total: 0,
      counts: { available: 0, assigned: 0, retired: 0, quarantined: 0 },
      devices: [],
      warnings: [],
    })

    render(<App />)
    await screen.findByRole('heading', { name: 'Device pool', level: 1 })

    expect((await screen.findAllByText('Device pool is off')).length).toBeGreaterThan(0)
    expect(
      screen.getByText('Daemon reason: DEVICE_POOL_DISABLED'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('table', { name: 'Device pool records' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Generate' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Export' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Import' })).toBeDisabled()
  })

  it('switches the page to Chinese', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Device pool', level: 1 })

    await user.click(screen.getByRole('button', { name: '中' }))
    expect(await screen.findByRole('heading', { name: '设备池', level: 1 })).toBeInTheDocument()
    const table = screen.getByRole('table', { name: '设备池记录' })
    expect(within(table).getByText('设备')).toBeInTheDocument()
    expect(within(table).getAllByText('可用').length).toBeGreaterThan(0)

    await user.click(screen.getByRole('button', { name: '生成' }))
    expect(await screen.findByRole('dialog', { name: '生成设备' })).toBeInTheDocument()
  })

  it('opens a record drawer and saves label and notes', async () => {
    const user = userEvent.setup()
    const table = await openDevicePool()

    await user.click(within(table).getByText('pool-linux-01'))
    const drawer = await screen.findByRole('dialog', { name: 'pool-linux-01' })
    expect(await within(drawer).findByRole('heading', { name: 'Hashes' })).toBeInTheDocument()
    expect(within(drawer).getByText('site')).toBeInTheDocument()
    expect(within(drawer).getByText('machine')).toBeInTheDocument()

    const label = within(drawer).getByLabelText('Label')
    await user.clear(label)
    await user.type(label, 'renamed-device')
    await user.click(within(drawer).getByRole('button', { name: 'Save' }))

    expect(await screen.findByText('Device metadata saved')).toBeInTheDocument()
    await waitFor(() =>
      expect(within(table).getByText('renamed-device')).toBeInTheDocument(),
    )
  })
})

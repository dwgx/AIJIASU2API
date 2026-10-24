import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api, ScheduleActionError } from '../src/lib/api'

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/schedules')
  await api.resetMock()
})

async function renderSchedules() {
  render(<App />)
  await screen.findByRole('heading', { name: 'Schedules', level: 1 })
  const table = await screen.findByRole('table')
  await waitFor(() => {
    expect(within(table).getByText('Nightly rotate')).toBeInTheDocument()
  })
}

/** The table row for one schedule; the preview picker also shows the name. */
function scheduleRow(name: string): HTMLElement {
  const table = screen.getByRole('table')
  const cell = within(table).getByText(name)
  const row = cell.closest('tr')
  if (!row) throw new Error(`row for ${name} not found`)
  return row as HTMLElement
}

describe('schedules page', () => {
  it('lists schedules, the read-only renewal sweep and a five-run preview', async () => {
    await renderSchedules()

    expect(screen.getByText('Reconnect watchdog')).toBeInTheDocument()
    expect(screen.getByText('Weekend rotate')).toBeInTheDocument()
    expect(screen.getByText('One-shot rotate')).toBeInTheDocument()
    expect(screen.getByText('Cron · 0 3 * * *')).toBeInTheDocument()
    expect(screen.getByText('OK')).toBeInTheDocument()
    expect(screen.getAllByText('Dry run').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Error').length).toBeGreaterThan(0)

    expect(screen.getByRole('heading', { name: 'Renewal sweep' })).toBeInTheDocument()
    expect(screen.getAllByText('system:renewal-sweep').length).toBeGreaterThan(0)
    expect(screen.getByText('Legacy HK')).toBeInTheDocument()
    expect(screen.getByText('Overdue by 2d')).toBeInTheDocument()

    const preview = screen.getByRole('heading', { name: 'Next runs' }).closest('section')
    expect(preview).not.toBeNull()
    const list = within(preview as HTMLElement).getByRole('list', { name: 'Next runs' })
    await waitFor(() => expect(within(list).getAllByRole('listitem')).toHaveLength(5))
  })

  it('creates a schedule from the dialog and shows the live preview', async () => {
    const user = userEvent.setup()
    await renderSchedules()

    await user.click(screen.getByRole('button', { name: 'New schedule' }))
    const dialog = await screen.findByRole('dialog', { name: 'New schedule' })
    await user.type(within(dialog).getByLabelText('Name'), 'Morning reconnect')

    await user.click(within(dialog).getByRole('combobox', { name: 'Action' }))
    const actionList = await screen.findByRole('listbox', { name: 'Action' })
    await user.click(within(actionList).getByRole('option', { name: 'Reconnect account' }))

    const preview = await within(dialog).findByRole('list', { name: 'Next runs' })
    await waitFor(() => expect(within(preview).getAllByRole('listitem')).toHaveLength(5))

    await user.click(within(dialog).getByRole('button', { name: 'Create schedule' }))

    expect(await screen.findByText('Schedule “Morning reconnect” created')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: 'New schedule' })).not.toBeInTheDocument()
    })
    expect(await screen.findByText('Morning reconnect')).toBeInTheDocument()
  })

  it('selects a rotate server pool from the account catalog', async () => {
    const user = userEvent.setup()
    await renderSchedules()

    await user.click(screen.getByRole('button', { name: 'New schedule' }))
    const dialog = await screen.findByRole('dialog', { name: 'New schedule' })
    await user.type(within(dialog).getByLabelText('Name'), 'Pool rotate')

    await user.click(within(dialog).getByRole('combobox', { name: 'Rotation strategy' }))
    const strategyList = await screen.findByRole('listbox', { name: 'Rotation strategy' })
    await user.click(within(strategyList).getByRole('option', { name: 'Pool' }))

    const pool = await within(dialog).findByRole('button', { name: 'Server pool' })
    await user.click(pool)
    const poolList = await screen.findByRole('listbox', { name: 'Server pool' })
    await user.click(within(poolList).getByRole('option', { name: /#2/ }))
    await user.click(within(poolList).getByRole('option', { name: /#3/ }))
    await user.click(screen.getByRole('button', { name: 'Done' }))
    expect(pool).toHaveTextContent('2 selected')

    await user.click(within(dialog).getByRole('button', { name: 'Create schedule' }))
    expect(await screen.findByText('Schedule “Pool rotate” created')).toBeInTheDocument()
  })

  it('edits a schedule from the dialog and sends the current revision', async () => {
    const user = userEvent.setup()
    await renderSchedules()

    const row = scheduleRow('Nightly rotate')
    await user.click(within(row).getByRole('button', { name: 'Edit Nightly rotate' }))
    const dialog = await screen.findByRole('dialog', { name: 'Edit schedule' })
    const name = within(dialog).getByLabelText('Name')
    await user.clear(name)
    await user.type(name, 'Nightly rotate v2')
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))

    expect(await screen.findByText('Schedule “Nightly rotate v2” updated')).toBeInTheDocument()
    expect(scheduleRow('Nightly rotate v2')).toBeInTheDocument()
  })

  it('sends the ARM token when an edit turns dry run off', async () => {
    const patch = vi.spyOn(api, 'patchSchedule')
    const user = userEvent.setup()
    await renderSchedules()

    const row = scheduleRow('Reconnect watchdog')
    await user.click(within(row).getByRole('button', { name: 'Edit Reconnect watchdog' }))
    const dialog = await screen.findByRole('dialog', { name: 'Edit schedule' })
    await user.click(within(dialog).getByRole('checkbox', { name: 'Dry run' }))
    await user.click(within(dialog).getByRole('button', { name: 'Save changes' }))

    expect(await screen.findByText('Schedule “Reconnect watchdog” updated')).toBeInTheDocument()
    await waitFor(() => {
      expect(patch).toHaveBeenCalledWith(
        'sch-002',
        1,
        expect.objectContaining({
          confirm: 'ARM',
          guard: expect.objectContaining({ dryRun: false }),
        }),
      )
    })
  })

  it('confirms a manual run and reports the finished state', async () => {
    const user = userEvent.setup()
    await renderSchedules()

    const row = scheduleRow('Nightly rotate')
    await user.click(within(row).getByRole('button', { name: 'Run Nightly rotate now' }))

    const dialog = await screen.findByRole('dialog', { name: 'Run schedule now?' })
    expect(within(dialog).getByText(/RUN confirmation token/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Run now' }))

    expect(await screen.findByText('Run finished: Succeeded')).toBeInTheDocument()
  })

  it('retries a stale If-Match once and surfaces the refreshed row', async () => {
    const original = api.patchSchedule.bind(api)
    let calls = 0
    vi.spyOn(api, 'patchSchedule').mockImplementation(async (id, revision, input) => {
      calls += 1
      if (calls === 1) {
        throw new ScheduleActionError(
          412,
          'schedule revision changed',
          'PRECONDITION_FAILED',
          'stale_revision',
          { currentRevision: '2' },
        )
      }
      return original(id, revision, input)
    })

    const user = userEvent.setup()
    await renderSchedules()

    const row = scheduleRow('Nightly rotate')
    await user.click(within(row).getByRole('switch', { name: 'Pause Nightly rotate' }))

    expect(await screen.findByText('Schedule “Nightly rotate” paused')).toBeInTheDocument()
    expect(calls).toBe(2)
    expect(await screen.findByText('Schedule changed elsewhere')).toBeInTheDocument()
  })

  it('honors a 429 Retry-After and disables writes for the countdown', async () => {
    vi.spyOn(api, 'patchSchedule').mockRejectedValue(
      new ScheduleActionError(429, 'quota exceeded', 'QUOTA_EXCEEDED', 'cooldown', null, 1),
    )
    const user = userEvent.setup()
    await renderSchedules()

    const row = scheduleRow('Nightly rotate')
    await user.click(within(row).getByRole('switch', { name: 'Pause Nightly rotate' }))

    expect((await screen.findAllByText(/Try again in 1s/)).length).toBeGreaterThan(0)
    expect(within(scheduleRow('Nightly rotate')).getByRole('switch', { name: 'Pause Nightly rotate' })).toBeDisabled()
  })

  it('rejects a stale revision in the mock API, mirroring the daemon ETag', async () => {
    const payload = await api.listSchedules()
    const schedules = Array.isArray(payload) ? payload : (payload.schedules ?? [])
    const schedule = schedules[0]
    await expect(
      api.patchSchedule(schedule.id, schedule.revision + 5, { name: 'stale' }),
    ).rejects.toMatchObject({
      status: 412,
      code: 'PRECONDITION_FAILED',
      reason: 'stale_revision',
    })
  })

  it('deletes a schedule after confirmation', async () => {
    const user = userEvent.setup()
    await renderSchedules()

    const row = scheduleRow('Weekend rotate')
    await user.click(within(row).getByRole('button', { name: 'Delete Weekend rotate' }))

    const dialog = await screen.findByRole('dialog', { name: 'Delete schedule?' })
    await user.click(within(dialog).getByRole('button', { name: 'Delete schedule' }))

    expect(await screen.findByText('Schedule “Weekend rotate” deleted')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('Weekend rotate')).not.toBeInTheDocument()
    })
  })

  it('opens run history with failure classes and reasons', async () => {
    const user = userEvent.setup()
    await renderSchedules()

    const row = scheduleRow('Weekend rotate')
    await user.click(within(row).getByRole('button', { name: 'Run history for Weekend rotate' }))

    const drawer = await screen.findByRole('dialog', { name: 'Run history' })
    expect(
      (await within(drawer).findAllByText('Permanent — fix the target and re-enable')).length,
    ).toBeGreaterThan(0)
    expect(
      within(drawer).getAllByText('No eligible server in the retained catalog').length,
    ).toBeGreaterThan(0)
    expect(within(drawer).getAllByText('Failed').length).toBeGreaterThan(0)
    expect(
      within(drawer).getAllByText('catalog has no eligible jp-tyo server').length,
    ).toBeGreaterThan(0)
  })

  it('shows an honest disabled state when the daemon reports the scheduler off', async () => {
    vi.spyOn(api, 'listSchedules').mockResolvedValue({
      enabled: false,
      reason: 'AJS_SCHEDULES is not 1',
      schedules: [],
    })
    render(<App />)

    expect(await screen.findByText('Scheduler is off')).toBeInTheDocument()
    expect(
      screen.getAllByText(/The daemon reports schedules disabled \(AJS_SCHEDULES is not 1\)/).length,
    ).toBeGreaterThan(0)
    expect(
      screen.getAllByRole('button', { name: 'New schedule' }).every((button) => button.hasAttribute('disabled')),
    ).toBe(true)
  })

  it('renders the page in Chinese with full nav and renewal copy', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    render(<App />)

    expect(await screen.findByRole('heading', { name: '计划任务', level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '新建计划' })).toBeInTheDocument()
    expect(screen.getByText('续费巡检')).toBeInTheDocument()
    expect(screen.getAllByText('system:renewal-sweep').length).toBeGreaterThan(0)
    expect(screen.getByRole('columnheader', { name: '下次运行' })).toBeInTheDocument()
    const sidebar = screen.getByRole('complementary', { name: '主导航' })
    expect(within(sidebar).getByRole('link', { name: '计划任务' })).toBeInTheDocument()
  })
})

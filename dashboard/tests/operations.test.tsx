import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider } from '../src/components/Toast'
import { api } from '../src/lib/api'
import { LanguageProvider } from '../src/lib/i18n'
import type { Operation, SelectNodeResult } from '../src/lib/types'
import Operations from '../src/pages/Operations'

function operation(overrides: Partial<Operation> = {}): Operation {
  return {
    id: 'op-0001',
    type: 'account.switch',
    state: 'running',
    accountId: 'acc-01',
    target: { serverId: 'hk-01' },
    requestedBy: 'operator',
    createdAt: '2026-10-26T00:00:00Z',
    startedAt: '2026-10-26T00:00:01Z',
    finishedAt: '',
    steps: [],
    result: null,
    error: null,
    ...overrides,
  }
}

const SUCCEEDED_RESULT: SelectNodeResult = {
  changed: true,
  accountId: 'acc-01',
  nodeId: 'node-03',
  serverId: 'hk-01',
  gatePort: 13002,
  egressIp: '203.0.113.13',
  sessionHealthy: true,
  durationMs: 1_200,
}

function renderOperations() {
  return render(
    <LanguageProvider>
      <ToastProvider>
        <Operations />
      </ToastProvider>
    </LanguageProvider>,
  )
}

beforeEach(async () => {
  localStorage.clear()
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    get: () => 'visible',
  })
  await api.resetMock()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('operations page', () => {
  it('polls while an operation is active and stops once the list is terminal', async () => {
    vi.useFakeTimers()
    const running = operation({ id: 'op-0009', state: 'running' })
    const succeeded = operation({
      id: 'op-0009',
      state: 'succeeded',
      finishedAt: '2026-10-26T00:00:03Z',
      result: SUCCEEDED_RESULT,
    })
    const list = vi
      .spyOn(api, 'listOperations')
      .mockResolvedValueOnce([running])
      .mockResolvedValue([succeeded])

    renderOperations()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(screen.getByText('Running')).toBeInTheDocument()
    expect(list).toHaveBeenCalledTimes(1)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000)
    })
    expect(screen.getByText('Succeeded')).toBeInTheDocument()
    expect(list).toHaveBeenCalledTimes(2)

    // The record is terminal now: no timer may fire another poll.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000)
    })
    expect(list).toHaveBeenCalledTimes(2)
  })

  it('queues a select-node request and follows the operation to a terminal state', async () => {
    const user = userEvent.setup()
    const queued = operation({ id: 'op-test-1', state: 'queued', startedAt: '' })
    const succeeded = operation({
      id: 'op-test-1',
      state: 'succeeded',
      finishedAt: '2026-10-26T00:00:03Z',
      result: SUCCEEDED_RESULT,
    })
    const selectNode = vi
      .spyOn(api, 'selectNode')
      .mockResolvedValue({ kind: 'operation', operation: queued })
    const getOperation = vi.spyOn(api, 'getOperation').mockResolvedValue(succeeded)

    renderOperations()
    await screen.findByRole('heading', { name: 'Operations', level: 1 })

    await user.click(screen.getByRole('button', { name: 'Account' }))
    await screen.findByRole('listbox', { name: 'Account' })
    await user.click(await screen.findByRole('option', { name: /Northwind Ops/ }))

    const serverPicker = screen.getByRole('button', { name: 'Server' })
    await waitFor(() => expect(serverPicker).toBeEnabled())
    await user.click(serverPicker)
    await screen.findByRole('listbox', { name: 'Server' })
    await user.click(await screen.findByRole('option', { name: /Hong Kong #3/ }))

    await user.click(screen.getByRole('button', { name: 'Select node' }))

    await waitFor(() =>
      expect(selectNode).toHaveBeenCalledWith('acc-01', {
        target: { serverId: 'hk-01' },
        ifCurrent: 'noop',
        timeoutSeconds: 60,
        wait: false,
      }),
    )
    expect(getOperation).toHaveBeenCalledWith('op-test-1')
    expect(await screen.findByText('Operation op-test-1')).toBeInTheDocument()
    expect((await screen.findAllByText('Switched to hk-01')).length).toBeGreaterThan(0)
    expect(screen.getByText(/Egress 203\.0\.113\.13 · gate 13002/)).toBeInTheDocument()
  })

  it('renders the history, form and error codes in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    renderOperations()

    expect(
      await screen.findByRole('heading', { name: '运维操作', level: 1 }),
    ).toBeInTheDocument()
    expect(screen.getByRole('table', { name: '运维操作' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '账号' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '服务器' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '选择节点' })).toBeInTheDocument()
    expect(screen.getAllByText('成功').length).toBeGreaterThan(0)
    expect(screen.getByText('UPSTREAM_UNAVAILABLE')).toBeInTheDocument()
  })
})

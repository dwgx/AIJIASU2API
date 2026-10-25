import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Mock } from 'vitest'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import Debug from '../src/pages/Debug'
import { api } from '../src/lib/api'
import type { SubscribeDebugOptions } from '../src/lib/api'
import { LanguageProvider } from '../src/lib/i18n'
import type { Account, DebugTrace, DebugTracePage } from '../src/lib/types'

function trace(seq: number, overrides: Partial<DebugTrace> = {}): DebugTrace {
  return {
    seq,
    at: new Date(Date.UTC(2026, 9, 26, 7, 0, seq)).toISOString(),
    kind: 'switch.step',
    accountId: 'acc-01',
    nodeId: 'node-01',
    slot: 1,
    generation: 1,
    step: 'login',
    state: 'ok',
    durationMs: 42,
    detail: `step ${seq}`,
    ...overrides,
  }
}

function snapshot(entries: DebugTrace[]): DebugTracePage {
  return {
    enabled: true,
    capacity: 500,
    retained: entries.length,
    evicted: 0,
    oldestSeq: entries[0]?.seq,
    nextSeq: entries.at(-1)?.seq ?? 0,
    entries,
  }
}

function renderPage() {
  return render(
    <LanguageProvider>
      <Debug />
    </LanguageProvider>,
  )
}

/** Replaces the transport and records every subscription the page opens. */
function captureStream(): {
  calls: SubscribeDebugOptions[]
  closes: Mock[]
} {
  const calls: SubscribeDebugOptions[] = []
  const closes: Mock[] = []
  vi.spyOn(api, 'subscribeDebug').mockImplementation((options) => {
    calls.push(options)
    const close = vi.fn()
    closes.push(close)
    return { close, lastSeq: () => 0 }
  })
  return { calls, closes }
}

function stubAccounts() {
  vi.spyOn(api, 'listAccounts').mockResolvedValue([
    { id: 'acc-01', label: 'Alpha' },
    { id: 'acc-02', label: 'Beta' },
  ] as Account[])
}

function stubSnapshot(entries: DebugTrace[]) {
  vi.spyOn(api, 'getDebugTrace').mockResolvedValue(snapshot(entries))
}

function dataRows(): HTMLElement[] {
  const table = screen.getByRole('table', { name: 'Debug trace entries' })
  return within(table).getAllByRole('row').slice(1)
}

beforeEach(async () => {
  localStorage.clear()
  await api.resetMock()
})

describe('debug page', () => {
  it('streams live appends and renders seq, time, slot, generation, node and duration', async () => {
    stubAccounts()
    stubSnapshot([trace(1)])
    const { calls } = captureStream()
    renderPage()

    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0]!.since).toBe(0)
    expect(
      screen.getByText(/In-memory ring · 500 entries · lost on restart/),
    ).toBeInTheDocument()

    act(() => {
      calls[0]!.onState?.('live', { attempt: 0, lastSeq: 1 })
      calls[0]!.onEntry(
        trace(2, {
          kind: 'gate.open',
          nodeId: 'node-02',
          slot: 2,
          generation: 3,
          state: 'open',
          durationMs: 7,
        }),
      )
    })

    await waitFor(() => expect(dataRows()).toHaveLength(2))
    const newest = dataRows()[0]!
    expect(newest).toHaveTextContent('#2')
    expect(newest).toHaveTextContent('gate.open')
    expect(newest).toHaveTextContent('node-02')
    expect(newest).toHaveTextContent('2')
    expect(newest).toHaveTextContent('3')
    expect(newest).toHaveTextContent('7 ms')
    expect(newest).toHaveTextContent('open')
    expect(newest.querySelector('time')).toHaveAttribute('dateTime', trace(2).at)

    expect(screen.getByText('Live')).toBeInTheDocument()
    expect(screen.getByText('2 / 2 shown')).toBeInTheDocument()
  })

  it('filters by account and kind without touching the stream', async () => {
    const user = userEvent.setup()
    stubAccounts()
    stubSnapshot([
      trace(1, { accountId: 'acc-01', kind: 'switch.step' }),
      trace(2, { accountId: 'acc-02', kind: 'gate.open', nodeId: 'node-02' }),
      trace(3, { accountId: 'acc-02', kind: 'switch.step', nodeId: 'node-02' }),
    ])
    const { calls, closes } = captureStream()
    renderPage()

    await waitFor(() => expect(calls).toHaveLength(1))
    expect(dataRows()).toHaveLength(3)

    const accountSelect = screen.getByRole('button', { name: 'Filter by account' })
    await user.click(accountSelect)
    const accountList = await screen.findByRole('listbox', { name: 'Filter by account' })
    await user.click(within(accountList).getByRole('option', { name: 'Beta' }))

    await waitFor(() => expect(dataRows()).toHaveLength(2))
    expect(screen.getByText('2 / 3 shown')).toBeInTheDocument()

    const kindSelect = screen.getByRole('combobox', { name: 'Filter by kind' })
    await user.click(kindSelect)
    const kindList = await screen.findByRole('listbox', { name: 'Filter by kind' })
    await user.click(within(kindList).getByRole('option', { name: 'gate.open' }))

    await waitFor(() => expect(dataRows()).toHaveLength(1))
    expect(dataRows()[0]).toHaveTextContent('gate.open')
    expect(screen.getByText('1 / 3 shown')).toBeInTheDocument()
    expect(calls).toHaveLength(1)
    expect(closes[0]).not.toHaveBeenCalled()
  })

  it('pauses the visible window, buffers new rows and resumes without reconnecting', async () => {
    const user = userEvent.setup()
    stubAccounts()
    stubSnapshot([trace(1), trace(2)])
    const { calls, closes } = captureStream()
    renderPage()

    await waitFor(() => expect(calls).toHaveLength(1))
    act(() => calls[0]!.onState?.('live', { attempt: 0, lastSeq: 2 }))

    await user.click(screen.getByRole('button', { name: 'Pause' }))
    expect(screen.getByText('Paused')).toBeInTheDocument()

    act(() => {
      calls[0]!.onEntry(trace(3))
      calls[0]!.onEntry(trace(4))
    })

    // The frozen window stays at sequence 2 while the buffer grows.
    await waitFor(() => expect(screen.getByText('2 new')).toBeInTheDocument())
    expect(dataRows()).toHaveLength(2)
    expect(screen.getByText('2 / 4 shown')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Resume' }))
    await waitFor(() => expect(dataRows()).toHaveLength(4))
    expect(dataRows()[0]).toHaveTextContent('#4')
    expect(screen.getByText('4 / 4 shown')).toBeInTheDocument()
    expect(screen.getByText('Live')).toBeInTheDocument()
    expect(screen.queryByText('2 new')).not.toBeInTheDocument()
    expect(calls).toHaveLength(1)
    expect(closes[0]).not.toHaveBeenCalled()
  })

  it('shows the stream state chip through connecting, live, reconnecting and offline', async () => {
    stubAccounts()
    stubSnapshot([trace(1)])
    const { calls } = captureStream()
    renderPage()

    await waitFor(() => expect(calls).toHaveLength(1))
    expect(screen.getByText('Connecting')).toBeInTheDocument()

    act(() => calls[0]!.onState?.('live', { attempt: 0, lastSeq: 1 }))
    expect(screen.getByText('Live')).toBeInTheDocument()

    act(() =>
      calls[0]!.onState?.('reconnecting', {
        attempt: 2,
        retryInMs: 4_000,
        lastSeq: 1,
      }),
    )
    expect(screen.getByText('Reconnecting')).toBeInTheDocument()
    expect(screen.getByText(/Retry in 4s/)).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('Debug stream connection lost')

    act(() => calls[0]!.onState?.('offline', { attempt: 3, lastSeq: 1 }))
    expect(screen.getByText('Offline')).toBeInTheDocument()
  })

  it('drops the old rows and says so when the daemon restarts the sequence', async () => {
    stubAccounts()
    stubSnapshot([trace(9), trace(10)])
    const { calls } = captureStream()
    renderPage()

    await waitFor(() => expect(calls).toHaveLength(1))
    act(() => calls[0]!.onState?.('live', { attempt: 0, lastSeq: 10 }))
    expect(dataRows()).toHaveLength(2)

    act(() => {
      calls[0]!.onRestart?.()
      calls[0]!.onEntry(trace(1, { nodeId: 'node-09' }))
    })

    await waitFor(() => expect(dataRows()).toHaveLength(1))
    expect(dataRows()[0]).toHaveTextContent('#1')
    expect(screen.getByText(/The daemon restarted/)).toBeInTheDocument()
  })

  it('renders the notice, columns and stream chip in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    stubAccounts()
    stubSnapshot([trace(1)])
    captureStream()
    renderPage()

    expect(
      await screen.findByRole('heading', { name: '调试追踪', level: 1 }),
    ).toBeInTheDocument()
    expect(screen.getByText(/内存环 · 500 条 · 重启即丢失/)).toBeInTheDocument()
    expect(await screen.findByRole('table', { name: '调试追踪条目' })).toBeInTheDocument()
    for (const column of ['序号', '时间', '类型', '账户', '槽位', '代次', '节点', '耗时', '详情']) {
      expect(screen.getByRole('columnheader', { name: column })).toBeInTheDocument()
    }
    expect(screen.getByText('连接中')).toBeInTheDocument()
    expect(screen.getByText('显示 1 / 1 条')).toBeInTheDocument()
  })

  it('serves the deterministic mock ring in mock mode', async () => {
    renderPage()

    expect(await screen.findByText('#36')).toBeInTheDocument()
    expect(screen.getByText('Live')).toBeInTheDocument()
    expect(screen.getByText('36 / 36 shown')).toBeInTheDocument()
    expect(screen.getByText(/Retained 36 \/ 500/)).toBeInTheDocument()
  })

  it('mounts at /debug with a sidebar entry', async () => {
    window.history.pushState({}, '', '/debug')
    render(<App />)

    expect(
      await screen.findByRole('heading', { name: 'Debug trace', level: 1 }),
    ).toBeInTheDocument()
    const sidebar = screen.getByRole('complementary', { name: 'Primary navigation' })
    expect(within(sidebar).getByRole('link', { name: 'Debug' })).toHaveAttribute(
      'href',
      '/debug',
    )
  })
})

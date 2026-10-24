import { act, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { HealthPanel } from '../src/components/nodes/HealthPanel'
import { api } from '../src/lib/api'
import { LanguageProvider } from '../src/lib/i18n'
import type { HealthView, NodeHealth } from '../src/lib/types'

const AT = '2026-10-24T12:00:00Z'

function makeNode(partial: Partial<NodeHealth> & Pick<NodeHealth, 'nodeId' | 'state'>): NodeHealth {
  return {
    count: 120,
    ok: 120,
    successRate: 1,
    p50Ms: 40,
    p95Ms: 64,
    minMs: 32,
    maxMs: 96,
    consecutiveFailures: 0,
    last: { at: AT, latencyMs: 42, ok: true },
    ...partial,
  }
}

const OK_NODE = makeNode({ nodeId: 'node-a', label: 'alpha', state: 'ok' })
const DEGRADED_NODE = makeNode({
  nodeId: 'node-b',
  label: 'bravo',
  state: 'degraded',
  ok: 104,
  successRate: 0.87,
  p50Ms: 60,
  p95Ms: 940,
  last: { at: AT, latencyMs: 55, ok: true },
})
const DOWN_NODE = makeNode({
  nodeId: 'node-c',
  label: 'charlie',
  state: 'down',
  ok: 0,
  successRate: 0,
  p50Ms: 0,
  p95Ms: 0,
  minMs: 0,
  maxMs: 0,
  consecutiveFailures: 5,
  last: { at: AT, latencyMs: 3000, ok: false, errorClass: 'timeout' },
})

const VIEW: HealthView = { enabled: true, nodes: [OK_NODE, DEGRADED_NODE, DOWN_NODE] }

function renderPanel(variant: 'full' | 'compact' = 'full') {
  return render(
    <MemoryRouter>
      <LanguageProvider>
        <HealthPanel variant={variant} />
      </LanguageProvider>
    </MemoryRouter>,
  )
}

/** Flushes the loader promise without advancing the clock. */
async function settle() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

function rowOf(name: string): HTMLElement {
  const cell = screen.getByText(name)
  const row = cell.closest('tr')
  if (!row) throw new Error(`no table row for ${name}`)
  return row
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.useRealTimers()
  delete (document as unknown as Record<string, unknown>).visibilityState
})

describe('HealthPanel', () => {
  it('renders ok, degraded and down rows with their measured aggregates', async () => {
    vi.spyOn(api, 'getNodeHealth').mockResolvedValue(VIEW)
    renderPanel()

    expect(await screen.findByText('1 ok · 1 degraded · 1 down')).toBeInTheDocument()

    const okRow = rowOf('alpha · node-a')
    expect(within(okRow).getByText('ok')).toBeInTheDocument()
    expect(within(okRow).getByText('42 ms')).toBeInTheDocument()
    expect(within(okRow).getByText('40 ms')).toBeInTheDocument()
    expect(within(okRow).getByText('64 ms')).toBeInTheDocument()
    expect(within(okRow).getByText('100.0%')).toBeInTheDocument()
    expect(within(okRow).getByText('0')).toBeInTheDocument()

    const degradedRow = rowOf('bravo · node-b')
    expect(within(degradedRow).getByText('degraded')).toBeInTheDocument()
    expect(within(degradedRow).getByText('55 ms')).toBeInTheDocument()
    expect(within(degradedRow).getByText('60 ms')).toBeInTheDocument()
    expect(within(degradedRow).getByText('940 ms')).toBeInTheDocument()
    expect(within(degradedRow).getByText('87.0%')).toBeInTheDocument()

    const downRow = rowOf('charlie · node-c')
    expect(within(downRow).getByText('down')).toBeInTheDocument()
    expect(within(downRow).getByText('3,000 ms')).toBeInTheDocument()
    expect(within(downRow).getByText('timeout')).toBeInTheDocument()
    expect(within(downRow).getByText('0%')).toBeInTheDocument()
    expect(within(downRow).getByText('5')).toBeInTheDocument()
    // Percentiles cover successful probes only; an all-failed window has none,
    // and no sparkline can be drawn from failed probes either.
    expect(within(downRow).getAllByText('—')).toHaveLength(3)
  })

  it('builds the sparkline from successive samples and keeps the last 60', async () => {
    vi.useFakeTimers()
    let call = 0
    vi.spyOn(api, 'getNodeHealth').mockImplementation(async () => {
      call += 1
      return {
        enabled: true,
        nodes: [
          makeNode({
            nodeId: 'node-a',
            label: 'alpha',
            state: 'ok',
            last: {
              at: new Date(Date.UTC(2026, 9, 24, 12, 0, 0) + call * 15_000).toISOString(),
              latencyMs: 40 + call,
              ok: true,
            },
          }),
        ],
      }
    })

    renderPanel()
    await settle()
    await advance(15_000)
    await advance(15_000)

    const spark = screen.getByRole('img', { name: 'alpha latency · 3 samples' })
    const paths = spark.querySelectorAll('path')
    expect(paths).toHaveLength(2)
    expect(paths[1].getAttribute('d')).toContain('L')

    for (let index = 0; index < 60; index += 1) await advance(15_000)
    expect(screen.getByRole('img', { name: 'alpha latency · 60 samples' })).toBeInTheDocument()
  })

  it('explains a disabled sampler and never starts a poll timer', async () => {
    vi.useFakeTimers()
    const spy = vi.spyOn(api, 'getNodeHealth').mockResolvedValue({
      enabled: false,
      reason: 'AJS_HEALTH_PROBE=0',
      nodes: [],
    })

    renderPanel()
    await settle()
    await settle()

    expect(screen.getByText('Health sampling is disabled')).toBeInTheDocument()
    expect(screen.getByText('Daemon reason: AJS_HEALTH_PROBE=0')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()

    const settledCalls = spy.mock.calls.length
    await advance(60_000)
    expect(spy.mock.calls.length).toBe(settledCalls)
  })

  it('pauses polling while hidden, refreshes on return and stops on unmount', async () => {
    vi.useFakeTimers()
    let visibility: DocumentVisibilityState = 'visible'
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => visibility,
    })
    const spy = vi.spyOn(api, 'getNodeHealth').mockResolvedValue({
      enabled: true,
      nodes: [OK_NODE],
    })

    const { unmount } = renderPanel()
    await settle()
    expect(spy).toHaveBeenCalledTimes(1)

    visibility = 'hidden'
    act(() => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await advance(45_000)
    expect(spy).toHaveBeenCalledTimes(1)

    visibility = 'visible'
    act(() => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await settle()
    expect(spy).toHaveBeenCalledTimes(2)

    await advance(15_000)
    expect(spy).toHaveBeenCalledTimes(3)

    unmount()
    await advance(60_000)
    expect(spy).toHaveBeenCalledTimes(3)
  })

  it('renders the health copy in Chinese', async () => {
    localStorage.setItem('ajs.lang', 'zh')
    vi.spyOn(api, 'getNodeHealth').mockResolvedValue(VIEW)
    renderPanel()

    expect(await screen.findByRole('heading', { name: '节点健康' })).toBeInTheDocument()
    expect(await screen.findByText('1 正常 · 1 降级 · 1 故障')).toBeInTheDocument()
    const okRow = rowOf('alpha · node-a')
    expect(within(okRow).getByText('正常')).toBeInTheDocument()
    const downRow = rowOf('charlie · node-c')
    expect(within(downRow).getByText('故障')).toBeInTheDocument()
    expect(within(downRow).getByText('超时')).toBeInTheDocument()
    expect(screen.getByText('连续失败')).toBeInTheDocument()
  })

  it('shows an honest empty state when no node is watched', async () => {
    vi.spyOn(api, 'getNodeHealth').mockResolvedValue({ enabled: true, nodes: [] })
    renderPanel()

    expect(await screen.findByText('No nodes watched')).toBeInTheDocument()
    expect(
      screen.getByText('Health appears once the daemon samples a running gate.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('says so when every watched node is ok', async () => {
    vi.spyOn(api, 'getNodeHealth').mockResolvedValue({ enabled: true, nodes: [OK_NODE] })
    renderPanel()

    expect(await screen.findByText('All watched nodes are ok.')).toBeInTheDocument()
    expect(screen.getByText('1 ok · 0 degraded · 0 down')).toBeInTheDocument()
  })

  it('summarizes state counts and the worst node in the compact card', async () => {
    vi.spyOn(api, 'getNodeHealth').mockResolvedValue(VIEW)
    renderPanel('compact')

    expect(await screen.findByRole('heading', { name: 'Node health' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'View nodes' })).toHaveAttribute('href', '/nodes')
    expect(screen.getByText('1 ok')).toBeInTheDocument()
    expect(screen.getByText('1 degraded')).toBeInTheDocument()
    expect(screen.getByText('1 down')).toBeInTheDocument()

    const worst = screen.getByText('Worst node').closest('.health-compact__worst')
    expect(worst).not.toBeNull()
    expect(within(worst as HTMLElement).getByText('charlie')).toBeInTheDocument()
    expect(within(worst as HTMLElement).getByText('down')).toBeInTheDocument()
    expect(within(worst as HTMLElement).getByText('0%')).toBeInTheDocument()
  })

  it('keeps a daemon error visible with a retry action', async () => {
    vi.spyOn(api, 'getNodeHealth').mockRejectedValue(new Error('daemon offline'))
    renderPanel()

    expect(await screen.findByText('daemon offline')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })
})

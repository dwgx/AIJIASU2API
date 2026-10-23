import { render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { api } from '../src/lib/api'
import type { Metrics } from '../src/lib/types'

const NO_TELEMETRY: Metrics = {
  range: '24h',
  series: [],
  totals: { requests: 0, egressBytes: 0, ingressBytes: 0, p95LatencyMs: 0 },
  accountHealth: [
    { accountId: 'acc-01', label: 'Northwind Ops', score: 100, state: 'active' },
  ],
  telemetryAvailable: false,
  generatedAt: new Date().toISOString(),
}

beforeEach(async () => {
  localStorage.clear()
  window.history.pushState({}, '', '/')
  await api.resetMock()
})

describe('overview telemetry honesty', () => {
  it('shows an empty state instead of a chart or zeroed KPIs when telemetry is unavailable', async () => {
    vi.spyOn(api, 'getMetrics').mockResolvedValue(NO_TELEMETRY)
    render(<App />)

    expect(await screen.findByText('No gate telemetry yet')).toBeInTheDocument()
    expect(screen.getByText(/does not draw a synthetic curve or zeroed KPIs/)).toBeInTheDocument()
    expect(screen.getByText('not measured')).toBeInTheDocument()

    // No fabricated chart, sparkline or traffic KPI tiles.
    expect(document.querySelector('.chart')).toBeNull()
    expect(document.querySelector('.metric__spark')).toBeNull()
    expect(screen.queryByText('Requests · 24h')).not.toBeInTheDocument()
    expect(screen.queryByText('Egress · 24h')).not.toBeInTheDocument()
    expect(screen.queryByRole('group', { name: 'Traffic range' })).not.toBeInTheDocument()

    // Real counts and account health still render.
    expect(screen.getByText('Live nodes')).toBeInTheDocument()
    expect(screen.getByText('Active leases')).toBeInTheDocument()
    const health = screen.getByRole('heading', { name: 'Account health' }).closest('section')
    expect(health).not.toBeNull()
    expect(within(health as HTMLElement).getByText('Northwind Ops')).toBeInTheDocument()
    expect(within(health as HTMLElement).getByText('100')).toBeInTheDocument()
  })

  it('keeps the chart and traffic KPIs when the daemon reports a real meter', async () => {
    const metrics = await api.getMetrics('24h')
    expect(metrics.telemetryAvailable).toBe(true)

    render(<App />)

    expect(await screen.findByText('Requests · 24h')).toBeInTheDocument()
    expect(screen.getByRole('group', { name: 'Traffic range' })).toBeInTheDocument()
    expect(document.querySelector('.chart')).not.toBeNull()
    expect(screen.queryByText('No gate telemetry yet')).not.toBeInTheDocument()
  })
})

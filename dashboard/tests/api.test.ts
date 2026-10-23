import { beforeEach, describe, expect, it } from 'vitest'
import { api } from '../src/lib/api'

beforeEach(async () => {
  localStorage.clear()
  await api.resetMock()
})

describe('mock api', () => {
  it('serves the deterministic seed dataset', async () => {
    expect(await api.listAccounts()).toHaveLength(6)
    expect(await api.listNodes()).toHaveLength(12)
    expect(await api.listLeases()).toHaveLength(8)
    expect(await api.listUsers()).toHaveLength(4)
    expect(await api.listRequests()).toHaveLength(40)
    expect(await api.listAudit()).toHaveLength(20)
    expect(await api.listKeys()).toHaveLength(6)
  })

  it('builds a metrics series for both ranges', async () => {
    const day = await api.getMetrics('24h')
    const week = await api.getMetrics('7d')
    expect(day.series).toHaveLength(48)
    expect(week.series).toHaveLength(56)
    expect(day.totals.requests).toBeGreaterThan(0)
    expect(day.totals.egressBytes).toBeGreaterThan(0)
    expect(day.totals.ingressBytes).toBeGreaterThan(day.totals.egressBytes)
    expect(day.accountHealth).toHaveLength(6)
  })

  it('rotates a lease and moves the egress IP', async () => {
    const before = (await api.listLeases()).find((lease) => lease.id === 'lease-01')
    expect(before).toBeDefined()
    const rotated = await api.rotateLease('lease-01')
    expect(rotated.egressIp).not.toBe(before?.egressIp)
    expect(new Date(rotated.expiresAt).getTime()).toBeGreaterThan(new Date(before?.expiresAt ?? 0).getTime())
  })

  it('releases a lease from the store', async () => {
    await api.releaseLease('lease-02')
    const leases = await api.listLeases()
    expect(leases).toHaveLength(7)
    expect(leases.some((lease) => lease.id === 'lease-02')).toBe(false)
  })

  it('creates a key with a one-time secret and revokes it', async () => {
    const created = await api.createKey({ userId: 'u-01', label: 'Test key' })
    expect(created.secret).toMatch(/^ajs_live_mock_/)
    expect(created.key.prefix).toBe(created.secret.slice(0, 16))

    const afterCreate = await api.listKeys()
    expect(afterCreate[0].label).toBe('Test key')

    await api.revokeKey(created.key.id)
    const afterRevoke = await api.listKeys()
    expect(afterRevoke.find((key) => key.id === created.key.id)?.revokedAt).not.toBeNull()
  })

  it('creates accounts and rejects duplicate labels', async () => {
    const input = {
      label: 'New Lab',
      username: 'demo-new',
      password: 'placeholder-password',
      platform: 'android' as const,
      concurrency: 25,
      regions: ['hk'],
    }
    const created = await api.createAccount(input)
    expect(created.state).toBe('active')
    expect(await api.listAccounts()).toHaveLength(7)

    await expect(api.createAccount({ ...input, username: 'another' })).rejects.toThrow(/already exists/)
  })

  it('drains a node and pauses an account', async () => {
    const node = await api.setNodeState('node-01', 'DRAINING')
    expect(node.state).toBe('DRAINING')
    const nodes = await api.listNodes()
    expect(nodes.find((entry) => entry.id === 'node-01')?.state).toBe('DRAINING')

    const account = await api.setAccountState('acc-02', 'paused')
    expect(account.state).toBe('paused')
  })

  it('resets mutations back to the seed dataset', async () => {
    await api.releaseLease('lease-01')
    await api.setAccountState('acc-01', 'paused')
    await api.resetMock()

    expect(await api.listLeases()).toHaveLength(8)
    const accounts = await api.listAccounts()
    expect(accounts.find((account) => account.id === 'acc-01')?.state).toBe('active')
  })

  it('persists panel settings in localStorage', async () => {
    const saved = await api.saveSettings({
      transport: 'rawtcp',
      healthIntervalSec: 90,
      leaseTtl: '24h',
      subscriptionRefreshSec: 45,
      theme: 'light',
    })
    expect(saved.transport).toBe('rawtcp')
    expect(JSON.parse(localStorage.getItem('ajs.settings') ?? '{}')).toMatchObject({
      transport: 'rawtcp',
      theme: 'light',
    })
    expect(await api.getSettings()).toMatchObject({ healthIntervalSec: 90 })
  })
})

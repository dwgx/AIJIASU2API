import { beforeEach, describe, expect, it } from 'vitest'
import { api } from '../src/lib/api'
import { getDb, mockAuditExportCsv } from '../src/lib/mock'

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

describe('lab fixtures', () => {
  it('derives counts and gate/session rows from the same mock database', async () => {
    const overview = await api.getLabOverview()
    expect(overview.counts).toMatchObject({ accounts: 6, nodes: 12, liveNodes: 8, gates: 9, leases: 8 })

    const gates = await api.listLabGates()
    const sessions = await api.listLabSessions()
    expect(Array.isArray(gates)).toBe(true)
    expect(Array.isArray(sessions)).toBe(true)
    if (!Array.isArray(gates) || !Array.isArray(sessions)) throw new Error('mock must return arrays')
    expect(gates).toHaveLength(9)
    expect(sessions).toHaveLength(9)
    expect(sessions.every((session) => /…\(32\)$/.test(session.sid))).toBe(true)
    expect(sessions.every((session) => session.keepalive.supported === false)).toBe(true)
  })

  it('serves bounded gate flows with host-only targets and redacted sessions', async () => {
    const view = await api.getLabFlows(200)
    expect(view.enabled).toBe(true)
    expect(view.flows.length).toBe(200)
    expect(view.flows.every((flow) => /…\(32\)$/.test(flow.sid ?? ''))).toBe(true)
    expect(view.flows.every((flow) => !flow.targetHost.includes('://'))).toBe(true)
    expect(view.flows.every((flow) => !('target' in flow))).toBe(true)
    expect(new Set(view.flows.map((flow) => flow.status))).toEqual(new Set(['ok', 'rejected']))

    const limited = await api.getLabFlows(5)
    expect(limited.flows).toHaveLength(5)
    expect(limited.flows[0]?.startedAt >= (limited.flows[4]?.startedAt ?? '')).toBe(true)
  })

  it('checks egress through the mock gate and enforces the 409/429 guards', async () => {
    const result = await api.checkLabEgress('node-01')
    expect(result).toMatchObject({
      nodeId: 'node-01',
      claimed: '203.0.113.11',
      verified: '203.0.113.11',
      match: true,
      source: 'echo',
    })
    expect(result.latencyMs).toBe(42)

    const rows = await api.listLabEgress()
    if (!Array.isArray(rows)) throw new Error('mock must return egress rows')
    expect(rows.find((row) => row.nodeId === 'node-01')).toMatchObject({
      verified: '',
      match: null,
      source: 'fv-claim',
    })

    await expect(api.checkLabEgress('node-01')).rejects.toMatchObject({ status: 429, code: 'QUOTA_EXCEEDED' })
    await expect(api.checkLabEgress('node-03')).rejects.toMatchObject({ status: 409, code: 'CONFLICT' })
  })

  it('never serialises a secret flag value', async () => {
    const flags = await api.getLabFlags()
    const secrets = flags.flags.filter((flag) => flag.secret)
    expect(secrets.length).toBeGreaterThan(0)
    for (const flag of secrets) {
      expect(flag.value).toBeUndefined()
      expect(flag.redacted).toBe(true)
    }
    expect(JSON.stringify(flags)).not.toMatch(/supersecret/i)
  })

  it('returns hashed device identities and a per-account catalog', async () => {
    const device = await api.getAccountDevice('acc-01')
    expect(device.present).toBe(true)
    expect(Object.values(device.hashes).every((value) => /^sha256:[0-9a-f]{64}$/.test(value))).toBe(true)
    expect(device.rawAvailable).toBe(false)

    const catalog = await api.getAccountServers('acc-01')
    expect(catalog.servers).toHaveLength(3)
    expect(catalog.servers.every((server) => !('pingSecret' in server))).toBe(true)
    expect(catalog).toMatchObject({
      total: 3,
      offset: 0,
      limit: 50,
      nextOffset: 3,
      hasMore: false,
    })
    expect(catalog.regions.map((region) => region.code)).toEqual(['cn-sh', 'cn-zj', 'hk'])

    const second = await api.getAccountServers('acc-01', { limit: 1, offset: 1, sort: 'title' })
    expect(second.servers).toHaveLength(1)
    expect(second).toMatchObject({ total: 3, offset: 1, limit: 1, nextOffset: 2, hasMore: true })

    const hongKong = await api.getAccountServers('acc-01', { region: 'hk' })
    expect(hongKong.total).toBe(1)
    expect(hongKong.servers[0]?.region).toBe('hk')
  })

  it('filters the runtime log ring server-side', async () => {
    const logs = await api.getLabLogs({ level: 'ERROR', limit: 20 })
    expect(logs.entries.length).toBeGreaterThan(0)
    expect(logs.entries.every((entry) => entry.level === 'ERROR')).toBe(true)
  })

  it('touches the probe timestamp without inventing latency or egress', async () => {
    const before = (await api.listNodes()).find((node) => node.id === 'node-01')
    expect(before).toBeDefined()
    const after = await api.probeNode('node-01')
    expect(after.latencyMs).toBe(before?.latencyMs)
    expect(after.egressIp).toBe(before?.egressIp)
    expect(after.lastProbeAt).not.toBe(before?.lastProbeAt)
  })

  it('drains nodes and drops leases on logout instead of only pausing', async () => {
    await api.logoutAccount('acc-01')
    const nodes = await api.listNodes()
    expect(nodes.filter((node) => node.accountId === 'acc-01').every((node) => node.state === 'READY')).toBe(true)
    expect((await api.listLeases()).some((lease) => lease.accountId === 'acc-01')).toBe(false)
    expect((await api.listAccounts()).find((account) => account.id === 'acc-01')?.state).toBe('paused')
  })
})

describe('audit export mock', () => {
  it('neutralizes spreadsheet formulas and quotes RFC 4180 cells in the audit CSV', () => {
    const db = getDb()
    const ts = new Date().toISOString()
    db.audit.unshift(
      {
        id: 'audit-formula',
        ts,
        severity: 'warn',
        actor: '=actor',
        action: '+action',
        target: '-target',
        detail: '@detail',
      },
      {
        id: 'audit-quoted',
        ts,
        severity: 'warn',
        actor: 'operator',
        action: 'note',
        target: 'host',
        detail: 'say "hi", now\nnext line',
      },
    )

    const file = mockAuditExportCsv({ severity: 'warn' })
    // Every formula-leading cell carries the apostrophe guard.
    expect(file.content).toContain(",warn,'=actor,'+action,'-target,'@detail,")
    // Quotes and a line break stay inside one RFC 4180 quoted cell.
    expect(file.content).toContain('"say ""hi"", now\nnext line"')
  })
})

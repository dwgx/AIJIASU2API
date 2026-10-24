import { describe, expect, it } from 'vitest'
import {
  PROVINCES,
  PROVINCE_VIEWBOX,
  fillBin,
  parseServerTitle,
  provinceCodeForTitle,
  resolveServerCity,
  summarizeCatalogGeo,
} from '../src/lib/geo'
import type { ServerView } from '../src/lib/types'

function serverView(index: number, title: string, overrides: Partial<ServerView> = {}): ServerView {
  return {
    id: `srv-${index}`,
    title,
    region: 'cn',
    host: `10.0.0.${index}`,
    port: 12180,
    status: 'ok',
    nodeId: null,
    nodeState: null,
    selected: false,
    skippedByLimit: false,
    memberOnly: null,
    fullLoad: false,
    blockingMarks: [],
    hasPingSecret: false,
    latencyMs: null,
    ...overrides,
  }
}

describe('offline china geography', () => {
  it('bundles 31 unique mainland provinces with pre-projected paths', () => {
    expect(PROVINCES).toHaveLength(31)
    expect(new Set(PROVINCES.map((province) => province.code)).size).toBe(31)
    expect(PROVINCE_VIEWBOX).toBe('0 0 1000 800')
    for (const province of PROVINCES) {
      expect(province.path.startsWith('M')).toBe(true)
      expect(province.path.endsWith('Z')).toBe(true)
      expect(province.centroid).toHaveLength(2)
      expect(province.shortName).not.toMatch(/[省市]$/)
      // No runtime map service: geometry is an inline path, never a URL.
      expect(province.path).not.toContain('http')
    }
  })

  it('parses the vendor title suffix and resolves cities to provinces', () => {
    expect(parseServerTitle('镇江 #1')).toBe('镇江')
    expect(parseServerTitle('  哈尔滨 #510 ')).toBe('哈尔滨')
    expect(parseServerTitle('Vendor Edge')).toBe('Vendor Edge')

    expect(provinceCodeForTitle('镇江 #1')).toBe('32')
    expect(provinceCodeForTitle('南京 #61')).toBe('32')
    // 宿州 (安徽) and 宿迁 (江苏) differ by one character: exact/prefix, no fuzzy.
    expect(provinceCodeForTitle('宿州 #1')).toBe('34')
    expect(provinceCodeForTitle('宿迁 #1')).toBe('32')
    // 海南藏族自治州 is a Qinghai prefecture, not Hainan province.
    expect(provinceCodeForTitle('海南藏族自治州 #1')).toBe('63')
    // Legacy/compound labels from the live snapshot.
    expect(provinceCodeForTitle('济南 - 莱芜 #1')).toBe('37')
    expect(provinceCodeForTitle('延吉 #1')).toBe('22')
    // Municipalities are city labels without a 市 suffix.
    expect(provinceCodeForTitle('北京 #1')).toBe('11')
    expect(provinceCodeForTitle('重庆 #1')).toBe('50')
    expect(provinceCodeForTitle('Atlantis #1')).toBe('00')
  })

  it('keeps an alias point for the current marker', () => {
    expect(resolveServerCity('济南 - 莱芜 #1')?.code).toBe('370100')
    expect(resolveServerCity('延吉 #1')?.provinceCode).toBe('22')
    expect(resolveServerCity('延吉 #1')?.x).toBeGreaterThan(0)
    expect(resolveServerCity('nowhere #1')).toBeNull()
  })

  it('groups rows into province counts, cities, connected and the current marker', () => {
    const rows = [
      serverView(1, '镇江 #1', { nodeState: 'LIVE', nodeId: 'acc-07:srv-1' }),
      serverView(2, '南京 #1', { nodeState: 'DRAINING' }),
      serverView(3, '苏州 #1'),
      serverView(4, '哈尔滨 #1'),
      serverView(5, '济南 - 莱芜 #1'),
      serverView(6, 'Atlantis #1'),
    ]

    const geo = summarizeCatalogGeo(rows)
    expect(geo.total).toBe(6)
    expect(geo.unknown).toBe(1)
    expect(geo.provinces).toHaveLength(31)

    const jiangsu = geo.provinces.find((province) => province.code === '32')
    expect(jiangsu).toMatchObject({ count: 3, cities: 3, connected: 2, current: true })
    const heilongjiang = geo.provinces.find((province) => province.code === '23')
    expect(heilongjiang).toMatchObject({ count: 1, current: false })

    expect(geo.current?.title).toBe('镇江 #1')
    expect(geo.currentCity?.code).toBe('321100')
    expect(geo.currentProvince).toBe('32')
  })

  it('uses fixed choropleth bins', () => {
    expect([0, 1, 20, 21, 50, 51, 150, 151, 300, 301].map(fillBin)).toEqual([
      0, 1, 1, 2, 2, 3, 3, 4, 4, 5,
    ])
  })
})

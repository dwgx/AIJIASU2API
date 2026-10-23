import { describe, expect, it } from 'vitest'
import { regionName } from '../src/lib/regions'

describe('region names', () => {
  it('maps vendor country codes to localized names', () => {
    expect(regionName('cn', 'en')).toBe('China')
    expect(regionName('cn', 'zh')).toBe('中国')
    expect(regionName('hk', 'zh')).toBe('中国香港')
    expect(regionName('jp', 'en')).toBe('Japan')
    expect(regionName('sg', 'zh')).toBe('新加坡')
    expect(regionName('kr', 'en')).toBe('South Korea')
    expect(regionName('de', 'zh')).toBe('德国')
    expect(regionName('uk', 'en')).toBe('United Kingdom')
    expect(regionName('us', 'zh')).toBe('美国')
  })

  it('resolves mock city subdivisions and normalizes case', () => {
    expect(regionName('cn-zj', 'en')).toBe('China · Zhejiang')
    expect(regionName('CN-ZJ', 'zh')).toBe('中国 · 浙江')
    expect(regionName('jp-tyo', 'zh')).toBe('日本 · 东京')
    expect(regionName('nl-ams', 'en')).toBe('Netherlands · Amsterdam')
  })

  it('labels unknown or empty codes without inventing a country', () => {
    expect(regionName('unknown', 'en')).toBe('Unknown region')
    expect(regionName('', 'zh')).toBe('未知区域')
    expect(regionName('zz', 'en')).toBe('zz')
  })
})

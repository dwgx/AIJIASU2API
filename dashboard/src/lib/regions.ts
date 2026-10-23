import type { Language } from './i18n'

/**
 * Vendor region codes are ISO 3166-1 alpha-2 country codes (the daemon
 * lowercases the vendor's `ctry` field). Mock fixtures may carry a city
 * subdivision, e.g. `cn-zj`, so both the full tag and its country base are
 * resolved here. The raw code stays available to the UI as secondary mono
 * text; it is never the primary label for a known region.
 */
interface RegionLabel {
  en: string
  zh: string
}

const COUNTRY_NAMES: Record<string, RegionLabel> = {
  ae: { en: 'United Arab Emirates', zh: '阿联酋' },
  ar: { en: 'Argentina', zh: '阿根廷' },
  at: { en: 'Austria', zh: '奥地利' },
  au: { en: 'Australia', zh: '澳大利亚' },
  be: { en: 'Belgium', zh: '比利时' },
  br: { en: 'Brazil', zh: '巴西' },
  ca: { en: 'Canada', zh: '加拿大' },
  ch: { en: 'Switzerland', zh: '瑞士' },
  cl: { en: 'Chile', zh: '智利' },
  cn: { en: 'China', zh: '中国' },
  cz: { en: 'Czechia', zh: '捷克' },
  de: { en: 'Germany', zh: '德国' },
  dk: { en: 'Denmark', zh: '丹麦' },
  eg: { en: 'Egypt', zh: '埃及' },
  es: { en: 'Spain', zh: '西班牙' },
  fi: { en: 'Finland', zh: '芬兰' },
  fr: { en: 'France', zh: '法国' },
  gb: { en: 'United Kingdom', zh: '英国' },
  gr: { en: 'Greece', zh: '希腊' },
  hk: { en: 'Hong Kong', zh: '中国香港' },
  hu: { en: 'Hungary', zh: '匈牙利' },
  id: { en: 'Indonesia', zh: '印度尼西亚' },
  ie: { en: 'Ireland', zh: '爱尔兰' },
  il: { en: 'Israel', zh: '以色列' },
  in: { en: 'India', zh: '印度' },
  it: { en: 'Italy', zh: '意大利' },
  jp: { en: 'Japan', zh: '日本' },
  ke: { en: 'Kenya', zh: '肯尼亚' },
  kr: { en: 'South Korea', zh: '韩国' },
  kz: { en: 'Kazakhstan', zh: '哈萨克斯坦' },
  mo: { en: 'Macao', zh: '中国澳门' },
  mx: { en: 'Mexico', zh: '墨西哥' },
  my: { en: 'Malaysia', zh: '马来西亚' },
  ng: { en: 'Nigeria', zh: '尼日利亚' },
  nl: { en: 'Netherlands', zh: '荷兰' },
  no: { en: 'Norway', zh: '挪威' },
  nz: { en: 'New Zealand', zh: '新西兰' },
  ph: { en: 'Philippines', zh: '菲律宾' },
  pl: { en: 'Poland', zh: '波兰' },
  pt: { en: 'Portugal', zh: '葡萄牙' },
  ro: { en: 'Romania', zh: '罗马尼亚' },
  ru: { en: 'Russia', zh: '俄罗斯' },
  sa: { en: 'Saudi Arabia', zh: '沙特阿拉伯' },
  se: { en: 'Sweden', zh: '瑞典' },
  sg: { en: 'Singapore', zh: '新加坡' },
  th: { en: 'Thailand', zh: '泰国' },
  tr: { en: 'Türkiye', zh: '土耳其' },
  tw: { en: 'Taiwan', zh: '中国台湾' },
  ua: { en: 'Ukraine', zh: '乌克兰' },
  uk: { en: 'United Kingdom', zh: '英国' },
  us: { en: 'United States', zh: '美国' },
  vn: { en: 'Vietnam', zh: '越南' },
  za: { en: 'South Africa', zh: '南非' },
}

const SUBDIVISION_NAMES: Record<string, RegionLabel> = {
  'ca-yyz': { en: 'Canada · Toronto', zh: '加拿大 · 多伦多' },
  'cn-sh': { en: 'China · Shanghai', zh: '中国 · 上海' },
  'cn-zj': { en: 'China · Zhejiang', zh: '中国 · 浙江' },
  'de-fra': { en: 'Germany · Frankfurt', zh: '德国 · 法兰克福' },
  'jp-tyo': { en: 'Japan · Tokyo', zh: '日本 · 东京' },
  'kr-sel': { en: 'South Korea · Seoul', zh: '韩国 · 首尔' },
  'nl-ams': { en: 'Netherlands · Amsterdam', zh: '荷兰 · 阿姆斯特丹' },
  'sg-01': { en: 'Singapore', zh: '新加坡' },
  'tw-tpe': { en: 'Taiwan · Taipei', zh: '中国台湾 · 台北' },
  'uk-lon': { en: 'United Kingdom · London', zh: '英国 · 伦敦' },
  'us-lax': { en: 'United States · Los Angeles', zh: '美国 · 洛杉矶' },
}

const UNKNOWN: RegionLabel = { en: 'Unknown region', zh: '未知区域' }

/** Localized display name for a vendor region code; unknown codes pass through. */
export function regionName(region: string, lang: Language = 'en'): string {
  const code = region.trim().toLowerCase()
  if (!code) return UNKNOWN[lang]
  const subdivision = SUBDIVISION_NAMES[code]
  if (subdivision) return subdivision[lang]
  if (code === 'unknown') return UNKNOWN[lang]
  const country = COUNTRY_NAMES[code.split('-')[0]]
  if (country) return country[lang]
  return region
}

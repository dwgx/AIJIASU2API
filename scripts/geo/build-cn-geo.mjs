#!/usr/bin/env node
/**
 * Builds the dashboard's offline China map assets from DataV GeoAtlas.
 *
 * The dashboard never contacts a map service at runtime: this script fetches
 * the national province boundaries and the per-province city centres once, then
 * pre-projects and simplifies them into two committed JSON files:
 *
 *   dashboard/src/assets/geo/cn-provinces.json  province SVG paths + centroids
 *   dashboard/src/assets/geo/cn-cities.json     prefecture lookup + map points
 *
 * Usage:
 *   node scripts/geo/build-cn-geo.mjs            # fetch, write assets
 *   node scripts/geo/build-cn-geo.mjs --check    # fail if committed assets drift
 *   node scripts/geo/build-cn-geo.mjs --raw DIR  # reuse a downloaded DataV cache
 *
 * Source: https://geo.datav.aliyun.com/areas_v3/bound (free CDN, no key).
 * The generated files record the source URL, retrieval date and the sha256 of
 * the national boundary file they were derived from.
 */
import { createHash } from 'node:crypto'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const OUT_DIR = join(ROOT, 'dashboard', 'src', 'assets', 'geo')
const DATAV = 'https://geo.datav.aliyun.com/areas_v3/bound'

const VIEW_W = 1000
const VIEW_H = 800
const PADDING = 24
const SIMPLIFY_PX = 0.8
const MIN_RING_AREA = 0.6

/** 31 mainland province-level divisions, in GB/T 2260 order. */
const PROVINCES = [
  ['11', '北京市', 'Beijing'],
  ['12', '天津市', 'Tianjin'],
  ['13', '河北省', 'Hebei'],
  ['14', '山西省', 'Shanxi'],
  ['15', '内蒙古自治区', 'Inner Mongolia'],
  ['21', '辽宁省', 'Liaoning'],
  ['22', '吉林省', 'Jilin'],
  ['23', '黑龙江省', 'Heilongjiang'],
  ['31', '上海市', 'Shanghai'],
  ['32', '江苏省', 'Jiangsu'],
  ['33', '浙江省', 'Zhejiang'],
  ['34', '安徽省', 'Anhui'],
  ['35', '福建省', 'Fujian'],
  ['36', '江西省', 'Jiangxi'],
  ['37', '山东省', 'Shandong'],
  ['41', '河南省', 'Henan'],
  ['42', '湖北省', 'Hubei'],
  ['43', '湖南省', 'Hunan'],
  ['44', '广东省', 'Guangdong'],
  ['45', '广西壮族自治区', 'Guangxi'],
  ['46', '海南省', 'Hainan'],
  ['50', '重庆市', 'Chongqing'],
  ['51', '四川省', 'Sichuan'],
  ['52', '贵州省', 'Guizhou'],
  ['53', '云南省', 'Yunnan'],
  ['54', '西藏自治区', 'Tibet'],
  ['61', '陕西省', 'Shaanxi'],
  ['62', '甘肃省', 'Gansu'],
  ['63', '青海省', 'Qinghai'],
  ['64', '宁夏回族自治区', 'Ningxia'],
  ['65', '新疆维吾尔自治区', 'Xinjiang'],
]

/**
 * Vendor titles that are legacy or compound labels rather than DataV city
 * names. `济南 - 莱芜` is Jinan after the 2019 merger; `延吉` is a
 * county-level city under Yanbian (222400), whose centre is the closest
 * prefecture-level point DataV publishes.
 */
const ALIASES = {
  '济南 - 莱芜': '370100',
  延吉: '222400',
}

/**
 * Ethnic descriptors that separate a prefecture's conventional short name
 * from its DataV full name: 湘西土家族苗族自治州 -> 湘西. The list covers the
 * names DataV publishes; a bare 蒙古/柯尔克孜/哈萨克 is stripped separately
 * (博尔塔拉蒙古自治州, 克孜勒苏柯尔克孜自治州, 伊犁哈萨克自治州).
 */
const ETHNIC_GROUPS =
  '(?:蒙古族|回族|藏族|维吾尔族|苗族|彝族|壮族|布依族|朝鲜族|满族|侗族|瑶族|白族|土家族|哈尼族|哈萨克族|傣族|黎族|傈僳族|佤族|畲族|高山族|拉祜族|水族|东乡族|纳西族|景颇族|柯尔克孜族|土族|达斡尔族|仫佬族|羌族|布朗族|撒拉族|毛南族|仡佬族|锡伯族|阿昌族|普米族|塔吉克族|怒族|乌孜别克族|俄罗斯族|鄂温克族|德昂族|保安族|裕固族|京族|塔塔尔族|独龙族|鄂伦春族|赫哲族|门巴族|珞巴族|基诺族)'

/**
 * The vendor labels are conventional short names (`镇江`, `延边`, `湘西`);
 * DataV publishes administrative names (`镇江市`, `延边朝鲜族自治州`). Derive
 * the short form so resolution stays exact instead of prefix-matching.
 */
function shortCityName(name) {
  const suffix = ['自治州', '自治县', '地区', '林区', '盟', '市', '县'].find((value) =>
    name.endsWith(value),
  )
  if (!suffix) return ''
  let base = name.slice(0, -suffix.length)
  if (suffix === '自治州' || suffix === '自治县') {
    base = base.replace(new RegExp(`(?:${ETHNIC_GROUPS})+$`), '')
    base = base.replace(/(?:蒙古|柯尔克孜|哈萨克)$/, '')
  }
  return base.trim()
}

/**
 * Adds one exact-match alias per city short name. An alias is only safe when
 * it identifies a single city; a short form that names a different province
 * (`海南` for 海南藏族自治州) is dropped so the row lands in `unknown` rather
 * than misfiling into 青海.
 */
function addDerivedAliases(cities) {
  const candidates = new Map()
  for (const city of cities) {
    const short = shortCityName(city.name)
    if (!short || short === city.name) continue
    const list = candidates.get(short) ?? []
    list.push(city)
    candidates.set(short, list)
  }
  const provinceByShortName = new Map(
    PROVINCES.map(([code, name]) => [shortProvinceName(name), code]),
  )
  for (const [alias, list] of candidates) {
    if (list.length !== 1) continue
    const city = list[0]
    const province = provinceByShortName.get(alias)
    if (province && province !== city.provinceCode) continue
    city.aliases = [...(city.aliases ?? []), alias]
  }
}

function parseArgs(argv) {
  const args = { check: false, raw: '' }
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]
    if (arg === '--check') args.check = true
    else if (arg === '--raw') args.raw = argv[++index] ?? ''
    else if (arg.startsWith('--raw=')) args.raw = arg.slice('--raw='.length)
    else throw new Error(`unknown argument: ${arg}`)
  }
  return args
}

async function readJson(path) {
  return JSON.parse(await readFile(path, 'utf8'))
}

/** Reads one DataV file from the cache directory or the network. */
async function fetchDatav(rawDir, name) {
  if (rawDir) return readJson(join(rawDir, name))
  const response = await fetch(`${DATAV}/${name}`)
  if (!response.ok) throw new Error(`${name}: HTTP ${response.status}`)
  return response.json()
}

/* --------------------------------------------------------------------------
 * Albers equal-area conic, the projection named in DESIGN-CATALOG §2.6.
 * ------------------------------------------------------------------------ */

const RAD = Math.PI / 180
const PHI1 = 25 * RAD
const PHI2 = 47 * RAD
const PHI0 = 35 * RAD
const LAMBDA0 = 105 * RAD
const N = (Math.sin(PHI1) + Math.sin(PHI2)) / 2
const C = Math.cos(PHI1) ** 2 + 2 * N * Math.sin(PHI1)
const RHO0 = Math.sqrt(C - 2 * N * Math.sin(PHI0)) / N

function albers(lng, lat) {
  const rho = Math.sqrt(C - 2 * N * Math.sin(lat * RAD)) / N
  const theta = N * (lng * RAD - LAMBDA0)
  return [rho * Math.sin(theta), RHO0 - rho * Math.cos(theta)]
}

/** Douglas-Peucker in projected pixel space. */
function simplify(points, tolerance) {
  if (points.length <= 4) return points
  const sq = tolerance * tolerance
  const keep = new Uint8Array(points.length)
  keep[0] = 1
  keep[points.length - 1] = 1
  const stack = [[0, points.length - 1]]
  while (stack.length > 0) {
    const [first, last] = stack.pop()
    let best = -1
    let bestDist = 0
    const [x1, y1] = points[first]
    const [x2, y2] = points[last]
    const dx = x2 - x1
    const dy = y2 - y1
    const lengthSq = dx * dx + dy * dy
    for (let index = first + 1; index < last; index += 1) {
      const [px, py] = points[index]
      let dist
      if (lengthSq === 0) {
        dist = (px - x1) ** 2 + (py - y1) ** 2
      } else {
        const t = Math.max(0, Math.min(1, ((px - x1) * dx + (py - y1) * dy) / lengthSq))
        dist = (px - (x1 + t * dx)) ** 2 + (py - (y1 + t * dy)) ** 2
      }
      if (dist > bestDist) {
        bestDist = dist
        best = index
      }
    }
    if (bestDist > sq) {
      keep[best] = 1
      stack.push([first, best], [best, last])
    }
  }
  return points.filter((_, index) => keep[index] === 1)
}

function ringArea(points) {
  let area = 0
  for (let index = 0; index < points.length; index += 1) {
    const [x1, y1] = points[index]
    const [x2, y2] = points[(index + 1) % points.length]
    area += x1 * y2 - x2 * y1
  }
  return Math.abs(area) / 2
}

function round(value) {
  return Math.round(value * 10) / 10
}

function pathFor(rings) {
  let path = ''
  for (const ring of rings) {
    const [first, ...rest] = ring
    path += `M${round(first[0])},${round(first[1])}`
    for (const [x, y] of rest) path += `L${round(x)},${round(y)}`
    path += 'Z'
  }
  return path
}

/** Polygon rings of a (Multi)Polygon, in projected pixel space. */
function projectRings(geometry, project) {
  const polygons =
    geometry.type === 'Polygon' ? [geometry.coordinates] : geometry.type === 'MultiPolygon' ? geometry.coordinates : []
  const rings = []
  for (const polygon of polygons) {
    for (const ring of polygon) {
      // Hainan's province geometry includes the South China Sea islets down
      // to ~4°N. They would stretch the projection bounds and shrink the
      // mainland, so rings entirely south of Hainan island are not drawn.
      if (ring.every(([, lat]) => lat < 17.8)) continue
      rings.push(ring.map(([lng, lat]) => project(lng, lat)))
    }
  }
  return rings
}

async function build(rawDir) {
  const nationalName = '100000_full.json'
  const nationalBytes = rawDir
    ? await readFile(join(rawDir, nationalName))
    : Buffer.from(await (await fetch(`${DATAV}/${nationalName}`)).text())
  const national = JSON.parse(nationalBytes.toString('utf8'))
  const nationalHash = createHash('sha256').update(nationalBytes).digest('hex')

  const features = new Map()
  for (const feature of national.features) {
    const code = String(feature.properties.adcode ?? '')
    if (!/^\d{2}0000$/.test(code)) continue
    features.set(code.slice(0, 2), feature)
  }

  // Project every province once, then scale all of them with one transform so
  // the relative positions stay true.
  const raw = new Map()
  let minX = Infinity
  let maxX = -Infinity
  let minY = Infinity
  let maxY = -Infinity
  for (const [code, name, nameEn] of PROVINCES) {
    const feature = features.get(code)
    if (!feature) throw new Error(`DataV national file has no province ${code}`)
    const rings = projectRings(feature.geometry, albers)
    raw.set(code, { name, nameEn, feature, rings })
    for (const ring of rings) {
      for (const [x, y] of ring) {
        if (x < minX) minX = x
        if (x > maxX) maxX = x
        if (y < minY) minY = y
        if (y > maxY) maxY = y
      }
    }
  }

  const scale = Math.min((VIEW_W - PADDING * 2) / (maxX - minX), (VIEW_H - PADDING * 2) / (maxY - minY))
  const offsetX = (VIEW_W - (maxX - minX) * scale) / 2
  const offsetY = (VIEW_H - (maxY - minY) * scale) / 2
  const project = (lng, lat) => {
    const [x, y] = albers(lng, lat)
    return [offsetX + (x - minX) * scale, offsetY + (maxY - y) * scale]
  }

  const provinces = []
  for (const [code, name, nameEn] of PROVINCES) {
    const { feature, rings } = raw.get(code)
    const kept = []
    for (const ring of rings) {
      // Simplify in pixel space: the tolerance is a fraction of a viewBox unit,
      // not of the raw Albers coordinates.
      const pixels = ring.map(([x, y]) => [offsetX + (x - minX) * scale, offsetY + (maxY - y) * scale])
      const simplified = simplify(pixels, SIMPLIFY_PX)
      if (simplified.length < 4 || ringArea(simplified) < MIN_RING_AREA) continue
      kept.push(simplified)
    }
    if (kept.length === 0) throw new Error(`province ${code} simplified to nothing`)
    const points = kept.flat()
    const xs = points.map(([x]) => x)
    const ys = points.map(([, y]) => y)
    const centre = feature.properties.centroid ?? feature.properties.center
    const [cx, cy] = project(centre[0], centre[1])
    provinces.push({
      code,
      name,
      shortName: shortProvinceName(name),
      nameEn,
      path: pathFor(kept),
      centroid: [round(cx), round(cy)],
      bbox: [round(Math.min(...xs)), round(Math.min(...ys)), round(Math.max(...xs)), round(Math.max(...ys))],
    })
  }

  // City centres: one lookup entry per prefecture-level city. Municipalities
  // have no city-level feature, so their province feature supplies the point.
  const cities = []
  for (const [code] of PROVINCES) {
    const file = await fetchDatav(rawDir, `${code}0000_full.json`)
    for (const feature of file.features ?? []) {
      const properties = feature.properties ?? {}
      if (properties.level !== 'city') continue
      const centre = properties.centroid ?? properties.center
      if (!centre) continue
      const [x, y] = project(centre[0], centre[1])
      cities.push({
        code: String(properties.adcode),
        name: properties.name,
        provinceCode: code,
        x: round(x),
        y: round(y),
      })
    }
    if (['11', '12', '31', '50'].includes(code)) {
      const feature = features.get(code)
      const centre = feature.properties.centroid ?? feature.properties.center
      const [x, y] = project(centre[0], centre[1])
      cities.push({ code: `${code}0000`, name: nameOf(code), provinceCode: code, x: round(x), y: round(y) })
    }
  }
  addDerivedAliases(cities)
  for (const city of cities) {
    for (const [alias, target] of Object.entries(ALIASES)) {
      if (city.code === target) city.aliases = [...(city.aliases ?? []), alias]
    }
  }

  return {
    provinces: {
      version: 1,
      source: 'DataV GeoAtlas areas_v3 (https://geo.datav.aliyun.com/areas_v3/bound/100000_full.json)',
      sourceSha256: nationalHash,
      projection: { name: 'albers', parallels: [25, 47], centralMeridian: 105, latitudeOfOrigin: 35 },
      viewBox: `0 0 ${VIEW_W} ${VIEW_H}`,
      provinces,
    },
    cities: {
      version: 1,
      source: 'DataV GeoAtlas areas_v3 per-province _full.json',
      cities,
    },
  }
}

/** 江苏省 -> 江苏, 广西壮族自治区 -> 广西; the map labels stay short. */
function shortProvinceName(name) {
  return name.replace(/(?:壮族|回族|维吾尔)?自治区$/, '').replace(/[省市]$/, '')
}

function nameOf(code) {
  return PROVINCES.find(([value]) => value === code)?.[1] ?? code
}

function serialize(value) {
  return `${JSON.stringify(value)}\n`
}

async function main() {
  const args = parseArgs(process.argv.slice(2))
  const { provinces, cities } = await build(args.raw)
  const outputs = [
    [join(OUT_DIR, 'cn-provinces.json'), serialize(provinces)],
    [join(OUT_DIR, 'cn-cities.json'), serialize(cities)],
  ]
  if (args.check) {
    let drifted = false
    for (const [path, expected] of outputs) {
      const actual = await readFile(path, 'utf8').catch(() => '')
      if (actual !== expected) {
        drifted = true
        console.error(`drift: ${path}`)
      }
    }
    if (drifted) process.exitCode = 1
    return
  }
  await mkdir(OUT_DIR, { recursive: true })
  for (const [path, content] of outputs) {
    await writeFile(path, content)
    console.log(`${path}: ${Buffer.byteLength(content)} bytes`)
  }
}

await main()

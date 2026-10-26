// Fail when a lockfile entry carries neither `resolved` nor `integrity`: such a
// lock still installs, but it cannot prove which tarball it came from. Plain
// node, no dependencies; the path is resolved from this file so the script
// works from the repo root and from dashboard/ (CI runs it via npm).
import { readFileSync } from 'node:fs'

const lockfile = new URL('../package-lock.json', import.meta.url)
const { packages = {} } = JSON.parse(readFileSync(lockfile, 'utf8'))

const keys = Object.keys(packages).filter((key) => key !== '')
const missing = keys.filter((key) => {
  const entry = packages[key]
  return !entry.link && (!entry.resolved || !entry.integrity)
})

if (missing.length > 0) {
  console.error(
    `verify-lockfile: FAIL: ${missing.length} of ${keys.length} entries lack resolved or integrity`,
  )
  for (const key of missing) console.error(`  ${key}`)
  console.error('verify-lockfile: regenerate with `rm package-lock.json && npm install --package-lock-only`')
  process.exit(1)
}

console.log(`verify-lockfile: OK: entries ${keys.length} missing 0`)

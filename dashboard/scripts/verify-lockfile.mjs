// Fail when the lockfile cannot prove provenance: it must describe a non-empty
// package set with a root entry, and every installed package must carry both
// `resolved` and `integrity`. A lock that only pins versions installs fine but
// cannot say which tarball it came from; an empty `packages` map proves nothing
// at all. Plain node, no dependencies; the path is resolved from this file so
// the script works from the repo root and from dashboard/ (CI runs it via npm).
import { readFileSync } from 'node:fs'

const lockfile = new URL('../package-lock.json', import.meta.url)
const { packages } = JSON.parse(readFileSync(lockfile, 'utf8'))

function reject(message, details = []) {
  console.error(`verify-lockfile: FAIL: ${message}`)
  for (const line of details) console.error(`  ${line}`)
  process.exit(1)
}

if (!packages || typeof packages !== 'object' || Array.isArray(packages)) {
  reject('no packages map; lockfileVersion 2 or newer is required')
}
if (!Object.hasOwn(packages, '')) {
  reject('no root entry (""); the lockfile does not describe the dashboard package')
}

const keys = Object.keys(packages).filter((key) => key !== '')
if (keys.length === 0) {
  reject('empty package set; the lockfile proves nothing about the installed tree')
}

const missing = keys.filter((key) => {
  const entry = packages[key]
  return !entry.link && (!entry.resolved || !entry.integrity)
})

if (missing.length > 0) {
  reject(`${missing.length} of ${keys.length} entries lack resolved or integrity`, [
    ...missing,
    'regenerate with `rm package-lock.json && npm install --package-lock-only`',
  ])
}

console.log(`verify-lockfile: OK: entries ${keys.length} missing 0`)

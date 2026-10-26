// Every locale must have exactly the same keys as en.json (the source of truth):
// a missing key would silently fall back to English, an extra one is dead weight.
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

const dir = new URL('../src/i18n/locales/', import.meta.url).pathname
const keysOf = (obj, prefix = '') =>
  Object.entries(obj).flatMap(([k, v]) =>
    v && typeof v === 'object' ? keysOf(v, `${prefix}${k}.`) : [`${prefix}${k}`],
  )
const en = new Set(keysOf(JSON.parse(readFileSync(join(dir, 'en.json'), 'utf8'))))
let failed = false
for (const file of readdirSync(dir).filter((f) => f.endsWith('.json') && f !== 'en.json')) {
  const keys = new Set(keysOf(JSON.parse(readFileSync(join(dir, file), 'utf8'))))
  const missing = [...en].filter((k) => !keys.has(k))
  const extra = [...keys].filter((k) => !en.has(k))
  if (missing.length || extra.length) {
    failed = true
    console.error(`${file}: ${missing.length} missing, ${extra.length} extra`)
    for (const k of missing.slice(0, 20)) console.error(`  missing ${k}`)
    for (const k of extra.slice(0, 20)) console.error(`  extra   ${k}`)
  }
}
if (failed) process.exit(1)
console.log('locales ok')

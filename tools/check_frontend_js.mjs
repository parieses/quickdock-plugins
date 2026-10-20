#!/usr/bin/env node
// 抽出 */frontend/index.html 里的所有 <script> 块（无 src）逐个 node --check
// 用法: node tools/check_frontend_js.mjs [--verbose]
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const verbose = process.argv.includes('--verbose')
const tmp = mkdtempSync(join(tmpdir(), 'qdfe-'))

function* htmlFiles(dir) {
  for (const e of readdirSync(dir)) {
    if (e === 'node_modules' || e === '.git' || e === '.workbuddy') continue
    const p = join(dir, e)
    const st = statSync(p)
    if (st.isDirectory()) yield* htmlFiles(p)
    else if (e === 'index.html' && dir.endsWith('frontend')) yield p
  }
}

let files = 0, blocks = 0, bad = 0
for (const f of [...htmlFiles(ROOT)].sort()) {
  files++
  const src = readFileSync(f, 'utf8')
  const re = /<script(?![^>]*\bsrc\s*=)[^>]*>([\s\S]*?)<\/script>/gi
  let m, i = 0
  while ((m = re.exec(src))) {
    i++; blocks++
    const t = join(tmp, `b${blocks}.mjs`)
    writeFileSync(t, m[1], 'utf8')
    try {
      execFileSync(process.execPath, ['--check', t], { stdio: 'pipe' })
      if (verbose) console.log('ok   ' + f.replace(ROOT + '\\', '') + ` #${i}`)
    } catch (e) {
      bad++
      console.log('FAIL ' + f.replace(ROOT + '\\', '') + ` #${i}`)
      console.log((e.stderr?.toString() || '').split('\n').slice(0, 6).join('\n'))
    }
  }
}
rmSync(tmp, { recursive: true, force: true })
console.log(`\nfiles=${files} script_blocks=${blocks} bad=${bad}`)
process.exit(bad ? 1 : 0)
#!/usr/bin/env node
/**
 * src-api-recon · 运行时采集器
 *
 *   node scripts/collect.mjs <OUTDIR>/config.json
 *
 * 做三件事：document-start 注入 preload.js → 遍历路由/点功能点 → 导出 outbound 记录。
 * 只发前端本来就会发的请求；不改包攻击、不提交登录。
 *
 * playwright 解析顺序：本机 require → $RECON_PLAYWRIGHT → ~/.npm/_npx/*（Playwright MCP 的缓存）。
 * 浏览器：$RECON_CHROMIUM || config.chromium || /usr/bin/chromium（Kali 自带）。
 */
import { readFileSync, writeFileSync, mkdirSync, existsSync, readdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { homedir } from 'node:os'

const here = dirname(fileURLToPath(import.meta.url))

/** 在若干候选位置里找一个能用的 playwright。 */
async function loadPlaywright () {
  const candidates = []
  if (process.env.RECON_PLAYWRIGHT) candidates.push(process.env.RECON_PLAYWRIGHT)
  const npxRoot = join(homedir(), '.npm', '_npx')
  if (existsSync(npxRoot)) {
    for (const d of readdirSync(npxRoot)) candidates.push(join(npxRoot, d, 'node_modules', 'playwright'))
  }
  try { return await import('playwright') } catch {}
  for (const c of candidates) {
    try {
      if (!existsSync(c)) continue
      return await import(pathToFileURL(join(c, 'index.mjs')).href)
    } catch {}
  }
  console.error('✗ 找不到 playwright。设置 RECON_PLAYWRIGHT=<播放wright 包目录> 后重试。')
  process.exit(1)
}

const configPath = process.argv[2]
if (!configPath) { console.error('用法: node collect.mjs <OUTDIR>/config.json'); process.exit(1) }
const cfg = JSON.parse(readFileSync(configPath, 'utf8'))
const outdir = resolve(cfg.outdir || 'recon')
mkdirSync(outdir, { recursive: true })

// routes 未写就从 harvest 的 routes.txt 取
let routes = cfg.routes
if (!routes && existsSync(join(outdir, 'routes.txt'))) {
  routes = readFileSync(join(outdir, 'routes.txt'), 'utf8').split('\n').map((s) => s.trim()).filter(Boolean).slice(0, cfg.maxRoutes || 60)
}
routes = routes || []

const injected = `window.__RECON_CONFIG__ = ${JSON.stringify(cfg)};\n` + readFileSync(join(here, 'preload.js'), 'utf8')

const { chromium } = await loadPlaywright()
const exe = process.env.RECON_CHROMIUM || cfg.chromium || '/usr/bin/chromium'
const launchOpts = existsSync(exe) ? { executablePath: exe } : {}
const browser = await chromium.launch({ ...launchOpts, args: ['--no-sandbox'] })
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 } })
await context.addInitScript({ content: injected })
const page = await context.newPage()

const errors = []
page.on('pageerror', (e) => errors.push(String(e && e.message).slice(0, 200)))
const seenLogin = { hit: false }
page.on('framenavigated', (f) => { if (f === page.mainFrame() && /\/login/i.test(f.url())) seenLogin.hit = true })

const waitMs = Number(cfg.waitMs || 1500)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const base = String(cfg.baseUrl || '').replace(/\/$/, '')

async function tryClicks () {
  for (const sel of cfg.clicks || []) {
    try {
      const loc = page.locator(sel)
      const n = Math.min(await loc.count(), cfg.maxClicksPerSelector || 4)
      for (let i = 0; i < n; i++) {
        try { await loc.nth(i).click({ timeout: 1200 }); await sleep(400) } catch {}
      }
    } catch {}
  }
}

console.log(`▶ ${base}  mode=${cfg.mode || 'observe'}  routes=${routes.length}  stubs=${(cfg.stubs || []).length}`)
await page.goto(base, { waitUntil: 'domcontentloaded', timeout: 30000 }).catch((e) => console.error('  goto 失败:', e.message.split('\n')[0]))
await sleep(waitMs)
await tryClicks()

for (const route of routes) {
  const target = route.startsWith('http') ? route : base + (route.startsWith('/') ? route : '/' + route)
  const mode = cfg.routeMode || 'history'
  try {
    if (mode === 'goto') await page.goto(target, { waitUntil: 'domcontentloaded', timeout: 20000 })
    else if (mode === 'hash') await page.evaluate((r) => { location.hash = r }, route)
    else await page.evaluate((r) => { history.pushState({}, '', r); dispatchEvent(new PopStateEvent('popstate')) }, route)
    await sleep(Number(cfg.routeWaitMs || waitMs))
    await tryClicks()
  } catch (e) { console.error(`  route ${route}: ${String(e.message).split('\n')[0]}`) }
}

const dump = await page.evaluate(() => ({
  log: window.__RECON__.log,
  stats: window.__RECON__.stats,
  unmatched: window.__RECON__.unmatched,
})).catch(() => ({ log: [], stats: {}, unmatched: [] }))

await browser.close()

// ── 产出 ────────────────────────────────────────────────────────────────────
const log = dump.log || []
const apiPrefixes = cfg.apiPrefix || ['/api/']
const isApiPath = (p) => apiPrefixes.some((x) => p.includes(x))
const byKey = new Map()
for (const e of log) {
  if (e.decision === 'native') continue        // 完全透传的非目标请求不计
  if (!isApiPath(e.path)) continue             // 只收 API 面（observe 模式也照收）
  const key = `${e.method} ${e.path.split('?')[0]}`
  if (!byKey.has(key)) byKey.set(key, { method: e.method, path: e.path.split('?')[0], count: 0, decisions: {}, queries: [], bodies: [] })
  const rec = byKey.get(key)
  rec.count++
  rec.decisions[e.decision] = (rec.decisions[e.decision] || 0) + 1
  const q = e.path.includes('?') ? e.path.split('?')[1] : undefined
  if (q && !rec.queries.includes(q)) rec.queries.push(q)
  if (e.body && !rec.bodies.includes(e.body)) rec.bodies.push(String(e.body).slice(0, 2000))
}
const runtimeApi = [...byKey.values()]
const paramSamples = Object.fromEntries(runtimeApi.filter((r) => r.bodies.length).map((r) => [`${r.method} ${r.path}`, r.bodies.map((b) => ({ body: b }))]))

const write = (name, data) => { writeFileSync(join(outdir, name), typeof data === 'string' ? data : JSON.stringify(data, null, 2)); }
write('raw_log.json', log)
write('runtime_api.json', runtimeApi)
write('param_samples.json', paramSamples)
write('unmatched.json', dump.unmatched || [])
write('runtime_api.txt', runtimeApi.map((r) => `${r.method} ${r.path}${r.queries.length ? ' ?' + r.queries[0] : ''}`).join('\n'))

console.log(`\n✅ 采集完成 → ${outdir}`)
console.log(`   请求总数 ${log.length} | stub ${dump.stats.stub || 0} | fallback ${dump.stats.fallback || 0} | allow ${dump.stats.allow || 0} | 负向修正 ${dump.stats.corrected || 0}`)
console.log(`   API 面 ${runtimeApi.length} 条 → runtime_api.txt | 带 body 的 ${Object.keys(paramSamples).length} 条 → param_samples.json`)
if (seenLogin.hit) console.log('   ⚠ 期间跳转过 /login —— 渲染门/拦截器门没对，回 reference.md A1/A2 补 stub')
if (errors.length) console.log(`   ⚠ 页面 JS 报错 ${errors.length} 条（前 3：${errors.slice(0, 3).join(' | ')}）`)
const unmatched = (dump.unmatched || []).length
if (unmatched) console.log(`   ⚠ 未命中规则的请求 ${unmatched} 条 → unmatched.json（判断要不要加进 allow）`)

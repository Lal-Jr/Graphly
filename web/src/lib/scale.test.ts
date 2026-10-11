import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { forecast, parseDate, toISODate } from './forecast'
import { analyze } from './graph'
import { hash, simulateConfidence, spreadFrom } from './montecarlo'
import { synthetic } from './synthetic'

const time = <T,>(fn: () => T): [T, number] => {
  const t = performance.now()
  const out = fn()
  return [out, performance.now() - t]
}

/**
 * The board, graph and insights all re-run the engine on every change, so it has to stay fast on
 * big projects. Budgets are generous (CI machines are slow); the point is to catch an accidental
 * quadratic, not to benchmark. Timings are printed for the README's numbers.
 */
describe('engine at scale', () => {
  const plan = synthetic(5000)
  const due = new Map(Object.entries(plan.dueDates))
  const today = parseDate('2026-01-05')

  it('analyzes, schedules and simulates 5,000 issues', () => {
    const links = plan.nodes.reduce((s, n) => s + n.dependsOn.length, 0)
    const [a, tAnalyze] = time(() => analyze(plan.nodes))
    const [, tForecast] = time(() => forecast(a, due, today))
    const [c, tSim] = time(() => simulateConfidence(plan.nodes, a, due, spreadFrom((id) => plan.types[id]), 1000, today, 1))
    console.log(
      `5000 issues, ${links} links: analyze ${tAnalyze.toFixed(0)}ms, forecast ${tForecast.toFixed(0)}ms, 1000-run simulation ${tSim.toFixed(0)}ms`,
    )
    expect(a.inCycle.size).toBeGreaterThan(0)
    expect(a.remaining).toBeGreaterThan(0)
    expect(c.p85).toBeGreaterThanOrEqual(c.p50)
    expect(tAnalyze).toBeLessThan(1500)
    expect(tForecast).toBeLessThan(1500)
    expect(tSim).toBeLessThan(6000)
  })
})

describe('impact', () => {
  it('matches a per-task DFS, cycles included', () => {
    for (const seed of [1, 2, 3]) {
      const { nodes } = synthetic(1500, seed)
      // Plant extra cycles, including a self-loop, among open tasks.
      nodes[10].dependsOn.push(nodes[10].id)
      nodes[200].dependsOn.push(nodes[1400].id)
      const a = analyze(nodes)
      for (const [id] of a.dependents) {
        const seen = new Set<string>()
        const stack = [...a.dependents.get(id)!]
        while (stack.length) {
          const cur = stack.pop()!
          if (seen.has(cur) || cur === id) continue
          seen.add(cur)
          stack.push(...(a.dependents.get(cur) ?? []))
        }
        expect(a.impact.get(id), id).toBe(seen.size)
      }
      expect([...a.impact.keys()]).toEqual([...a.dependents.keys()])
    }
  })
})

/**
 * The 5,000-issue plan is too big to keep as a fixture, so both engines generate it from the seed
 * and compare a summary: fixtures/forecast-scale/synthetic-5k.json (checked by
 * server/forecast/scale_test.go too). Regenerate with UPDATE_FIXTURES=1 npm test.
 */
export function scaleSummary() {
  const plan = synthetic(5000)
  const due = new Map(Object.entries(plan.dueDates))
  const today = parseDate('2026-01-05')
  const a = analyze(plan.nodes)
  const fc = forecast(a, due, today)
  const c = simulateConfidence(plan.nodes, a, due, spreadFrom((id) => plan.types[id]), 200, today, 7)
  const order = [...a.schedule.keys()]
  let impactSum = 0
  for (const v of a.impact.values()) impactSum += v
  return {
    links: plan.nodes.reduce((s, n) => s + n.dependsOn.length, 0),
    remaining: a.remaining,
    orderHash: hash(order.join(',')),
    criticalPath: a.criticalPath,
    inCycle: [...a.inCycle].sort(),
    topBlockers: a.topBlockers.slice(0, 25),
    impactSum,
    ready: a.ready.length,
    atRisk: a.atRisk.length,
    finishHash: hash(order.map((id) => toISODate(fc.get(id)!.finish) + fc.get(id)!.daysLate).join(',')),
    confidence: { p50: c.p50, p85: c.p85, p95: c.p95, histogram: c.histogram, onTimeHash: hash([...c.onTime].map(([id, v]) => id + v).join(',')) },
  }
}

describe('engine parity at scale', () => {
  it('matches fixtures/forecast-scale/synthetic-5k.json', () => {
    const path = join(__dirname, '../../../fixtures/forecast-scale/synthetic-5k.json')
    const got = scaleSummary()
    if (process.env.UPDATE_FIXTURES || !existsSync(path)) {
      writeFileSync(path, JSON.stringify(got, null, 1) + '\n')
      return
    }
    expect(got).toEqual(JSON.parse(readFileSync(path, 'utf8')))
  })
})

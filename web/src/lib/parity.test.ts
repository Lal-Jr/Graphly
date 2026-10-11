import { readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { forecast, parseDate, toISODate } from './forecast'
import { analyze, type GraphNode } from './graph'
import { DEFAULT_SPREADS, simulateConfidence, spreadFrom } from './montecarlo'

/**
 * fixtures/forecast holds test vectors shared with the Go engine (server/forecast). Both sides must
 * reproduce `expect` exactly, so the server's history and alerts agree with what the browser shows.
 * After an intentional engine change, regenerate with `UPDATE_FIXTURES=1 npm test` and check the Go
 * side still passes.
 */
const DIR = join(__dirname, '../../../fixtures/forecast')

interface Fixture {
  name: string
  today: string
  runs: number
  seed?: number
  nodes: GraphNode[]
  dueDates: Record<string, string>
  types: Record<string, string>
  spreads?: Record<string, [number, number]>
  expect?: unknown
}

export function evaluate(f: Fixture) {
  const today = parseDate(f.today)
  const dueDates = new Map(Object.entries(f.dueDates))
  const a = analyze(f.nodes)
  const fc = forecast(a, dueDates, today)
  const spread = spreadFrom((id) => f.types[id], { ...DEFAULT_SPREADS, ...f.spreads })
  const c = simulateConfidence(f.nodes, a, dueDates, spread, f.runs, today, f.seed)
  const obj = <V,>(m: Map<string, V>) => Object.fromEntries([...m].sort(([x], [y]) => (x < y ? -1 : 1)))
  return {
    analysis: {
      remaining: a.remaining,
      order: [...a.schedule.keys()],
      criticalPath: a.criticalPath,
      inCycle: [...a.inCycle].sort(),
      ready: a.ready,
      atRisk: a.atRisk,
      blockedBy: obj(a.blockedBy),
      topBlockers: a.topBlockers,
      schedule: obj(a.schedule),
    },
    forecast: obj(new Map([...fc].map(([id, x]) => [id, { start: toISODate(x.start), finish: toISODate(x.finish), daysLate: x.daysLate }]))),
    confidence: { seed: c.seed, p50: c.p50, p85: c.p85, p95: c.p95, histogram: c.histogram, onTime: obj(c.onTime), p85ById: obj(c.p85ById) },
  }
}

describe('shared forecast fixtures', () => {
  const files = readdirSync(DIR).filter((f) => f.endsWith('.json'))
  it('has fixtures', () => expect(files.length).toBeGreaterThan(0))
  for (const file of files) {
    it(file, () => {
      const path = join(DIR, file)
      const f = JSON.parse(readFileSync(path, 'utf8')) as Fixture
      const got = evaluate(f)
      if (process.env.UPDATE_FIXTURES) {
        writeFileSync(path, JSON.stringify({ ...f, expect: got }, null, 1) + '\n')
        return
      }
      expect(got).toEqual(f.expect)
    })
  }
})

import type { Analysis, GraphNode } from './graph'
import { nextWorkday, parseDate, workdaysBetween } from './forecast'

/**
 * Single-point estimates are optimistic by construction, so a date computed from them is a
 * coin flip at best. This simulates the plan many times, sampling each task's duration from a
 * skewed (triangular) distribution around its estimate, and reports what fraction of runs finish
 * by a given day — "85% likely by Oct 17" instead of one date nobody believes.
 */
export interface Confidence {
  /** Remaining working days by percentile of simulated runs. */
  p50: number
  p85: number
  p95: number
  /** Probability (0–1) each dated open task finishes on or before its due date. */
  onTime: Map<string, number>
  /** 85th-percentile finish (working days from today) per task. */
  p85ById: Map<string, number>
  runs: number
}

/** Low and high multipliers on the estimate. Work tends to overrun, bugs most of all. */
export type Spread = (id: string) => [number, number]

export const defaultSpread =
  (typeOf: (id: string) => string | undefined): Spread =>
  (id) => {
    switch (typeOf(id)) {
      case 'bug':
        return [0.6, 2.5]
      case 'story':
        return [0.8, 1.8]
      default:
        return [0.8, 1.6]
    }
  }

/** Small, fast, seedable PRNG so the same plan always yields the same numbers (no jitter on re-render). */
function mulberry32(seed: number) {
  return () => {
    seed |= 0
    seed = (seed + 0x6d2b79f5) | 0
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function hash(s: string) {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619)
  return h >>> 0
}

/** Samples a triangular distribution on [lo, hi] with the given mode. */
function triangular(rand: () => number, lo: number, mode: number, hi: number) {
  if (hi <= lo) return mode
  const u = rand()
  const c = (mode - lo) / (hi - lo)
  return u < c ? lo + Math.sqrt(u * (hi - lo) * (mode - lo)) : hi - Math.sqrt((1 - u) * (hi - lo) * (hi - mode))
}

const percentile = (sorted: number[], p: number) => sorted[Math.min(sorted.length - 1, Math.floor(p * sorted.length))]

export function simulateConfidence(
  nodes: GraphNode[],
  analysis: Analysis,
  dueDates: Map<string, string | null>,
  spread: Spread,
  runs = 1000,
  today = new Date(),
): Confidence {
  // analysis.schedule is keyed in topological order over the open, acyclic tasks.
  const order = [...analysis.schedule.keys()]
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const index = new Map(order.map((id, k) => [id, k]))
  const preds = order.map((id) => byId.get(id)!.dependsOn.filter((d) => index.has(d)).map((d) => index.get(d)!))
  const est = order.map((id) => Math.max(0, byId.get(id)!.estimate || 0))
  const spreads = order.map((id) => spread(id))

  const origin = nextWorkday(today)
  const dueCol = order.map((id) => {
    const due = dueDates.get(id)
    return due ? workdaysBetween(origin, parseDate(due)) : -1
  })

  const rand = mulberry32(hash(order.join() + est.join()))
  const finishes = new Float64Array(order.length)
  const totals: number[] = []
  const perTask = order.map(() => [] as number[])
  const hits = new Array(order.length).fill(0)

  for (let r = 0; r < runs; r++) {
    let total = 0
    for (let k = 0; k < order.length; k++) {
      let start = 0
      for (const p of preds[k]) if (finishes[p] > start) start = finishes[p]
      const [lo, hi] = spreads[k]
      const d = est[k] === 0 ? 0 : triangular(rand, est[k] * lo, est[k], est[k] * hi)
      const f = start + d
      finishes[k] = f
      perTask[k].push(f)
      if (f > total) total = f
      // Finishing within working day `dueCol` means ceil(f) - 1 <= dueCol.
      if (dueCol[k] >= 0 && Math.ceil(f) - 1 <= dueCol[k]) hits[k]++
    }
    totals.push(total)
  }

  totals.sort((a, b) => a - b)
  const onTime = new Map<string, number>()
  const p85ById = new Map<string, number>()
  order.forEach((id, k) => {
    if (dueCol[k] >= 0) onTime.set(id, hits[k] / runs)
    p85ById.set(id, percentile(perTask[k].sort((a, b) => a - b), 0.85))
  })
  return { p50: percentile(totals, 0.5), p85: percentile(totals, 0.85), p95: percentile(totals, 0.95), onTime, p85ById, runs }
}

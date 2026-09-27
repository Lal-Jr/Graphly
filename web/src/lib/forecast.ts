import { analyze, type Analysis, type GraphNode } from './graph'

const DAY = 86_400_000

export const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate())
const isWeekend = (d: Date) => d.getDay() === 0 || d.getDay() === 6

/** The first working day on or after `d`. */
export function nextWorkday(d: Date): Date {
  let x = startOfDay(d)
  while (isWeekend(x)) x = new Date(x.getTime() + DAY)
  return x
}

/** Moves `n` working days forward from a working day. */
export function addWorkdays(d: Date, n: number): Date {
  let x = nextWorkday(d)
  for (let i = 0; i < n; ) {
    x = new Date(x.getFullYear(), x.getMonth(), x.getDate() + 1)
    if (!isWeekend(x)) i++
  }
  return x
}

/** Parses a YYYY-MM-DD date as local midnight. */
export function parseDate(s: string): Date {
  const [y, m, d] = s.split('-').map(Number)
  return new Date(y, m - 1, d)
}

export function formatDate(s: string | Date) {
  const d = typeof s === 'string' ? parseDate(s) : s
  const sameYear = d.getFullYear() === new Date().getFullYear()
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', ...(sameYear ? {} : { year: 'numeric' }) })
}

export function toISODate(d: Date) {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

export interface Forecast {
  start: Date
  finish: Date
  /** Working days past the due date the forecast lands (0 when on time or undated). */
  daysLate: number
}

/**
 * Converts the critical-path schedule (in working days from now) into calendar dates.
 * Work is assumed to start today; a task with earliest finish `ef` completes at the end of
 * working day ceil(ef), so a 1-day task starting today finishes today.
 */
export function forecast(analysis: Analysis, dueDates: Map<string, string | null>, today = new Date()): Map<string, Forecast> {
  const origin = nextWorkday(today)
  const out = new Map<string, Forecast>()
  for (const [id, s] of analysis.schedule) {
    const start = addWorkdays(origin, Math.floor(s.earliestStart))
    const finish = addWorkdays(origin, Math.max(0, Math.ceil(s.earliestFinish) - 1))
    const due = dueDates.get(id)
    out.set(id, { start, finish, daysLate: due ? workdaysBetween(parseDate(due), finish) : 0 })
  }
  return out
}

/** Working days from `a` to `b` (positive when b is later), or 0 if b is not after a. */
export function workdaysBetween(a: Date, b: Date): number {
  let n = 0
  for (let x = startOfDay(a); x < startOfDay(b); x = new Date(x.getFullYear(), x.getMonth(), x.getDate() + 1)) {
    const next = new Date(x.getFullYear(), x.getMonth(), x.getDate() + 1)
    if (!isWeekend(next)) n++
  }
  return n
}

export interface Simulation {
  before: number
  after: number
  /** Tasks whose forecast finish moves because of the slip. */
  delayed: { id: string; by: number }[]
  /** Tasks that were on time and would now miss their due date. */
  newlyLate: string[]
}

/** What happens to the plan if `id` takes `slip` more days than estimated? */
export function simulateSlip(nodes: GraphNode[], dueDates: Map<string, string | null>, id: string, slip: number, today = new Date()): Simulation {
  const base = analyze(nodes)
  const slipped = analyze(nodes.map((n) => (n.id === id ? { ...n, estimate: n.estimate + slip } : n)))
  const fBase = forecast(base, dueDates, today)
  const fSlip = forecast(slipped, dueDates, today)
  const delayed: Simulation['delayed'] = []
  const newlyLate: string[] = []
  for (const [nid, s] of slipped.schedule) {
    const b = base.schedule.get(nid)
    if (!b) continue
    const by = s.earliestFinish - b.earliestFinish
    if (by > 1e-9) delayed.push({ id: nid, by })
    if ((fBase.get(nid)?.daysLate ?? 0) === 0 && (fSlip.get(nid)?.daysLate ?? 0) > 0) newlyLate.push(nid)
  }
  delayed.sort((a, b) => b.by - a.by)
  return { before: base.remaining, after: slipped.remaining, delayed, newlyLate }
}

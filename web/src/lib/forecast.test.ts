import { describe, expect, it } from 'vitest'
import { addWorkdays, forecast, simulateSlip, toISODate, workdaysBetween } from './forecast'
import { analyze, type GraphNode } from './graph'

// Monday 6 October 2025
const MON = new Date(2025, 9, 6)
const node = (id: string, estimate: number, dependsOn: string[] = []): GraphNode => ({ id, estimate, state: 'todo', dependsOn })

describe('working days', () => {
  it('skips weekends', () => {
    expect(toISODate(addWorkdays(MON, 4))).toBe('2025-10-10')
    expect(toISODate(addWorkdays(MON, 5))).toBe('2025-10-13')
    expect(toISODate(addWorkdays(new Date(2025, 9, 11), 0))).toBe('2025-10-13') // Saturday → Monday
  })
  it('counts days between', () => {
    expect(workdaysBetween(new Date(2025, 9, 10), new Date(2025, 9, 13))).toBe(1)
    expect(workdaysBetween(new Date(2025, 9, 13), new Date(2025, 9, 10))).toBe(0)
  })
})

describe('forecast', () => {
  const nodes = [node('a', 1), node('b', 3, ['a'])]
  it('lays tasks out in calendar days from today', () => {
    const f = forecast(analyze(nodes), new Map(), MON)
    expect(toISODate(f.get('a')!.finish)).toBe('2025-10-06')
    expect(toISODate(f.get('b')!.start)).toBe('2025-10-07')
    expect(toISODate(f.get('b')!.finish)).toBe('2025-10-09')
  })
  it('flags late work against due dates', () => {
    const f = forecast(analyze(nodes), new Map([['b', '2025-10-08']]), MON)
    expect(f.get('b')!.daysLate).toBe(1)
  })
})

describe('simulateSlip', () => {
  it('reports the knock-on effect of a slip', () => {
    const nodes = [node('a', 2), node('b', 1, ['a']), node('c', 5)]
    const sim = simulateSlip(nodes, new Map([['b', '2025-10-08']]), 'a', 2, MON)
    expect(sim.before).toBe(5)
    expect(sim.after).toBe(5) // c still dominates
    expect(sim.delayed.map((d) => d.id)).toEqual(['a', 'b'])
    expect(sim.newlyLate).toEqual(['b'])
  })
})

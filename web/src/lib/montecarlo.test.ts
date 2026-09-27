import { describe, expect, it } from 'vitest'
import { analyze, type GraphNode } from './graph'
import { simulateConfidence } from './montecarlo'

const MON = new Date(2025, 9, 6)
const node = (id: string, estimate: number, dependsOn: string[] = []): GraphNode => ({ id, estimate, state: 'todo', dependsOn })

describe('simulateConfidence', () => {
  const nodes = [node('a', 4), node('b', 4, ['a']), node('c', 1)]
  const a = analyze(nodes)

  it('is never more optimistic than the point estimate when work only overruns', () => {
    const c = simulateConfidence(nodes, a, new Map(), () => [1, 2], 500, MON)
    expect(c.p50).toBeGreaterThanOrEqual(a.remaining)
    expect(c.p85).toBeGreaterThanOrEqual(c.p50)
    expect(c.p95).toBeGreaterThanOrEqual(c.p85)
  })

  it('collapses to the point estimate with no uncertainty', () => {
    const c = simulateConfidence(nodes, a, new Map(), () => [1, 1], 50, MON)
    expect(c.p50).toBe(8)
    expect(c.p95).toBe(8)
  })

  it('gives the probability of hitting a due date', () => {
    // b needs 8 days at best; due in 5 working days is impossible, due in 30 is certain.
    const tight = simulateConfidence(nodes, a, new Map([['b', '2025-10-10']]), () => [1, 1.5], 300, MON)
    const loose = simulateConfidence(nodes, a, new Map([['b', '2025-11-14']]), () => [1, 1.5], 300, MON)
    expect(tight.onTime.get('b')).toBe(0)
    expect(loose.onTime.get('b')).toBe(1)
  })

  it('buckets every run into a finish-day histogram', () => {
    const c = simulateConfidence(nodes, a, new Map(), () => [1, 2], 400, MON)
    expect(c.histogram.reduce((s, n) => s + n, 0)).toBe(400)
    // Nothing can finish before the point estimate when work only overruns.
    expect(c.histogram.slice(0, 8).every((n) => n === 0)).toBe(true)
    expect(simulateConfidence(nodes, a, new Map(), () => [1, 1], 50, MON).histogram[8]).toBe(50)
  })

  it('is deterministic for the same plan', () => {
    const spread = () => [0.8, 1.8] as [number, number]
    expect(simulateConfidence(nodes, a, new Map(), spread, 200, MON).p85).toBe(simulateConfidence(nodes, a, new Map(), spread, 200, MON).p85)
  })
})

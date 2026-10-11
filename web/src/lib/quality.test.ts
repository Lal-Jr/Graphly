import { describe, expect, it } from 'vitest'
import { estimateQuality } from './quality'

const plan = (open: number, unestimated: number) => Array.from({ length: open }, (_, i) => ({ estimate: i < unestimated ? 0 : 1 }))

describe('estimateQuality', () => {
  it('degrades as more open work is unestimated (same thresholds as the server)', () => {
    expect(estimateQuality(plan(20, 1)).confidence).toBe('high')
    expect(estimateQuality(plan(20, 2)).confidence).toBe('medium')
    expect(estimateQuality(plan(20, 5)).confidence).toBe('medium')
    expect(estimateQuality(plan(20, 6)).confidence).toBe('low')
    expect(estimateQuality([])).toEqual({ openIssues: 0, unestimated: 0, share: 0, confidence: 'high' })
  })
  it('counts zero and missing estimates', () => {
    expect(estimateQuality([{ estimate: 0 }, { estimate: NaN }, { estimate: 0.5 }]).unestimated).toBe(2)
  })
})

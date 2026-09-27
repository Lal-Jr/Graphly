import { describe, expect, it } from 'vitest'
import { analyze, findCycles, wouldCreateCycle } from './graph'
import type { Task } from './types'

let n = 0
const task = (id: string, dependsOn: string[] = [], estimate = 1, status: Task['status'] = 'todo'): Task => ({
  id,
  title: id,
  description: '',
  status,
  priority: 'medium',
  assignee: '',
  estimate,
  dependsOn,
  order: n++,
  createdAt: 0,
})

describe('wouldCreateCycle', () => {
  const tasks = [task('a'), task('b', ['a']), task('c', ['b'])]
  it('rejects self dependencies', () => expect(wouldCreateCycle(tasks, 'a', 'a')).toBe(true))
  it('rejects transitive back edges', () => expect(wouldCreateCycle(tasks, 'a', 'c')).toBe(true))
  it('allows forward edges', () => expect(wouldCreateCycle(tasks, 'c', 'a')).toBe(false))
})

describe('findCycles', () => {
  it('finds tasks in a cycle but not ones hanging off it', () => {
    const cycles = findCycles([task('a', ['c']), task('b', ['a']), task('c', ['b']), task('d', ['c'])])
    expect([...cycles].sort()).toEqual(['a', 'b', 'c'])
  })
  it('detects self loops', () => expect([...findCycles([task('a', ['a'])])]).toEqual(['a']))
  it('returns nothing for a DAG', () => expect(findCycles([task('a'), task('b', ['a'])]).size).toBe(0))
})

describe('analyze', () => {
  //   a(2) ─► b(3) ─► d(1)
  //   a(2) ─► c(1) ─┘
  const tasks = [task('a', [], 2), task('b', ['a'], 3), task('c', ['a'], 1), task('d', ['b', 'c'], 1)]

  it('computes the critical path and remaining duration', () => {
    const r = analyze(tasks)
    expect(r.criticalPath).toEqual(['a', 'b', 'd'])
    expect(r.remaining).toBe(6)
    expect(r.schedule.get('c')!.slack).toBe(2)
    expect(r.schedule.get('b')!.slack).toBe(0)
  })

  it('reports blockers with transitive impact', () => {
    const r = analyze(tasks)
    expect(r.blockedBy.get('d')).toEqual(['b', 'c'])
    expect(r.impact.get('a')).toBe(3)
    expect(r.topBlockers[0]).toEqual({ id: 'a', impact: 3 })
    expect(r.ready).toEqual(['a'])
  })

  it('treats done dependencies as satisfied', () => {
    const r = analyze([task('a', [], 2, 'done'), task('b', ['a'], 3)])
    expect(r.blockedBy.has('b')).toBe(false)
    expect(r.ready).toEqual(['b'])
    expect(r.remaining).toBe(3)
  })

  it('flags work started on blocked tasks', () => {
    const r = analyze([task('a'), task('b', ['a'], 1, 'in_progress')])
    expect(r.atRisk).toEqual(['b'])
  })

  it('excludes cyclic tasks from scheduling', () => {
    const r = analyze([task('a', ['b']), task('b', ['a']), task('c', [], 4)])
    expect([...r.inCycle].sort()).toEqual(['a', 'b'])
    expect(r.criticalPath).toEqual(['c'])
    expect(r.remaining).toBe(4)
  })

  it('ignores dangling dependency ids', () => {
    const r = analyze([task('a', ['ghost'])])
    expect(r.blockedBy.size).toBe(0)
  })
})

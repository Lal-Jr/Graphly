import type { GraphNode } from './graph'
import { mulberry32 } from './montecarlo'

/**
 * A deterministic synthetic project for scale tests: `n` issues, each depending on up to seven
 * earlier ones (about 3.5 links per issue), a fifth of them done, a tenth with due dates, and a
 * few planted cycles. server/forecast/synthetic.go generates the identical plan from the same
 * seed, so both engines can be measured, and compared, on it.
 */
export interface SyntheticPlan {
  nodes: GraphNode[]
  dueDates: Record<string, string>
  types: Record<string, string>
}

const ESTIMATES = [0, 0.5, 1, 2, 3, 5]
const TYPES = ['task', 'story', 'bug']

export function synthetic(n: number, seed = 1): SyntheticPlan {
  const rand = mulberry32(seed)
  const pick = (k: number) => Math.floor(rand() * k)
  const nodes: GraphNode[] = []
  const dueDates: Record<string, string> = {}
  const types: Record<string, string> = {}
  for (let i = 0; i < n; i++) {
    const id = `s${i}`
    const estimate = ESTIMATES[pick(ESTIMATES.length)]
    const r = rand()
    const state = r < 0.2 ? 'done' : r < 0.35 ? 'in_progress' : 'todo'
    const dependsOn: string[] = []
    const links = i === 0 ? 0 : pick(8)
    for (let k = 0; k < links; k++) {
      // Mostly recent issues, sometimes anything earlier: long chains plus wide fan-in.
      const j = rand() < 0.7 ? Math.max(0, i - 1 - pick(40)) : pick(i)
      const dep = `s${j}`
      if (!dependsOn.includes(dep)) dependsOn.push(dep)
    }
    nodes.push({ id, estimate, state, dependsOn })
    types[id] = TYPES[pick(TYPES.length)]
    if (rand() < 0.1) {
      const d = new Date(Date.UTC(2026, 0, 5 + pick(400)))
      dueDates[id] = d.toISOString().slice(0, 10)
    }
  }
  // Plant a cycle every thousand issues: the first dependency of issue i also depends on i.
  for (let i = 999; i < n; i += 1000) {
    const dep = nodes[i].dependsOn[0]
    if (dep) nodes[Number(dep.slice(1))].dependsOn.push(nodes[i].id)
  }
  return { nodes, dueDates, types }
}

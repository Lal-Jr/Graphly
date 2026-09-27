/** The minimum the engine needs to know about a unit of work. */
export interface GraphNode {
  id: string
  /** Estimated effort in days. */
  estimate: number
  state: 'todo' | 'in_progress' | 'done'
  /** Ids of nodes that must be done before this one can start. */
  dependsOn: string[]
}

type Task = GraphNode

export interface ScheduleEntry {
  earliestStart: number
  earliestFinish: number
  latestStart: number
  latestFinish: number
  slack: number
}

export interface Analysis {
  /** Unfinished direct dependencies of each unfinished task (only tasks that have some). */
  blockedBy: Map<string, string[]>
  /** Unfinished tasks that directly depend on each task. */
  dependents: Map<string, string[]>
  /** Number of unfinished tasks transitively waiting on each unfinished task. */
  impact: Map<string, number>
  /** Unfinished tasks sorted by how much work they hold up. */
  topBlockers: { id: string; impact: number }[]
  /** Tasks being worked on (or reviewed) while a dependency is still open. */
  atRisk: string[]
  /** Unblocked tasks that haven't started yet. */
  ready: string[]
  /** Task ids that are part of a dependency cycle. */
  inCycle: Set<string>
  /** Critical path schedule for unfinished, acyclic tasks. */
  schedule: Map<string, ScheduleEntry>
  criticalPath: string[]
  criticalSet: Set<string>
  /** Remaining project duration (days) along the critical path. */
  remaining: number
}

const isOpen = (t: Task) => t.state !== 'done'

/** True if making `taskId` depend on `depId` would introduce a cycle. */
export function wouldCreateCycle(tasks: GraphNode[], taskId: string, depId: string): boolean {
  if (taskId === depId) return true
  const byId = new Map(tasks.map((t) => [t.id, t]))
  // A cycle appears if `depId` already (transitively) depends on `taskId`.
  const stack = [depId]
  const seen = new Set<string>()
  while (stack.length) {
    const id = stack.pop()!
    if (id === taskId) return true
    if (seen.has(id)) continue
    seen.add(id)
    for (const d of byId.get(id)?.dependsOn ?? []) stack.push(d)
  }
  return false
}

/** Tarjan's SCC — concurrent edits can merge into a cycle even though the UI prevents creating one. */
export function findCycles(tasks: GraphNode[]): Set<string> {
  const byId = new Map(tasks.map((t) => [t.id, t]))
  const index = new Map<string, number>()
  const low = new Map<string, number>()
  const onStack = new Set<string>()
  const stack: string[] = []
  const result = new Set<string>()
  let counter = 0

  const visit = (id: string) => {
    index.set(id, counter)
    low.set(id, counter)
    counter++
    stack.push(id)
    onStack.add(id)
    for (const dep of byId.get(id)!.dependsOn) {
      if (!byId.has(dep)) continue
      if (!index.has(dep)) {
        visit(dep)
        low.set(id, Math.min(low.get(id)!, low.get(dep)!))
      } else if (onStack.has(dep)) {
        low.set(id, Math.min(low.get(id)!, index.get(dep)!))
      }
    }
    if (low.get(id) === index.get(id)) {
      const component: string[] = []
      let w: string
      do {
        w = stack.pop()!
        onStack.delete(w)
        component.push(w)
      } while (w !== id)
      const selfLoop = byId.get(id)!.dependsOn.includes(id)
      if (component.length > 1 || selfLoop) component.forEach((c) => result.add(c))
    }
  }

  for (const t of tasks) if (!index.has(t.id)) visit(t.id)
  return result
}

export function analyze(tasks: GraphNode[]): Analysis {
  const byId = new Map(tasks.map((t) => [t.id, t]))
  const open = tasks.filter(isOpen)
  const inCycle = findCycles(tasks)

  // Only dependencies on existing, unfinished tasks count.
  const openDeps = (t: Task) => t.dependsOn.filter((d) => byId.has(d) && isOpen(byId.get(d)!))

  const blockedBy = new Map<string, string[]>()
  const dependents = new Map<string, string[]>()
  for (const t of open) {
    const deps = openDeps(t)
    if (deps.length) blockedBy.set(t.id, deps)
    for (const d of deps) {
      if (!dependents.has(d)) dependents.set(d, [])
      dependents.get(d)!.push(t.id)
    }
  }

  const impact = new Map<string, number>()
  for (const id of dependents.keys()) {
    const seen = new Set<string>()
    const stack = [...dependents.get(id)!]
    while (stack.length) {
      const cur = stack.pop()!
      if (seen.has(cur) || cur === id) continue
      seen.add(cur)
      stack.push(...(dependents.get(cur) ?? []))
    }
    impact.set(id, seen.size)
  }
  const topBlockers = [...impact.entries()]
    .map(([id, n]) => ({ id, impact: n }))
    .sort((a, b) => b.impact - a.impact)

  const atRisk = open
    .filter((t) => t.state === 'in_progress' && blockedBy.has(t.id))
    .map((t) => t.id)
  const ready = open
    .filter((t) => t.state === 'todo' && !blockedBy.has(t.id))
    .map((t) => t.id)

  // Critical path method over the open, acyclic subgraph.
  const nodes = open.filter((t) => !inCycle.has(t.id))
  const nodeIds = new Set(nodes.map((t) => t.id))
  const preds = new Map(nodes.map((t) => [t.id, openDeps(t).filter((d) => nodeIds.has(d))]))
  const succs = new Map<string, string[]>(nodes.map((t) => [t.id, []]))
  for (const [id, ps] of preds) for (const p of ps) succs.get(p)!.push(id)

  // Kahn topological order.
  const indeg = new Map(nodes.map((t) => [t.id, preds.get(t.id)!.length]))
  const queue = nodes.filter((t) => indeg.get(t.id) === 0).map((t) => t.id)
  const order: string[] = []
  while (queue.length) {
    const id = queue.shift()!
    order.push(id)
    for (const s of succs.get(id)!) {
      indeg.set(s, indeg.get(s)! - 1)
      if (indeg.get(s) === 0) queue.push(s)
    }
  }

  const dur = (id: string) => Math.max(0, byId.get(id)!.estimate || 0)
  const es = new Map<string, number>()
  const ef = new Map<string, number>()
  for (const id of order) {
    const start = Math.max(0, ...preds.get(id)!.map((p) => ef.get(p)!))
    es.set(id, start)
    ef.set(id, start + dur(id))
  }
  const remaining = Math.max(0, ...order.map((id) => ef.get(id)!))

  const lf = new Map<string, number>()
  const ls = new Map<string, number>()
  for (const id of [...order].reverse()) {
    const finish = Math.min(remaining, ...succs.get(id)!.map((s) => ls.get(s)!))
    lf.set(id, finish)
    ls.set(id, finish - dur(id))
  }

  const EPS = 1e-9
  const schedule = new Map<string, ScheduleEntry>()
  for (const id of order) {
    schedule.set(id, {
      earliestStart: es.get(id)!,
      earliestFinish: ef.get(id)!,
      latestStart: ls.get(id)!,
      latestFinish: lf.get(id)!,
      slack: Math.max(0, ls.get(id)! - es.get(id)!),
    })
  }

  // Walk back from the latest-finishing task through zero-slack predecessors.
  const criticalPath: string[] = []
  if (order.length && remaining > 0) {
    let cur: string | undefined = order
      .filter((id) => Math.abs(ef.get(id)! - remaining) < EPS)
      .sort((a, b) => dur(b) - dur(a))[0]
    while (cur) {
      criticalPath.unshift(cur)
      const start: number = es.get(cur)!
      cur = preds
        .get(cur)!
        .filter((p) => Math.abs(ef.get(p)! - start) < EPS && schedule.get(p)!.slack < EPS)
        .sort((a, b) => dur(b) - dur(a))[0]
    }
  }

  return {
    blockedBy,
    dependents,
    impact,
    topBlockers,
    atRisk,
    ready,
    inCycle,
    schedule,
    criticalPath,
    criticalSet: new Set(criticalPath),
    remaining,
  }
}

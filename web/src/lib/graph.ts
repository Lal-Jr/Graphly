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

/**
 * Strongly connected components of `ids` under `next`, in Tarjan's order: every component comes
 * after all the components it reaches. Iterative, so a long dependency chain can't overflow the stack.
 */
export function components(ids: string[], next: (id: string) => readonly string[]): string[][] {
  const index = new Map<string, number>()
  const low = new Map<string, number>()
  const onStack = new Set<string>()
  const stack: string[] = []
  const out: string[][] = []
  let counter = 0
  for (const root of ids) {
    if (index.has(root)) continue
    const call: { id: string; i: number }[] = [{ id: root, i: 0 }]
    index.set(root, counter)
    low.set(root, counter++)
    stack.push(root)
    onStack.add(root)
    while (call.length) {
      const f = call[call.length - 1]
      const deps = next(f.id)
      if (f.i < deps.length) {
        const dep = deps[f.i++]
        if (!index.has(dep)) {
          index.set(dep, counter)
          low.set(dep, counter++)
          stack.push(dep)
          onStack.add(dep)
          call.push({ id: dep, i: 0 })
        } else if (onStack.has(dep)) {
          low.set(f.id, Math.min(low.get(f.id)!, index.get(dep)!))
        }
        continue
      }
      const id = f.id
      if (low.get(id) === index.get(id)) {
        const component: string[] = []
        let w: string
        do {
          w = stack.pop()!
          onStack.delete(w)
          component.push(w)
        } while (w !== id)
        out.push(component)
      }
      call.pop()
      if (call.length) {
        const parent = call[call.length - 1].id
        low.set(parent, Math.min(low.get(parent)!, low.get(id)!))
      }
    }
  }
  return out
}

/** Ids of tasks in a dependency cycle (concurrent edits can merge into one even though the UI prevents creating it). */
export function findCycles(tasks: GraphNode[]): Set<string> {
  const byId = new Map(tasks.map((t) => [t.id, t]))
  const result = new Set<string>()
  const deps = (id: string) => byId.get(id)!.dependsOn.filter((d) => byId.has(d))
  for (const c of components(
    tasks.map((t) => t.id),
    deps,
  )) {
    if (c.length > 1 || byId.get(c[0])!.dependsOn.includes(c[0])) c.forEach((id) => result.add(id))
  }
  return result
}

const popcount = (x: number) => {
  x -= (x >>> 1) & 0x55555555
  x = (x & 0x33333333) + ((x >>> 2) & 0x33333333)
  return (((x + (x >>> 4)) & 0x0f0f0f0f) * 0x01010101) >>> 24
}

/**
 * How many tasks transitively wait on each task: the size of its downstream set, itself excluded.
 * One DFS per task is quadratic, so instead collapse cycles into components and, walking them so
 * every component comes after those downstream of it, OR together bitsets of what each reaches.
 */
function transitiveImpact(keys: Iterable<string>, dependents: Map<string, string[]>): Map<string, number> {
  const next = (id: string) => dependents.get(id) ?? []
  const comps = components([...keys], next)
  const compOf = new Map<string, number>()
  comps.forEach((c, k) => c.forEach((id) => compOf.set(id, k)))
  // Every task reachable from a key ends up in some component; give each a bit.
  const bit = new Map<string, number>()
  for (const c of comps) for (const id of c) bit.set(id, bit.size)
  const words = (bit.size + 31) >>> 5
  // reach[k]: the tasks strictly downstream of component k (its own members excluded).
  const reach: Uint32Array[] = []
  const impact = new Map<string, number>()
  comps.forEach((c, k) => {
    const r = new Uint32Array(words)
    for (const id of c)
      for (const w of next(id)) {
        const d = compOf.get(w)!
        if (d === k) continue
        const rd = reach[d]
        for (let i = 0; i < words; i++) r[i] |= rd[i]
        for (const m of comps[d]) {
          const b = bit.get(m)!
          r[b >>> 5] |= 1 << (b & 31)
        }
      }
    reach.push(r)
    let n = 0
    for (let i = 0; i < words; i++) n += popcount(r[i])
    for (const id of c) impact.set(id, n + c.length - 1)
  })
  return impact
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

  // Only tasks something waits on have an impact entry, in the order they were first seen.
  const allImpact = transitiveImpact(dependents.keys(), dependents)
  const impact = new Map<string, number>()
  for (const id of dependents.keys()) impact.set(id, allImpact.get(id)!)
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
  for (let head = 0; head < queue.length; head++) {
    const id = queue[head]
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
  let remaining = 0
  for (const id of order) remaining = Math.max(remaining, ef.get(id)!)

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

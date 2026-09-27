import { createContext, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { analyze, type Analysis, type GraphNode } from '../lib/graph'
import { addWorkdays, forecast, nextWorkday, toISODate, type Forecast } from '../lib/forecast'
import { defaultSpread, simulateConfidence, type Confidence } from '../lib/montecarlo'
import { api } from '../api'
import type { Category, Issue, Member, Status } from '../types'
import { useToast } from '../ui/toast'
import { ProjectStore, type ProjectState } from './project'

export interface ProjectData extends ProjectState {
  store: ProjectStore
  statusMap: Map<string, Status>
  issueMap: Map<string, Issue>
  memberMap: Map<string, Member>
  /** Everything except epics — the units of work that flow across the board. */
  work: Issue[]
  epics: Issue[]
  category: (issue: Issue) => Category
  keyOf: (issue: Issue) => string
  byKey: (key: string) => Issue | undefined
  blockersOf: (id: string) => Issue[]
  blocksOf: (id: string) => Issue[]
  childrenOf: (epicId: string) => Issue[]
  nodes: GraphNode[]
  analysis: Analysis
  forecasts: Map<string, Forecast>
  dueDates: Map<string, string | null>
  /** Monte Carlo confidence over the same plan. */
  confidence: Confidence
}

const Ctx = createContext<ProjectData | null>(null)

export function ProjectProvider({ projectId, children }: { projectId: string; children: ReactNode }) {
  const toast = useToast()
  const [store, setStore] = useState(() => new ProjectStore(projectId, (m) => toast(m, 'error')))

  useEffect(() => {
    const s = store.projectId === projectId ? store : new ProjectStore(projectId, (m) => toast(m, 'error'))
    if (s !== store) setStore(s)
    void s.start()
    return () => s.stop()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId])

  const state = useSyncExternalStore(store.subscribe, store.getState)
  const data = useMemo(() => derive(store, state), [store, state])
  useDailySnapshot(data)
  return <Ctx.Provider value={data}>{children}</Ctx.Provider>
}

function derive(store: ProjectStore, s: ProjectState): ProjectData {
  const statusMap = new Map(s.statuses.map((st) => [st.id, st]))
  const issueMap = new Map(s.issues.map((i) => [i.id, i]))
  const memberMap = new Map(s.members.map((m) => [m.id, m]))
  const category = (i: Issue): Category => statusMap.get(i.statusId)?.category ?? 'todo'
  const keyOf = (i: Issue) => `${s.project?.key ?? ''}-${i.number}`
  const work = s.issues.filter((i) => i.type !== 'epic')
  const epics = s.issues.filter((i) => i.type === 'epic')

  const blockers = new Map<string, string[]>()
  const blocks = new Map<string, string[]>()
  for (const l of s.links) {
    if (!issueMap.has(l.blockerId) || !issueMap.has(l.blockedId)) continue
    blockers.set(l.blockedId, [...(blockers.get(l.blockedId) ?? []), l.blockerId])
    blocks.set(l.blockerId, [...(blocks.get(l.blockerId) ?? []), l.blockedId])
  }
  const children = new Map<string, Issue[]>()
  for (const i of s.issues) if (i.epicId) children.set(i.epicId, [...(children.get(i.epicId) ?? []), i])

  const workIds = new Set(work.map((i) => i.id))
  const nodes: GraphNode[] = work.map((i) => ({
    id: i.id,
    estimate: i.estimate,
    state: category(i),
    dependsOn: (blockers.get(i.id) ?? []).filter((b) => workIds.has(b)),
  }))
  const dueDates = new Map(work.map((i) => [i.id, i.dueDate]))
  // Presence and comment events also change state; only re-plan when the plan itself changed.
  const planKey = JSON.stringify([nodes, [...dueDates], work.map((i) => i.type)])
  if (planKey !== planCache.key) {
    const analysis = analyze(nodes)
    const types = new Map(work.map((i) => [i.id, i.type]))
    planCache.key = planKey
    planCache.analysis = analysis
    planCache.forecasts = forecast(analysis, dueDates)
    planCache.confidence = simulateConfidence(nodes, analysis, dueDates, defaultSpread((id) => types.get(id)))
  }
  const { analysis, forecasts, confidence } = planCache
  const byNumber = new Map(s.issues.map((i) => [i.number, i]))
  const toIssues = (ids: string[] | undefined) => (ids ?? []).map((id) => issueMap.get(id)!).filter(Boolean)

  return {
    ...s,
    store,
    statusMap,
    issueMap,
    memberMap,
    work,
    epics,
    category,
    keyOf,
    byKey: (key) => {
      const [k, n] = key.split('-')
      return k === s.project?.key ? byNumber.get(Number(n)) : undefined
    },
    blockersOf: (id) => toIssues(blockers.get(id)),
    blocksOf: (id) => toIssues(blocks.get(id)),
    childrenOf: (id) => children.get(id) ?? [],
    nodes,
    analysis,
    forecasts,
    dueDates,
    confidence,
  }
}

const planCache = {} as { key?: string; analysis: Analysis; forecasts: Map<string, Forecast>; confidence: Confidence }

/**
 * The planning engine runs in the browser, so whoever has the project open reports the day's
 * forecast (throttled). The standup uses these readings to show how the finish date moved.
 */
function useDailySnapshot(d: ProjectData) {
  const last = useRef('')
  const ready = d.phase === 'ready' && d.project
  const origin = nextWorkday(new Date())
  const date = (days: number) => (days > 0 ? toISODate(addWorkdays(origin, Math.ceil(days) - 1)) : null)
  const body = ready
    ? {
        finish: date(d.analysis.remaining),
        finishP85: date(d.confidence.p85),
        remaining: d.analysis.remaining,
        openIssues: d.work.filter((i) => d.category(i) !== 'done').length,
        blocked: d.analysis.blockedBy.size,
      }
    : null
  const key = body ? JSON.stringify(body) : ''
  const projectId = d.project?.id
  useEffect(() => {
    if (!key || key === last.current || !projectId) return
    const t = setTimeout(() => {
      last.current = key
      api.put(`/projects/${projectId}/snapshot`, JSON.parse(key)).catch(() => {})
    }, 4000)
    return () => clearTimeout(t)
  }, [key, projectId])
}

export function useProject() {
  const v = useContext(Ctx)
  if (!v) throw new Error('useProject must be used inside ProjectProvider')
  return v
}

/** Other people currently looking at an issue. */
export function useViewers(issueId: string, myId: string | undefined) {
  const { presence } = useProject()
  const seen = new Set<string>()
  return presence.filter((p) => p.viewing === issueId && p.userId !== myId && !seen.has(p.userId) && seen.add(p.userId))
}

/** For components that also render outside a project (top nav, command palette). */
export function useProjectOptional() {
  return useContext(Ctx)
}

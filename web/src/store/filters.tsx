import { createContext, useContext, useMemo, useState, type ReactNode } from 'react'
import type { Issue, IssueType, Priority } from '../types'
import { useSession } from './session'
import { useProject, type ProjectData } from './useProject'

export type QuickFilter = 'mine' | 'blocked' | 'critical' | 'atRisk' | 'late'

export interface Filters {
  q: string
  assignees: string[] // member ids, or 'none'
  types: IssueType[]
  priorities: Priority[]
  labels: string[]
  epics: string[] // epic ids, or 'none'
  quick: QuickFilter[]
}

const EMPTY: Filters = { q: '', assignees: [], types: [], priorities: [], labels: [], epics: [], quick: [] }

interface Ctx {
  filters: Filters
  set: (patch: Partial<Filters>) => void
  toggle: <K extends keyof Omit<Filters, 'q'>>(key: K, value: Filters[K][number]) => void
  clear: () => void
  active: boolean
  match: (i: Issue) => boolean
}

const FilterCtx = createContext<Ctx | null>(null)

export function matches(i: Issue, f: Filters, d: ProjectData, me: string | undefined): boolean {
  if (f.q) {
    const q = f.q.toLowerCase()
    if (!`${d.keyOf(i)} ${i.title} ${i.labels.join(' ')}`.toLowerCase().includes(q)) return false
  }
  if (f.assignees.length && !f.assignees.includes(i.assigneeId ?? 'none')) return false
  if (f.types.length && !f.types.includes(i.type)) return false
  if (f.priorities.length && !f.priorities.includes(i.priority)) return false
  if (f.labels.length && !f.labels.some((l) => i.labels.includes(l))) return false
  if (f.epics.length && !f.epics.includes(i.epicId ?? 'none')) return false
  for (const q of f.quick) {
    if (q === 'mine' && i.assigneeId !== me) return false
    if (q === 'blocked' && !d.analysis.blockedBy.has(i.id)) return false
    if (q === 'critical' && !d.analysis.criticalSet.has(i.id)) return false
    if (q === 'atRisk' && !d.analysis.atRisk.includes(i.id)) return false
    if (q === 'late' && !(d.forecasts.get(i.id)?.daysLate ?? 0)) return false
  }
  return true
}

export function FiltersProvider({ children }: { children: ReactNode }) {
  const [filters, setFilters] = useState<Filters>(EMPTY)
  const data = useProject()
  const { user } = useSession()
  const value = useMemo<Ctx>(() => {
    const active = JSON.stringify(filters) !== JSON.stringify(EMPTY)
    return {
      filters,
      set: (patch) => setFilters((f) => ({ ...f, ...patch })),
      toggle: (key, v) =>
        setFilters((f) => {
          const cur = f[key] as string[]
          return { ...f, [key]: cur.includes(v as string) ? cur.filter((x) => x !== v) : [...cur, v] }
        }),
      clear: () => setFilters(EMPTY),
      active,
      match: (i) => !active || matches(i, filters, data, user?.id),
    }
  }, [filters, data, user?.id])
  return <FilterCtx.Provider value={value}>{children}</FilterCtx.Provider>
}

export function useFilters() {
  const v = useContext(FilterCtx)
  if (!v) throw new Error('useFilters must be used inside FiltersProvider')
  return v
}

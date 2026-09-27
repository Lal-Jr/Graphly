import { useCallback, useEffect, useState } from 'react'
import { Link, NavLink, Navigate, Outlet, useLocation, useParams } from 'react-router-dom'
import { CommandPalette } from '../components/CommandPalette'
import { CreateIssueDialog } from '../components/CreateIssueDialog'
import { IssueModal } from '../components/IssueModal'
import { Sidebar } from '../components/Sidebar'
import { addWorkdays, formatDate, nextWorkday } from '../lib/forecast'
import { FiltersProvider } from '../store/filters'
import { useSession } from '../store/session'
import { ProjectProvider, useProject } from '../store/useProject'
import { Icon, type IconName } from '../ui/icons'
import { AvatarStack, EmptyState, Spinner } from '../ui/primitives'

interface View {
  path: string
  label: string
  icon: IconName
  /** One line under the page title saying what the view is for. */
  blurb: string
}

const PLAN: View[] = [
  { path: 'board', label: 'Board', icon: 'board', blurb: 'Drag work across the flow. Hover a card to trace what it waits on and what waits on it.' },
  { path: 'list', label: 'List', icon: 'list', blurb: 'Every issue with its forecast, slack and blockers, sortable.' },
  { path: 'graph', label: 'Dependency graph', icon: 'graph', blurb: 'The plan as it really is. Drag between nodes to link work; the coral line decides your ship date.' },
]
const UNDERSTAND: View[] = [
  { path: 'timeline', label: 'Forecast', icon: 'timeline', blurb: 'An auto-scheduled timeline from estimates and dependencies. Nothing here is dragged by hand.' },
  { path: 'insights', label: 'Insights', icon: 'insights', blurb: 'When it ships, how sure we are, and what is holding everyone up.' },
  { path: 'standup', label: 'Standup', icon: 'users', blurb: 'What moved since the last standup, and what moved the date.' },
]
const VIEWS = [...PLAN, ...UNDERSTAND]

export function ProjectLayout() {
  const { projectId } = useParams()
  return (
    <ProjectProvider projectId={projectId!}>
      <FiltersProvider>
        <ProjectShell />
      </FiltersProvider>
    </ProjectProvider>
  )
}

function ProjectShell() {
  const d = useProject()
  const { user } = useSession()
  const loc = useLocation()
  const [creating, setCreating] = useState(false)
  const openCreate = useCallback(() => setCreating(true), [])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (e.metaKey || e.ctrlKey || e.altKey || el.closest('input, textarea, select, [contenteditable]') || document.querySelector('.modal-backdrop')) return
      if (e.key === 'c') {
        e.preventDefault()
        setCreating(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  if (d.phase === 'error')
    return (
      <div className="app">
        <Sidebar />
        <main className="app-main">
          <EmptyState icon={<Icon name="warning" size={40} />} title={d.error === 'not-found' ? "This project doesn't exist or you don't have access" : "Couldn't load this project"}>
            <p className="muted">{d.error === 'not-found' ? 'Ask a workspace admin for an invite.' : d.error}</p>
            <Link to="/" className="btn btn-primary btn-md">
              Back to your work
            </Link>
          </EmptyState>
        </main>
      </div>
    )
  if (d.deleted) return <Navigate to="/" replace />

  const view: Pick<View, 'label' | 'blurb'> | undefined =
    VIEWS.find((v) => loc.pathname.endsWith(`/${v.path}`)) ??
    (loc.pathname.endsWith('/settings') ? { label: 'Project settings', blurb: 'Workflow, people, integrations.' } : undefined)
  const others = [...new Map(d.presence.filter((p) => p.userId !== user?.id).map((p) => [p.userId, p])).values()]

  return (
    <div className="app">
      <Sidebar onCreate={openCreate} footer={d.phase === 'ready' ? <ForecastPulse /> : null}>
        <div className="sb-project">
          <span className="project-avatar">{d.project?.key.slice(0, 2) ?? '··'}</span>
          <span className="ws-name">
            <b className="truncate">{d.project?.name ?? 'Loading…'}</b>
            <span className="muted small mono">{d.project?.key}</span>
          </span>
        </div>
        <span className="sidebar-heading">Plan</span>
        {PLAN.map((v) => (
          <ViewLink key={v.path} view={v} />
        ))}
        <span className="sidebar-heading">Understand</span>
        {UNDERSTAND.map((v) => (
          <ViewLink key={v.path} view={v} />
        ))}
        <NavLink to="settings" className="sidebar-link">
          <Icon name="settings" size={17} />
          Settings
        </NavLink>
      </Sidebar>

      <main className="app-main">
        <header className="page-head">
          <div className="page-title">
            <nav className="crumbs">
              <Link to="/">{user?.name.split(' ')[0]}’s work</Link>
              <Icon name="chevron-right" size={12} />
              <span>{d.project?.name}</span>
            </nav>
            <h1>{view?.label}</h1>
            {view?.blurb && <p className="page-blurb">{view.blurb}</p>}
          </div>
          <span className="spacer" />
          {others.length > 0 && (
            <span className="presence" title={`${others.map((o) => o.name).join(', ')} online`}>
              <span className="live-dot" />
              <AvatarStack users={others.map((o) => ({ ...o, key: o.userId }))} size={26} />
              <span className="muted small">{others.length} here now</span>
            </span>
          )}
          {d.phase === 'ready' && !d.connected && (
            <span className="offline" title="Reconnecting — changes you make are still saved">
              <Icon name="offline" size={14} /> Reconnecting…
            </span>
          )}
        </header>
        {d.phase === 'loading' ? (
          <div className="center-fill">
            <Spinner size={28} />
          </div>
        ) : (
          <Outlet />
        )}
      </main>
      <IssueModal />
      {creating && <CreateIssueDialog onClose={() => setCreating(false)} />}
      <CommandPalette onCreate={openCreate} />
    </div>
  )
}

function ViewLink({ view }: { view: View }) {
  return (
    <NavLink to={view.path} className="sidebar-link">
      <Icon name={view.icon} size={17} />
      {view.label}
    </NavLink>
  )
}

/** The forecast, always in view: when this ships, how sure we are, and how much is left. */
function ForecastPulse() {
  const d = useProject()
  const a = d.analysis
  const origin = nextWorkday(new Date())
  const at = (days: number) => formatDate(addWorkdays(origin, Math.ceil(days) - 1))
  const total = d.work.reduce((s, i) => s + i.estimate, 0)
  const done = d.work.filter((i) => d.category(i) === 'done').reduce((s, i) => s + i.estimate, 0)
  const pct = total ? Math.round((done / total) * 100) : 0
  const late = [...d.forecasts.values()].filter((f) => f.daysLate > 0).length

  return (
    <NavLink to="insights" className="pulse" title="Open insights">
      <span className="pulse-label">
        <Icon name="target" size={13} /> Ships
      </span>
      <b className="pulse-date">{a.remaining > 0 ? at(a.remaining) : 'All done'}</b>
      {a.remaining > 0 && (
        <span className="pulse-sub">
          85% sure by <b>{at(d.confidence.p85)}</b>
        </span>
      )}
      <span className="pulse-bar" title={`${done} of ${total} estimated days done`}>
        <span style={{ width: `${pct}%` }} />
      </span>
      <span className="pulse-foot">
        <span>{pct}% done</span>
        <span className="pulse-crit">
          <Icon name="route" size={12} /> {a.criticalPath.length}
        </span>
        {late > 0 && <span className="pulse-late">{late} late</span>}
      </span>
    </NavLink>
  )
}

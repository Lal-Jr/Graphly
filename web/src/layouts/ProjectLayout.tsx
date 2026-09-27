import { useCallback, useEffect, useState } from 'react'
import { Link, NavLink, Navigate, Outlet, useLocation, useParams } from 'react-router-dom'
import { CommandPalette } from '../components/CommandPalette'
import { CreateIssueDialog } from '../components/CreateIssueDialog'
import { IssueModal } from '../components/IssueModal'
import { TopNav } from '../components/TopNav'
import { FiltersProvider } from '../store/filters'
import { useSession } from '../store/session'
import { ProjectProvider, useProject } from '../store/useProject'
import { Icon, type IconName } from '../ui/icons'
import { AvatarStack, EmptyState, Spinner } from '../ui/primitives'

const VIEWS: { path: string; label: string; icon: IconName; hint?: string }[] = [
  { path: 'board', label: 'Board', icon: 'board' },
  { path: 'list', label: 'List', icon: 'list' },
  { path: 'standup', label: 'Standup', icon: 'users' },
  { path: 'graph', label: 'Dependency graph', icon: 'graph' },
  { path: 'timeline', label: 'Forecast', icon: 'timeline' },
  { path: 'insights', label: 'Insights', icon: 'insights' },
]

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
        <TopNav />
        <EmptyState icon={<Icon name="warning" size={40} />} title={d.error === 'not-found' ? "This project doesn't exist or you don't have access" : "Couldn't load this project"}>
          <p className="muted">{d.error === 'not-found' ? 'Ask a workspace admin for an invite.' : d.error}</p>
          <Link to="/" className="btn btn-primary btn-md">
            Back to your work
          </Link>
        </EmptyState>
      </div>
    )
  if (d.deleted) return <Navigate to="/" replace />

  const view = VIEWS.find((v) => loc.pathname.endsWith(`/${v.path}`)) ?? (loc.pathname.endsWith('/settings') ? { label: 'Project settings' } : null)
  const others = [...new Map(d.presence.filter((p) => p.userId !== user?.id).map((p) => [p.userId, p])).values()]

  return (
    <div className="app">
      <TopNav onCreate={openCreate} />
      <div className="project-shell">
        <aside className="sidebar">
          <div className="sidebar-project">
            <span className="project-avatar">{d.project?.key.slice(0, 2) ?? '··'}</span>
            <div>
              <b className="truncate">{d.project?.name ?? 'Loading…'}</b>
              <span className="muted small">Software project</span>
            </div>
          </div>
          <nav className="sidebar-nav">
            <span className="sidebar-heading">Planning</span>
            {VIEWS.map((v) => (
              <NavLink key={v.path} to={v.path} className="sidebar-link">
                <Icon name={v.icon} size={18} />
                {v.label}
              </NavLink>
            ))}
            <span className="sidebar-heading">Project</span>
            <NavLink to="settings" className="sidebar-link">
              <Icon name="settings" size={18} />
              Settings
            </NavLink>
          </nav>
          <div className="sidebar-foot muted small">
            <kbd>C</kbd> create · <kbd>⌘K</kbd> search
          </div>
        </aside>

        <main className="project-main">
          <div className="page-head">
            <div>
              <nav className="crumbs muted small">
                <Link to="/">Projects</Link>
                <span>/</span>
                <span>{d.project?.name}</span>
              </nav>
              <h1>{view?.label}</h1>
            </div>
            <span className="spacer" />
            {others.length > 0 && (
              <span className="presence" title={`${others.map((o) => o.name).join(', ')} online`}>
                <AvatarStack users={others.map((o) => ({ ...o, key: o.userId }))} size={28} />
                <span className="muted small">{others.length} online</span>
              </span>
            )}
            {d.phase === 'ready' && !d.connected && (
              <span className="offline" title="Reconnecting — changes you make are still saved">
                <Icon name="offline" size={14} /> Reconnecting…
              </span>
            )}
          </div>
          {d.phase === 'loading' ? (
            <div className="center-fill">
              <Spinner size={28} />
            </div>
          ) : (
            <Outlet />
          )}
        </main>
      </div>
      <IssueModal />
      {creating && <CreateIssueDialog onClose={() => setCreating(false)} />}
      <CommandPalette onCreate={openCreate} />
    </div>
  )
}

import { Suspense, lazy, type ReactNode } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { ProjectLayout } from './layouts/ProjectLayout'
import { AuthPage } from './pages/Auth'
import { Home } from './pages/Home'
import { InvitePage } from './pages/Invite'
import { ProfilePage } from './pages/Profile'
import { SessionProvider, useSession } from './store/session'
import { ThemeProvider } from './store/theme'
import { Logo } from './ui/icons'
import { ToastProvider } from './ui/toast'
import { BoardView } from './views/BoardView'
import { InsightsView } from './views/InsightsView'
import { ListView } from './views/ListView'
import { SettingsView } from './views/SettingsView'
import { StandupView } from './views/StandupView'

// The graph pulls in React Flow and dagre; load it (and the timeline) only when opened.
const GraphView = lazy(() => import('./views/GraphView').then((m) => ({ default: m.GraphView })))
const TimelineView = lazy(() => import('./views/TimelineView').then((m) => ({ default: m.TimelineView })))
const lazyView = (node: ReactNode) => <Suspense fallback={<div className="center-fill" />}>{node}</Suspense>

function RequireAuth({ children }: { children: ReactNode }) {
  const { user, loading } = useSession()
  const loc = useLocation()
  if (loading)
    return (
      <div className="boot">
        <Logo size={40} />
      </div>
    )
  if (!user) return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />
  return children
}

export default function App() {
  return (
    <ThemeProvider>
      <ToastProvider>
        <SessionProvider>
          <BrowserRouter>
            <Routes>
              <Route path="/login" element={<AuthPage mode="login" />} />
              <Route path="/signup" element={<AuthPage mode="signup" />} />
              <Route path="/invite/:token" element={<InvitePage />} />
              <Route path="/" element={<RequireAuth><Home /></RequireAuth>} />
              <Route path="/profile" element={<RequireAuth><ProfilePage /></RequireAuth>} />
              <Route path="/p/:projectId" element={<RequireAuth><ProjectLayout /></RequireAuth>}>
                <Route index element={<Navigate to="board" replace />} />
                <Route path="board" element={<BoardView />} />
                <Route path="list" element={<ListView />} />
                <Route path="standup" element={<StandupView />} />
                <Route path="graph" element={lazyView(<GraphView />)} />
                <Route path="timeline" element={lazyView(<TimelineView />)} />
                <Route path="insights" element={<InsightsView />} />
                <Route path="settings" element={<SettingsView />} />
              </Route>
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          </BrowserRouter>
        </SessionProvider>
      </ToastProvider>
    </ThemeProvider>
  )
}

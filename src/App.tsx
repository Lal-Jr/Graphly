import { Suspense, lazy, useCallback, useEffect, useState } from 'react'
import { BoardView } from './components/BoardView'
import { Insights } from './components/Insights'
import { TaskEditor } from './components/TaskEditor'
import { Avatar, ToastProvider, useToast } from './components/ui'
import type { Task } from './lib/types'
import { useBoard } from './store/context'
import { loadSample } from './store/sample'

const GraphView = lazy(() => import('./components/GraphView').then((m) => ({ default: m.GraphView })))

type View = 'board' | 'graph'

export default function App() {
  return (
    <ToastProvider>
      <Shell />
    </ToastProvider>
  )
}

function Shell() {
  const { board, tasks, loaded } = useBoard()
  const [view, setView] = useState<View>(() => (localStorage.getItem('graphly:view') as View) || 'board')
  const [openId, setOpenId] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [showInsights, setShowInsights] = useState(true)

  useEffect(() => {
    try {
      localStorage.setItem('graphly:view', view)
    } catch {}
  }, [view])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (el.closest('input, textarea, select, [contenteditable]')) return
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'z') {
        e.preventDefault()
        if (e.shiftKey) board.undo.redo()
        else board.undo.undo()
      } else if (e.key === 'g') setView((v) => (v === 'board' ? 'graph' : 'board'))
      else if (e.key === 'i') setShowInsights((s) => !s)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [board])

  const q = query.trim().toLowerCase()
  const filter = useCallback(
    (t: Task) => !q || t.title.toLowerCase().includes(q) || t.assignee.toLowerCase().includes(q) || t.description.toLowerCase().includes(q),
    [q],
  )
  const close = useCallback(() => setOpenId(null), [])

  return (
    <div className={`app${showInsights ? '' : ' no-insights'}`}>
      <Header view={view} setView={setView} query={query} setQuery={setQuery} showInsights={showInsights} setShowInsights={setShowInsights} />
      <main>
        {loaded && tasks.length === 0 ? (
          <Empty onSample={() => loadSample(board)} onNew={() => setOpenId(board.addTask({ title: '', status: 'todo' }))} />
        ) : view === 'board' ? (
          <BoardView filter={filter} onOpen={setOpenId} />
        ) : (
          <Suspense fallback={<div className="graph" />}>
            <GraphView filter={filter} onOpen={setOpenId} />
          </Suspense>
        )}
        {showInsights && tasks.length > 0 && <Insights onOpen={setOpenId} />}
      </main>
      {openId && <TaskEditor id={openId} onClose={close} onOpen={setOpenId} />}
    </div>
  )
}

function Header(props: {
  view: View
  setView: (v: View) => void
  query: string
  setQuery: (q: string) => void
  showInsights: boolean
  setShowInsights: (f: (s: boolean) => boolean) => void
}) {
  const { board, presence } = useBoard()
  const toast = useToast()
  const [editingName, setEditingName] = useState(false)
  const others = presence.filter((p) => p.clientId !== board.clientId)

  return (
    <header className="topbar">
      <div className="brand">
        <Logo />
        <span>Graphly</span>
      </div>

      <nav className="tabs" role="tablist">
        {(['board', 'graph'] as const).map((v) => (
          <button key={v} role="tab" aria-selected={props.view === v} className={props.view === v ? 'active' : ''} onClick={() => props.setView(v)}>
            {v === 'board' ? '▦ Board' : '⬡ Graph'}
          </button>
        ))}
      </nav>

      <input className="search" type="search" placeholder="Filter tasks…" value={props.query} onChange={(e) => props.setQuery(e.target.value)} />

      <div className="spacer" />

      <div className="presence">
        {others.slice(0, 5).map((p) => (
          <Avatar key={p.clientId} user={p} size={26} ring />
        ))}
        {others.length > 5 && <span className="more">+{others.length - 5}</span>}
        {editingName ? (
          <input
            className="name-input"
            autoFocus
            defaultValue={board.me.name}
            onBlur={(e) => {
              board.setName(e.target.value)
              setEditingName(false)
            }}
            onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
          />
        ) : (
          <button className="me" onClick={() => setEditingName(true)} title="Click to rename yourself">
            <Avatar user={board.me} size={26} />
            <span>{board.me.name}</span>
          </button>
        )}
      </div>

      <button
        className="ghost"
        onClick={() => {
          navigator.clipboard.writeText(location.href).then(
            () => toast('Invite link copied — anyone with it can edit this board'),
            () => toast(location.href),
          )
        }}
        title={`Room: ${board.room}`}
      >
        Share
      </button>
      <button className={`ghost${props.showInsights ? ' on' : ''}`} onClick={() => props.setShowInsights((s) => !s)} title="Toggle insights (i)">
        Insights
      </button>
    </header>
  )
}

function Empty({ onSample, onNew }: { onSample: () => void; onNew: () => void }) {
  return (
    <div className="empty">
      <Logo size={56} />
      <h1>An empty board</h1>
      <p>Add tasks and link them with dependencies. Graphly finds your blockers and works out the critical path as you go.</p>
      <div className="row">
        <button className="primary" onClick={onSample}>
          Load sample project
        </button>
        <button className="ghost" onClick={onNew}>
          Start from scratch
        </button>
      </div>
      <p className="muted small">Share the URL to collaborate in real time.</p>
    </div>
  )
}

function Logo({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden>
      <path d="M11 9.5l9 5M11 22.5l9-5" stroke="currentColor" strokeOpacity=".45" strokeWidth="2" />
      <circle cx="8" cy="8" r="5" fill="#6366f1" />
      <circle cx="24" cy="16" r="5" fill="#22c55e" />
      <circle cx="8" cy="24" r="5" fill="#f59e0b" />
    </svg>
  )
}

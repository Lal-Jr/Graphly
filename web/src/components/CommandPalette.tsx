import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTheme } from '../store/theme'
import { useProjectOptional } from '../store/useProject'
import { Icon, TypeIcon, type IconName } from '../ui/icons'
import { Modal } from '../ui/primitives'

interface Command {
  id: string
  label: string
  hint?: string
  icon: React.ReactNode
  run: () => void
}

/** ⌘K: jump to any issue or view, create, or switch theme. */
export function CommandPalette({ onCreate }: { onCreate?: () => void }) {
  const [open, setOpen] = useState(false)
  const [q, setQ] = useState('')
  const [active, setActive] = useState(0)
  const nav = useNavigate()
  const project = useProjectOptional()
  const { setMode } = useTheme()

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setOpen((o) => !o)
        setQ('')
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const commands = useMemo<Command[]>(() => {
    const out: Command[] = []
    const go = (icon: IconName, label: string, to: string) => out.push({ id: to, label, icon: <Icon name={icon} />, run: () => nav(to) })
    if (project?.project) {
      const base = `/p/${project.project.id}`
      if (onCreate) out.push({ id: 'create', label: 'Create issue', hint: 'C', icon: <Icon name="plus" />, run: onCreate })
      go('board', 'Go to board', `${base}/board`)
      go('list', 'Go to list', `${base}/list`)
      go('graph', 'Go to dependency graph', `${base}/graph`)
      go('timeline', 'Go to forecast timeline', `${base}/timeline`)
      go('insights', 'Go to insights', `${base}/insights`)
      go('settings', 'Project settings', `${base}/settings`)
      for (const i of project.issues)
        out.push({
          id: i.id,
          label: i.title,
          hint: project.keyOf(i),
          icon: <TypeIcon type={i.type} />,
          run: () => nav(`${base}/board?issue=${project.keyOf(i)}`, { replace: false }),
        })
    }
    go('home', 'Go to home', '/')
    out.push({ id: 'light', label: 'Switch to light theme', icon: <Icon name="sun" />, run: () => setMode('light') })
    out.push({ id: 'dark', label: 'Switch to dark theme', icon: <Icon name="moon" />, run: () => setMode('dark') })
    out.push({ id: 'system', label: 'Use system theme', icon: <Icon name="monitor" />, run: () => setMode('system') })
    return out
  }, [project, nav, onCreate, setMode])

  const shown = useMemo(() => {
    const s = q.trim().toLowerCase()
    const list = s ? commands.filter((c) => `${c.hint ?? ''} ${c.label}`.toLowerCase().includes(s)) : commands.filter((c) => !c.hint || c.id === 'create')
    return list.slice(0, 12)
  }, [q, commands])
  useEffect(() => setActive(0), [q])

  if (!open) return null
  const run = (c: Command) => {
    setOpen(false)
    // Keep the current view when jumping to an issue inside the same project.
    if (project?.project && project.issues.some((i) => i.id === c.id)) {
      const url = new URL(location.href)
      url.searchParams.set('issue', c.hint!)
      nav(url.pathname + url.search)
    } else c.run()
  }
  return (
    <Modal onClose={() => setOpen(false)} label="Command palette" width={600} className="palette">
      <div className="palette-search">
        <Icon name="search" size={18} />
        <input
          autoFocus
          placeholder={project ? `Search ${project.project?.key} issues, views and actions…` : 'Search actions…'}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              setActive((a) => Math.min(a + 1, shown.length - 1))
            } else if (e.key === 'ArrowUp') {
              e.preventDefault()
              setActive((a) => Math.max(a - 1, 0))
            } else if (e.key === 'Enter' && shown[active]) run(shown[active])
          }}
        />
        <kbd>esc</kbd>
      </div>
      <div className="palette-list">
        {shown.map((c, k) => (
          <button key={c.id} className={`palette-item${k === active ? ' active' : ''}`} onMouseEnter={() => setActive(k)} onClick={() => run(c)}>
            {c.icon}
            {c.hint && c.id !== 'create' && <span className="issue-key">{c.hint}</span>}
            <span className="truncate">{c.label}</span>
            {c.id === 'create' && <kbd>C</kbd>}
          </button>
        ))}
        {shown.length === 0 && <p className="muted palette-empty">No results for “{q}”</p>}
      </div>
    </Modal>
  )
}
